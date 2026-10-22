package ci

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"spikeloader/internal/db"
	"spikeloader/internal/env"
)

// CI state. Records live in PostgreSQL (projects, secrets, runs and the run id sequence); files stay
// on disk under .lab/ci/ (mode 0700):
//
//	mirrors/<project>.git          bare mirrors of the remotes
//	runs/<id>/<job>.log, <job>.tgz job logs and artifacts
//	cache/<project>/<hash>.tgz     job caches
var (
	ciDir    = filepath.Join(env.State, "ci")
	mirrors  = filepath.Join(ciDir, "mirrors")
	runsDir  = filepath.Join(ciDir, "runs")
	cacheDir = filepath.Join(ciDir, "cache")
)

func ensureDirs() error {
	for _, d := range []string{ciDir, mirrors, runsDir, cacheDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func runDir(id int64) string          { return filepath.Join(runsDir, strconv.FormatInt(id, 10)) }
func mirrorDir(project string) string { return filepath.Join(mirrors, project+".git") }

// JobLog is the path of a job's log.
func JobLog(run int64, idx int) string { return filepath.Join(runDir(run), strconv.Itoa(idx)+".log") }

// JobArtifacts is the path of a job's artifacts archive.
func JobArtifacts(run int64, idx int) string {
	return filepath.Join(runDir(run), strconv.Itoa(idx)+".tgz")
}

/* ---------- projects ---------- */

func loadProjects(ctx context.Context) ([]*Project, error) {
	rows, err := db.Pool.Query(ctx, "SELECT data FROM projects ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Project
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		p := &Project{}
		if err := json.Unmarshal(raw, p); err != nil {
			return nil, err
		}
		p.normalize()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (p *Project) normalize() {
	if p.ProtectedBranches == nil {
		p.ProtectedBranches = []string{p.DefaultBranch}
	}
	if p.Refs == nil {
		p.Refs = map[string]string{}
	}
	if p.ScheduleFired == nil {
		p.ScheduleFired = map[string]int64{}
	}
}

// saveProject snapshots the project now (call it with the engine lock held).
func saveProject(p *Project) {
	b, _ := json.Marshal(p)
	db.Write("INSERT INTO projects (id, created_at, data) VALUES ($1, $2, $3) ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data", p.ID, p.CreatedAt, b)
}

// deleteProjectRecords removes the project with its runs and secrets.
func deleteProjectRecords(id string) {
	db.Write("DELETE FROM runs WHERE project = $1", id)
	db.Write("DELETE FROM secrets WHERE scope = $1", id)
	db.Write("DELETE FROM projects WHERE id = $1", id)
}

/* ---------- runs ---------- */

func nextRunID(ctx context.Context) (int64, error) {
	var id int64
	err := db.Pool.QueryRow(ctx, "SELECT nextval('run_id_seq')").Scan(&id)
	return id, err
}

func loadRuns(ctx context.Context) ([]*Run, error) {
	rows, err := db.Pool.Query(ctx, "SELECT data FROM runs ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		r := &Run{}
		if err := json.Unmarshal(raw, r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// saveRun snapshots the run now (call it with the engine lock held).
const saveRunSQL = "INSERT INTO runs (id, project, status, data) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, data = EXCLUDED.data"

func saveRun(r *Run) {
	b, _ := json.Marshal(r)
	db.Write(saveRunSQL, r.ID, r.Project, r.Status, b)
}

// deleteRun removes the run record and its files (logs, artifacts).
func deleteRun(id int64) {
	db.Write("DELETE FROM runs WHERE id = $1", id)
	_ = os.RemoveAll(runDir(id))
}

/* ---------- secrets ---------- */

// Secrets by scope: a project id or "_global".
type Secret struct {
	Value     string
	Protected bool
}

type SecretInfo struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
}

type Secrets map[string]map[string]Secret

// Secret values are encrypted at rest (AES-256-GCM, "v1:" + base64 of iv|tag|ciphertext) with a key
// kept outside the database, in .lab/secrets.key (0600): a database dump or the Docker volume alone
// does not reveal them. Losing the key makes the stored secrets unreadable.
var (
	secretsKeyFile = filepath.Join(env.State, "secrets.key")
	keyOnce        sync.Once
	keyVal         []byte
	keyErr         error
)

func secretsKey() ([]byte, error) {
	keyOnce.Do(func() {
		b, err := os.ReadFile(secretsKeyFile)
		switch {
		case err == nil:
			keyVal, keyErr = base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		case errors.Is(err, fs.ErrNotExist):
			keyVal = make([]byte, 32)
			if _, keyErr = rand.Read(keyVal); keyErr == nil {
				keyErr = writeExclusive(secretsKeyFile, base64.StdEncoding.EncodeToString(keyVal)+"\n")
			}
		default:
			keyErr = err
		}
		if keyErr == nil && len(keyVal) != 32 {
			keyErr = fmt.Errorf("%s must hold 32 bytes (base64)", secretsKeyFile)
		}
	})
	return keyVal, keyErr
}

func writeExclusive(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

func gcm() (cipher.AEAD, error) {
	k, err := secretsKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func seal(value string) (string, error) {
	g, err := gcm()
	if err != nil {
		return "", err
	}
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	ct := g.Seal(nil, iv, []byte(value), nil) // ciphertext || tag
	data, tag := ct[:len(ct)-16], ct[len(ct)-16:]
	return "v1:" + base64.StdEncoding.EncodeToString(append(append(iv, tag...), data...)), nil
}

func unseal(stored string) (string, error) {
	if !strings.HasPrefix(stored, "v1:") {
		return stored, nil // written before encryption existed
	}
	raw, err := base64.StdEncoding.DecodeString(stored[3:])
	if err != nil || len(raw) < 28 {
		return "", errors.New("corrupt secret")
	}
	g, err := gcm()
	if err != nil {
		return "", err
	}
	iv, tag, data := raw[:12], raw[12:28], raw[28:]
	plain, err := g.Open(nil, iv, append(append([]byte{}, data...), tag...), nil)
	if err != nil {
		return "", errors.New("secret does not decrypt with .lab/secrets.key")
	}
	return string(plain), nil
}

func loadSecrets(ctx context.Context) (Secrets, error) {
	rows, err := db.Pool.Query(ctx, "SELECT scope, name, value, protected FROM secrets")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	s := Secrets{}
	for rows.Next() {
		var scope, name, value string
		var protected bool
		if err := rows.Scan(&scope, &name, &value, &protected); err != nil {
			return nil, err
		}
		v, err := unseal(value)
		if err != nil {
			return nil, fmt.Errorf("secret %s/%s: %w", scope, name, err)
		}
		if s[scope] == nil {
			s[scope] = map[string]Secret{}
		}
		s[scope][name] = Secret{Value: v, Protected: protected}
	}
	return s, rows.Err()
}

func saveSecret(scope, name, value string, protected bool) error {
	sealed, err := seal(value)
	if err != nil {
		return err
	}
	db.Write("INSERT INTO secrets (scope, name, value, protected) VALUES ($1, $2, $3, $4) ON CONFLICT (scope, name) DO UPDATE SET value = EXCLUDED.value, protected = EXCLUDED.protected", scope, name, sealed, protected)
	return nil
}

func deleteSecretRecord(scope, name string) {
	db.Write("DELETE FROM secrets WHERE scope = $1 AND name = $2", scope, name)
}
