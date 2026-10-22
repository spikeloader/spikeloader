package ci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// git runs without prompts: credentials come from the URL, the user's SSH agent/keys or git config.
func git(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new",
		// only real transports: no ext:: (runs commands) or other remote helpers, whatever the URL says
		"GIT_ALLOW_PROTOCOL=https:http:ssh:git:file",
	)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(Redact(msg))
	}
	return out.String(), nil
}

var credentials = regexp.MustCompile(`(\w+://)[^/@\s]+@`)

// Redact hides credentials embedded in a URL (https://user:token@host/...).
func Redact(s string) string { return credentials.ReplaceAllString(s, "$1***@") }

// syncMirror clones (first time) or fetches the project's bare mirror and returns branch → sha.
func syncMirror(ctx context.Context, project, url string) (map[string]string, error) {
	dir := mirrorDir(project)
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		_ = os.RemoveAll(dir)
		steps := [][]string{
			{"init", "--bare", "--quiet", dir},
			{"-C", dir, "remote", "add", "origin", url},
			{"-C", dir, "config", "remote.origin.fetch", "+refs/heads/*:refs/heads/*"},
			{"-C", dir, "config", "uploadpack.allowAnySHA1InWant", "true"},
		}
		for _, a := range steps {
			if _, err := git(ctx, "", time.Minute, a...); err != nil {
				return nil, err
			}
		}
	} else {
		// syncs of a project never overlap, so any lock here was left by an interrupted git
		for _, f := range []string{"config.lock", "packed-refs.lock", "shallow.lock", "FETCH_HEAD.lock"} {
			_ = os.Remove(filepath.Join(dir, f))
		}
		_ = filepath.WalkDir(filepath.Join(dir, "refs"), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".lock") {
				_ = os.Remove(p)
			}
			return nil
		})
		cur, err := git(ctx, dir, time.Minute, "remote", "get-url", "origin")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(cur) != url {
			if _, err := git(ctx, dir, time.Minute, "remote", "set-url", "origin", url); err != nil {
				return nil, err
			}
		}
	}
	if _, err := git(ctx, dir, 10*time.Minute, "fetch", "--prune", "--quiet", "origin"); err != nil {
		return nil, err
	}
	out, err := git(ctx, dir, time.Minute, "for-each-ref", "refs/heads", "--format=%(refname:strip=2) %(objectname)")
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if name, sha, ok := strings.Cut(l, " "); ok && !strings.HasPrefix(name, "-") {
			refs[name] = sha
		}
	}
	return refs, nil
}

func resolveRef(ctx context.Context, project, ref string) (string, error) {
	out, err := git(ctx, mirrorDir(project), time.Minute, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	return strings.TrimSpace(out), err
}

func commitInfo(ctx context.Context, project, sha string) (message, author string) {
	out, err := git(ctx, mirrorDir(project), time.Minute, "log", "-1", "--format=%s%x00%an", sha)
	if err != nil {
		return "", ""
	}
	message, author, _ = strings.Cut(strings.TrimSpace(out), "\x00")
	return message, author
}

// readAt reads a file at a commit; ok=false when it does not exist.
func readAt(ctx context.Context, project, sha, file string) (string, bool) {
	out, err := git(ctx, mirrorDir(project), time.Minute, "show", sha+":"+file)
	return out, err == nil
}

// listAt lists the files under a directory at a commit (recursively).
func listAt(ctx context.Context, project, sha, dir string) []string {
	out, err := git(ctx, mirrorDir(project), time.Minute, "ls-tree", "-r", "--name-only", sha, "--", dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	return files
}

// packWorkspace prepares a job workspace: a shallow checkout of the commit (with .git, origin
// pointing at the real remote without credentials) packed as a tar.gz at out, plus the hash of the
// files listed in each cache entry (for cache keys).
func packWorkspace(ctx context.Context, project, url, sha, branch string, depth int, out string, hashFiles [][]string) ([]string, error) {
	work, err := os.MkdirTemp("", "spikeloader-ws-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	if _, err := git(ctx, "", time.Minute, "init", "--quiet", work); err != nil {
		return nil, err
	}
	fetch := []string{"fetch", "--quiet", "--no-tags"}
	if depth > 0 {
		fetch = append(fetch, "--depth="+strconv.Itoa(depth))
	}
	if _, err := git(ctx, work, 10*time.Minute, append(fetch, "file://"+mirrorDir(project), sha)...); err != nil {
		return nil, err
	}
	if branch == "" {
		branch = "ci"
	}
	if _, err := git(ctx, work, time.Minute, "checkout", "--quiet", "-B", branch, sha); err != nil {
		return nil, err
	}
	if _, err := git(ctx, work, time.Minute, "remote", "add", "origin", strings.Replace(Redact(url), "***@", "", 1)); err != nil {
		return nil, err
	}
	hashes := make([]string, len(hashFiles))
	for i, files := range hashFiles {
		h := sha256.New()
		for _, f := range files {
			h.Write([]byte(f))
			p := filepath.Join(work, f)
			if rel, err := filepath.Rel(work, p); err != nil || strings.HasPrefix(rel, "..") {
				continue // only files of the checkout
			}
			if b, err := os.ReadFile(p); err == nil {
				h.Write(b)
			}
		}
		hashes[i] = hex.EncodeToString(h.Sum(nil))[:16]
	}
	if b, err := exec.CommandContext(ctx, "tar", "-czf", out, "-C", work, ".").CombinedOutput(); err != nil {
		return nil, errors.New(strings.TrimSpace(string(b)))
	}
	return hashes, nil
}
