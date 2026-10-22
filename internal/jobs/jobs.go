// Package jobs runs and records the panel's background operations (starting and stopping the outer
// VM), shown as events on the Infrastructure page. History is kept in PostgreSQL (panel_jobs).
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"spikeloader/internal/db"
	"spikeloader/internal/env"
)

const (
	maxJobs   = 50
	maxOutput = 256 * 1024
)

// Job is one operation (JSON shape: web/lib/types.ts Job).
type Job struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Command   string `json:"command"`
	Status    string `json:"status"` // running | ok | error
	ExitCode  *int   `json:"exitCode"`
	Output    string `json:"output"`
	StartedAt int64  `json:"startedAt"`
	EndedAt   *int64 `json:"endedAt"`
	Group     string `json:"group,omitempty"` // jobs of a group never overlap ("outer")
}

var (
	mu   sync.Mutex
	jobs = map[string]*Job{}
)

// Load reads the history; operations that were running when the panel stopped are marked failed.
func Load(ctx context.Context) error {
	rows, err := db.Pool.Query(ctx, "SELECT data FROM panel_jobs ORDER BY started_at DESC LIMIT $1", maxJobs)
	if err != nil {
		return err
	}
	defer rows.Close()
	mu.Lock()
	defer mu.Unlock()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		j := &Job{}
		if err := json.Unmarshal(raw, j); err != nil {
			continue
		}
		if j.Status == "running" {
			code, ended := -1, j.StartedAt
			j.Status, j.ExitCode, j.EndedAt = "error", &code, &ended
			j.Output += "\n[interrupted: the panel was restarted]"
			save(j)
		}
		jobs[j.ID] = j
	}
	return rows.Err()
}

func save(j *Job) {
	b, _ := json.Marshal(j)
	db.Write("INSERT INTO panel_jobs (id, started_at, data) VALUES ($1, $2, $3) ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data", j.ID, j.StartedAt, b)
}

func sorted() []*Job {
	out := make([]*Job, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].StartedAt > out[b].StartedAt })
	return out
}

// List returns copies of the operations, newest first.
func List() []Job {
	mu.Lock()
	defer mu.Unlock()
	out := []Job{}
	for _, j := range sorted() {
		out = append(out, *j)
	}
	return out
}

// Get returns a copy of one operation.
func Get(id string) (Job, bool) {
	mu.Lock()
	defer mu.Unlock()
	j, ok := jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// Busy reports whether an operation of the group is running.
func Busy(group string) bool {
	mu.Lock()
	defer mu.Unlock()
	for _, j := range jobs {
		if j.Group == group && j.Status == "running" {
			return true
		}
	}
	return false
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// clean strips ANSI sequences and resolves '\r' (progress bars), keeping each line's final state.
func clean(s string) string {
	lines := strings.Split(ansi.ReplaceAllString(s, ""), "\n")
	for i, l := range lines {
		parts := strings.Split(l, "\r")
		last := ""
		for _, p := range parts {
			if p != "" {
				last = p
			}
		}
		lines[i] = last
	}
	return strings.Join(lines, "\n")
}

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type output struct {
	j   *Job
	raw []byte
}

func (o *output) Write(p []byte) (int, error) {
	mu.Lock()
	defer mu.Unlock()
	o.raw = append(o.raw, p...)
	if len(o.raw) > maxOutput {
		o.raw = o.raw[len(o.raw)-maxOutput:]
	}
	o.j.Output = clean(string(o.raw))
	return len(p), nil
}

// Start runs a command from the repository root in the background and records it; display is the
// command as shown in the panel.
func Start(title, group, display string, name string, args ...string) Job {
	j := &Job{ID: uuid(), Title: title, Command: display, Status: "running", StartedAt: time.Now().UnixMilli(), Group: group}
	mu.Lock()
	jobs[j.ID] = j
	for _, old := range sorted()[min(len(jobs), maxJobs):] {
		delete(jobs, old.ID)
		db.Write("DELETE FROM panel_jobs WHERE id = $1", old.ID)
	}
	save(j)
	mu.Unlock()

	cmd := exec.Command(name, args...)
	cmd.Dir = env.Root
	cmd.Env = append(os.Environ(), "TERM=dumb")
	out := &output{j: j}
	cmd.Stdout, cmd.Stderr = out, out
	go func() {
		err := cmd.Run()
		code := 0
		if err != nil {
			code = 1
			if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() >= 0 {
				code = ee.ExitCode()
			} else {
				_, _ = out.Write([]byte("\n" + err.Error() + "\n"))
			}
		}
		mu.Lock()
		ended := time.Now().UnixMilli()
		j.ExitCode, j.EndedAt = &code, &ended
		j.Status = "ok"
		if code != 0 {
			j.Status = "error"
		}
		save(j)
		mu.Unlock()
		log.Printf("job %q finished: exit %d", title, code)
	}()
	mu.Lock()
	defer mu.Unlock()
	return *j
}
