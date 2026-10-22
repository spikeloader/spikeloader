// Package env locates the spikeloader checkout and reads its .env configuration.
package env

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Root is the repository checkout: $SPIKELOADER_ROOT, else the parent of the directory holding the
// running binary when that looks like a checkout, else the working directory.
var Root = findRoot()

// State is the local state directory (.lab/): keys, tokens, CI files.
var State = filepath.Join(Root, ".lab")

func findRoot() string {
	if r := os.Getenv("SPIKELOADER_ROOT"); r != "" {
		return r
	}
	looksLike := func(dir string) bool {
		_, err := os.Stat(filepath.Join(dir, ".env"))
		_, err2 := os.Stat(filepath.Join(dir, "ci", "images"))
		return err == nil && err2 == nil
	}
	if exe, err := os.Executable(); err == nil {
		// binaries are built into .lab/bin/
		for dir := filepath.Dir(exe); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
			if looksLike(dir) {
				return dir
			}
		}
	}
	wd, _ := os.Getwd()
	for dir := wd; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if looksLike(dir) {
			return dir
		}
	}
	return wd
}

var line = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)=(.*)$`)

// Load reads .env on every call (it is small and edits must apply without a restart).
// Values are trimmed and lose one pair of surrounding double quotes, like the shell scripts see them.
func Load() map[string]string {
	out, err := ReadFile(filepath.Join(Root, ".env"))
	if err != nil {
		return map[string]string{}
	}
	return out
}

// ReadFile reads the lab configuration at an explicit path, reporting I/O errors.
func ReadFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, l := range strings.Split(string(data), "\n") {
		m := line.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		v := strings.TrimSpace(m[2])
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = v[1 : len(v)-1]
		}
		out[m[1]] = v
	}
	return out, nil
}

// Get returns one .env value, or def when it is missing or empty.
func Get(key, def string) string {
	if v := Load()[key]; v != "" {
		return v
	}
	return def
}

// Version is the contents of the VERSION file ("dev" when missing).
func Version() string {
	b, err := os.ReadFile(filepath.Join(Root, "VERSION"))
	if err != nil {
		return "dev"
	}
	return strings.TrimSpace(string(b))
}
