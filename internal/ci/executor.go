package ci

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"spikeloader/internal/agent"
	"spikeloader/internal/env"
	"spikeloader/internal/loglimit"
	"spikeloader/internal/sshx"
)

// The executor runs one job in a throwaway microVM:
//   image → spawn VM → upload workspace, upstream artifacts, caches and the job bundle →
//   run steps (streamed into the job log) → save caches/artifacts → destroy the VM.

/* ---------- images ---------- */

var (
	imageBuildsMu sync.Mutex
	imageBuilds   = map[string]chan struct{}{}
	imageErrs     = map[string]error{}
)

// ListImages returns the images built on the outer VM.
func ListImages() ([]Image, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out := []Image{}
	if err := sshx.AgentJSON(ctx, "GET", "/images", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RemoveImage deletes a built image (the next job that needs it rebuilds it).
func RemoveImage(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return sshx.AgentJSON(ctx, "DELETE", "/images/"+name, nil, nil)
}

var (
	headerFrom = regexp.MustCompile(`(?m)^#\s*from:\s*([a-z0-9._-]+)\s*$`)
	headerSize = regexp.MustCompile(`(?m)^#\s*size:\s*(\d+[MG])\s*$`)
)

func imageHeader(script string) (from, size string) {
	if m := headerFrom.FindStringSubmatch(script); m != nil {
		from = m[1]
	}
	size = "8G"
	if m := headerSize.FindStringSubmatch(script); m != nil {
		size = m[1]
	}
	return from, size
}

func imageKey(name, parent, script, project string) string {
	h := sha256.Sum256([]byte(parent + script))
	key := name + "-" + hex.EncodeToString(h[:])[:10]
	if project != "" {
		key = project + "-" + key
	}
	return strings.ToLower(key)
}

// ImageDefinition is a built-in image (ci/images/<name>.sh).
type ImageDefinition struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Key         *string `json:"key"` // what its current script builds
	From        *string `json:"from"`
	Size        string  `json:"size"`
}

// BuiltinImages lists the image definitions shipped with spikeloader.
func BuiltinImages() []ImageDefinition {
	dir := filepath.Join(env.Root, "ci", "images")
	entries, _ := os.ReadDir(dir)
	scripts := map[string]string{}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sh") {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err == nil {
				n := strings.TrimSuffix(e.Name(), ".sh")
				scripts[n] = string(b)
				names = append(names, n)
			}
		}
	}
	var keyOf func(name string, depth int) string
	keyOf = func(name string, depth int) string {
		script, ok := scripts[name]
		if !ok || depth > 5 {
			return ""
		}
		parent := "base"
		if from, _ := imageHeader(script); from != "" {
			if parent = keyOf(from, depth+1); parent == "" {
				return ""
			}
		}
		return imageKey(name, parent, script, "")
	}
	out := []ImageDefinition{}
	for _, n := range names {
		from, size := imageHeader(scripts[n])
		d := ImageDefinition{Name: n, Description: strings.TrimSpace(strings.TrimLeft(strings.SplitN(scripts[n], "\n", 2)[0], "# ")), Size: size}
		if k := keyOf(n, 0); k != "" {
			d.Key = &k
		}
		if from != "" {
			d.From = &from
		}
		out = append(out, d)
	}
	return out
}

// ensureImage resolves an image name to a built image key, building it (and its parents) when
// needed. The repository's .spikeloader/images/<name>.sh wins over the built-in ci/images/<name>.sh;
// the key includes a hash of the script chain, so editing a script triggers a rebuild.
func ensureImage(ctx context.Context, name, project, sha string, log func(string), depth int) (string, error) {
	if depth > 5 {
		return "", errors.New("image `from:` chain is too deep")
	}
	script, local := readAt(ctx, project, sha, ".spikeloader/images/"+name+".sh")
	if !local {
		b, err := os.ReadFile(filepath.Join(env.Root, "ci", "images", name+".sh"))
		if err != nil {
			return "", fmt.Errorf("image %q not found (.spikeloader/images/%s.sh or ci/images/%s.sh)", name, name, name)
		}
		script = string(b)
	}
	from, size := imageHeader(script)
	parent := "base"
	if from != "" {
		var err error
		if parent, err = ensureImage(ctx, from, project, sha, log, depth+1); err != nil {
			return "", err
		}
	}
	owner := ""
	if local {
		owner = project
	}
	key := imageKey(name, parent, script, owner)

	built, err := ListImages()
	if err != nil {
		return "", err
	}
	if slices.ContainsFunc(built, func(i Image) bool { return i.Name == key }) {
		return key, nil
	}

	imageBuildsMu.Lock()
	wait, building := imageBuilds[key]
	if !building {
		wait = make(chan struct{})
		imageBuilds[key] = wait
	}
	imageBuildsMu.Unlock()
	if building {
		log(fmt.Sprintf("▶ waiting for image %s (being built by another job)\n", key))
		<-wait
		imageBuildsMu.Lock()
		err := imageErrs[key]
		imageBuildsMu.Unlock()
		return key, err
	}

	log(fmt.Sprintf("▶ building image %s (%s) — first use, this can take a few minutes\n", name, key))
	var buildErr error
	{
		prefix := regexp.MustCompile(`(?m)^(.)`)
		body, err := json.Marshal(agent.ImageBuild{Name: key, From: parent, Size: size, Script: script})
		if err != nil {
			return "", err
		}
		code, err := sshx.AgentFrames(context.Background(), "POST", "/images", bytes.NewReader(body), sshx.Stream{OnData: func(b []byte) {
			log(prefix.ReplaceAllString(string(b), "  │ $1"))
		}})
		if err != nil {
			buildErr = err
		} else if code != 0 {
			buildErr = fmt.Errorf("image build %s failed (exit %d)", key, code)
		}
	}
	imageBuildsMu.Lock()
	imageErrs[key] = buildErr
	delete(imageBuilds, key)
	close(wait)
	imageBuildsMu.Unlock()
	return key, buildErr
}

func tmpFile(content string) (string, error) {
	f, err := os.CreateTemp("", "spikeloader-")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return "", err
	}
	_, err = f.WriteString(content)
	return f.Name(), err
}

/* ---------- templates ---------- */

// TemplateReaderFor returns the `uses:` reader: the repository's .spikeloader/templates/ first (when
// project and sha are set), then the built-in ci/templates/.
func TemplateReaderFor(ctx context.Context, project, sha string) TemplateReader {
	local := map[string]string{}
	if project != "" && sha != "" {
		for _, f := range listAt(ctx, project, sha, ".spikeloader/templates") {
			if text, ok := readAt(ctx, project, sha, f); ok {
				name := strings.TrimPrefix(f, ".spikeloader/templates/")
				name = strings.TrimSuffix(strings.TrimSuffix(name, ".yml"), ".yaml")
				local[name] = text
			}
		}
	}
	return func(name string) (string, bool) {
		if t, ok := local[name]; ok {
			return t, true
		}
		b, err := os.ReadFile(filepath.Join(env.Root, "ci", "templates", name+".yml"))
		return string(b), err == nil
	}
}

// TemplateInfo describes a built-in template for the panel.
type TemplateInfo struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Image       string            `json:"image"`
	Inputs      map[string]string `json:"inputs"`
	Secrets     []string          `json:"secrets"`
}

// BuiltinTemplates lists ci/templates/**/*.yml.
func BuiltinTemplates() []TemplateInfo {
	dir := filepath.Join(env.Root, "ci", "templates")
	out := []TemplateInfo{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yml") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		text := string(b)
		var doc struct {
			Image   string         `yaml:"image"`
			Inputs  map[string]any `yaml:"inputs"`
			Secrets []string       `yaml:"secrets"`
		}
		_ = yaml.Unmarshal(b, &doc)
		rel, _ := filepath.Rel(dir, p)
		info := TemplateInfo{
			Name:        strings.TrimSuffix(rel, ".yml"),
			Description: strings.TrimSpace(strings.TrimLeft(strings.SplitN(text, "\n", 2)[0], "# ")),
			Image:       doc.Image,
			Inputs:      map[string]string{},
			Secrets:     nonNil(doc.Secrets),
		}
		if info.Image == "" {
			info.Image = "base"
		}
		for k, v := range doc.Inputs {
			if v != nil {
				info.Inputs[k] = jsString(normalizeYAML(v))
			} else {
				info.Inputs[k] = ""
			}
		}
		out = append(out, info)
		return nil
	})
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

func normalizeYAML(v any) any {
	switch x := v.(type) {
	case int:
		return int64(x)
	case uint64:
		return int64(x)
	}
	return v
}

/* ---------- job log ---------- */

var (
	tokenPieces = regexp.MustCompile(`[A-Za-z0-9+/=._~-]{20,}`)
	ctrlSeq     = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-ln-z]`)
	marker      = regexp.MustCompile(`##\[(step|end|skip)\] (\d+)(?: (-?\d+))?\s*$`)
)

// jobLog masks secrets and turns the runner's markers into readable lines plus step state.
type jobLog struct {
	mu       sync.Mutex
	out      *os.File
	capped   *loglimit.Writer
	err      error
	secrets  []string
	partial  string
	flush    *time.Timer
	onMarker func(kind string, idx, rc int)
	stepName func(idx int) string
}

func newJobLog(file string, secrets []string, onMarker func(string, int, int), stepName func(int) string) (*jobLog, error) {
	limit, err := loglimit.Config()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	truncated := false
	if info.Size() > 0 {
		tail, err := os.Open(file)
		if err != nil {
			f.Close()
			return nil, err
		}
		buffer := make([]byte, min(info.Size(), 128))
		_, err = tail.ReadAt(buffer, info.Size()-int64(len(buffer)))
		tail.Close()
		if err != nil {
			f.Close()
			return nil, err
		}
		truncated = bytes.Contains(buffer, []byte("log truncated at "))
	}
	// structured secrets (kubeconfig, JSON keys, PEM) leak in pieces: also mask every long token inside them
	uniq := map[string]bool{}
	for _, s := range secrets {
		uniq[s] = true
		for _, p := range tokenPieces.FindAllString(s, -1) {
			uniq[p] = true
		}
	}
	list := make([]string, 0, len(uniq))
	for s := range uniq {
		if len(s) >= 3 {
			list = append(list, s)
		}
	}
	sort.Slice(list, func(a, b int) bool { return len(list[a]) > len(list[b]) })
	return &jobLog{out: f, capped: &loglimit.Writer{Out: f, Limit: limit, Written: info.Size(), Truncated: truncated}, secrets: list, onMarker: onMarker, stepName: stepName}, nil
}

func (l *jobLog) render(text string) {
	if l.err != nil {
		return
	}
	_, l.err = l.capped.Write([]byte(text))
	if l.err != nil {
		log.Printf("ci: writing job log: %v", l.err)
	}
}

// mask hides secrets and drops terminal control sequences except colors (SGR), which the viewer renders.
func (l *jobLog) mask(text string) string {
	for _, s := range l.secrets {
		text = strings.ReplaceAll(text, s, "***")
	}
	// text never holds "\n" here (lines are split first), so every "\r" is a bare one
	return strings.ReplaceAll(ctrlSeq.ReplaceAllString(text, ""), "\r", "")
}

func (l *jobLog) Write(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := strings.Split(l.partial+text, "\n")
	l.partial = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		l.line(line)
	}
	// Bound an unterminated line as well as the file. Retain possible split secrets/markers.
	keep := 64
	for _, secret := range l.secrets {
		keep = max(keep, len(secret))
	}
	if len(l.partial) > 2*keep+64*1024 {
		cut := len(l.partial) - keep
		for _, secret := range l.secrets {
			if start := strings.LastIndex(l.partial[:min(len(l.partial), cut+len(secret))], secret); start >= 0 && start < cut && start+len(secret) > cut {
				cut = start
			}
		}
		l.render(l.mask(l.partial[:cut]))
		l.partial = l.partial[cut:]
	}
	if l.flush != nil {
		l.flush.Stop()
	}
	if l.partial != "" {
		l.flush = time.AfterFunc(500*time.Millisecond, func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.partial != "" && !strings.Contains(l.partial, "##[") {
				l.render(l.mask(l.partial))
				l.partial = ""
			}
		})
	}
}

func (l *jobLog) line(line string) {
	loc := marker.FindStringSubmatchIndex(line)
	if loc == nil {
		l.render(l.mask(line) + "\n")
		return
	}
	if before := line[:loc[0]]; before != "" {
		l.render(l.mask(before) + "\n")
	}
	kind := line[loc[2]:loc[3]]
	idx, _ := strconv.Atoi(line[loc[4]:loc[5]])
	rc := 0
	if loc[6] >= 0 {
		rc, _ = strconv.Atoi(line[loc[6]:loc[7]])
	}
	l.onMarker(kind, idx, rc)
	name := l.mask(l.stepName(idx))
	switch {
	case kind == "step":
		l.render("\n▶ " + name + "\n")
	case kind == "end" && rc == 0:
		l.render("✓ " + name + "\n")
	case kind == "end":
		l.render(fmt.Sprintf("✗ %s (exit %d)\n", name, rc))
	default:
		l.render("○ " + name + " (skipped)\n")
	}
}

func (l *jobLog) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.flush != nil {
		l.flush.Stop()
	}

	if l.partial != "" {
		l.line(l.partial)
		l.partial = ""
	}
	if err := l.out.Close(); err != nil && l.err == nil {
		l.err = err
		log.Printf("ci: closing job log: %v", err)
	}
}

/* ---------- job ---------- */

func (l *jobLog) checkpoint() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.partial != "" {
		l.render(l.mask(l.partial))
		l.partial = ""
	}
	if l.err != nil {
		return l.err
	}
	return l.out.Sync()
}

func size(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1048576:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/1048576)
	}
}

func envFile(vars map[string]string) string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		if nameRe.MatchString(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%s\n", k, sshx.Quote(vars[k]))
	}
	return b.String()
}

// tarPaths are paths as tar arguments: relative to /workspace, absolute, or ~ (root's home).
func tarPaths(paths []string) string {
	out := make([]string, len(paths))
	for i, p := range paths {
		if strings.HasPrefix(p, "~") {
			p = "/root" + p[1:]
		}
		out[i] = p
	}
	return strings.Join(out, " ")
}

// runScript is the job runner inside the microVM: sources env and secrets, then runs each step,
// printing the markers the log parser turns into step state.
func runScript(steps []phasedStep) string {
	lines := []string{
		"#!/bin/bash",
		"cd /workspace || exit 1",
		"set -a; . /ci/env; . /ci/secrets; set +a",
		"rm -f /ci/secrets",
		// template inputs may reference variables (with: { tag: "app:$CI_COMMIT_SHORT_SHA" }): $VAR and ${VAR} only, never eval
		`expand() { local s=$1 out="" m; while [[ $s =~ \$\{?([A-Za-z_][A-Za-z0-9_]*)\}? ]]; do m=${BASH_REMATCH[0]}; out+=${s%%"$m"*}${!BASH_REMATCH[1]-}; s=${s#*"$m"}; done; printf '%s' "$out$s"; }`,
		`for v in ${!INPUT_@}; do export "$v=$(expand "${!v}")"; done`,
		"rc=0",
		`step() { printf '##[step] %s\n' "$1"; bash -eo pipefail "/ci/steps/$1.sh" </dev/null; local r=$?; printf '##[end] %s %s\n' "$1" "$r"; return $r; }`,
	}
	for i, s := range steps {
		if s.phase == "after" {
			// `after` always runs; its failure fails the job only if the main steps passed
			lines = append(lines, fmt.Sprintf("step %d || { r=$?; [ $rc -ne 0 ] || rc=$r; }", i))
		} else {
			lines = append(lines, fmt.Sprintf("if [ $rc -eq 0 ]; then step %d || rc=$?; else printf '##[skip] %d\\n'; fi", i, i))
		}
	}
	return strings.Join(append(lines, "exit $rc", ""), "\n")
}

type phasedStep struct {
	StepSpec
	phase string
}

// jobContext is what the engine hands the executor.
type jobContext struct {
	run         *Run
	job         *Job
	project     Project // snapshot
	workspace   string
	cacheHashes []string
	upstream    []Job // snapshots of the jobs this one depends on
	variables   map[string]string
	secrets     map[string]string
	// mutate applies a change to the run/job under the engine lock and persists it.
	mutate func(func())
}

// runJob executes one job and returns its exit code (124 = timed out, 130 = canceled).
func runJob(ctx context.Context, jc jobContext) (result int) {
	run, job := jc.run, jc.job
	recovery, recoveryErr := readRecovery(run.ID, job.Idx)
	if recoveryErr != nil && !errors.Is(recoveryErr, os.ErrNotExist) {
		appendLog(JobLog(run.ID, job.Idx), "\nrecovery: "+recoveryErr.Error()+"\n")
		return 1
	}
	resuming := recoveryErr == nil
	if resuming {
		if err := os.Truncate(JobLog(run.ID, job.Idx), recovery.Prefix); err != nil {
			appendLog(JobLog(run.ID, job.Idx), err.Error()+"\n")
			return 1
		}
		plain, err := unseal(recovery.Secrets)
		if err != nil {
			appendLog(JobLog(run.ID, job.Idx), err.Error()+"\n")
			return 1
		}
		if err := json.Unmarshal([]byte(plain), &jc.secrets); err != nil {
			appendLog(JobLog(run.ID, job.Idx), err.Error()+"\n")
			return 1
		}
		jc.cacheHashes = recovery.Hashes
	}
	var spec JobSpec
	var steps []phasedStep
	jc.mutate(func() {
		spec = job.Spec
		for _, s := range spec.Before {
			steps = append(steps, phasedStep{s, "before"})
		}
		for _, s := range spec.Steps {
			steps = append(steps, phasedStep{s, "main"})
		}
		for _, s := range spec.After {
			steps = append(steps, phasedStep{s, "after"})
		}
		if !resuming || len(job.Steps) != len(steps) {
			job.Steps = make([]StepState, len(steps))
			for i, s := range steps {
				job.Steps[i] = StepState{Name: s.Name, Phase: s.phase, Status: "pending"}
			}
		}
	})

	secretValues := make([]string, 0, len(jc.secrets))
	for _, v := range jc.secrets {
		secretValues = append(secretValues, v)
	}
	logw, err := newJobLog(JobLog(run.ID, job.Idx), secretValues,
		func(kind string, idx, rc int) {
			jc.mutate(func() {
				if idx < 0 || idx >= len(job.Steps) {
					return
				}
				st := &job.Steps[idx]
				now := time.Now().UnixMilli()
				switch kind {
				case "step":
					st.Status = "running"
					if st.StartedAt == nil {
						st.StartedAt = &now
					}
				case "end":
					st.Status, st.ExitCode = "success", ptr(rc)
					if st.EndedAt == nil {
						st.EndedAt = &now
					}
					if rc != 0 {
						st.Status = "failed"
					}
				default:
					st.Status = "skipped"
				}
			})
		},
		func(idx int) string {
			if idx >= 0 && idx < len(steps) {
				return steps[idx].Name
			}
			return fmt.Sprintf("step %d", idx)
		})
	if err != nil {
		appendLog(JobLog(run.ID, job.Idx), "job log: "+err.Error()+"\n")
		return 1
	}
	say := logw.Write
	t0 := time.Now()
	vm := -1
	if resuming {
		vm = recovery.VM
		t0 = time.UnixMilli(recovery.StartedAt)
	}

	defer func() {
		if vm >= 0 {
			// Keep prepared/running VMs until the engine durably records the outcome.
			jc.mutate(func() { job.VM = ptr(vm) })
		}
		jc.mutate(func() {
			for i := range job.Steps {
				if s := job.Steps[i].Status; s == "running" || s == "pending" {
					job.Steps[i].Status = "skipped"
				}
			}
		})
		logw.Close()
		if logw.err != nil {
			jc.mutate(func() { job.Reason = ptr("job log: " + logw.err.Error()) })
			result = 1
		}
	}()

	code, err := func() (int, error) {
		var inVM func(string, sshx.Stream) int
		inVM = func(cmd string, opt sshx.Stream) int {
			if opt.OnData == nil && opt.Stdout == nil {
				opt.OnData = func(b []byte) { say(string(b)) }
			}
			return sshx.AgentExec(ctx, vm, cmd, opt)
		}
		if !resuming {
			say(fmt.Sprintf("spikeloader CI · run #%d · %s · attempt %d\n", run.ID, job.Name, job.Attempt))
			say(fmt.Sprintf("commit %s on %s — %s\n\n", short(run.Sha, 10), run.Branch, run.Message))

			var missing []string
			for _, s := range spec.Secrets {
				if _, ok := jc.secrets[s]; !ok {
					missing = append(missing, s)
				}
			}
			if len(missing) > 0 {
				return 1, fmt.Errorf("missing secrets: %s (add them in the project settings)", strings.Join(missing, ", "))
			}

			image, err := ensureImage(ctx, spec.Image, jc.project.ID, run.Sha, say, 0)
			if err != nil {
				return 1, err
			}
			if ctx.Err() != nil {
				return 130, nil
			}

			boot := time.Now()
			say(fmt.Sprintf("▶ starting microVM (%s · %d vCPU · %d MiB · disk %s)\n", spec.Image, spec.Vcpus, spec.Mem, spec.Disk))
			// not cancelable: killing the local ssh would leave the remote spawn running; the VM is removed afterwards instead
			var spawned agent.VMID
			bootCtx, bootCancel := context.WithTimeout(context.Background(), 3*time.Minute)
			err = sshx.AgentJSON(bootCtx, "POST", "/vms", agent.Spawn{VCPUs: spec.Vcpus, MemMiB: spec.Mem, Image: image, Disk: spec.Disk, Label: fmt.Sprintf("run #%d · %s", run.ID, job.Name)}, &spawned)
			bootCancel()
			if err == nil {
				vm = spawned.ID
				jc.mutate(func() { job.VM = ptr(vm) })
			}
			if ctx.Err() != nil {
				return 130, nil
			}
			if err != nil || vm < 0 {
				return 1, fmt.Errorf("could not start the microVM: %w", err)
			}
			say(fmt.Sprintf("  vm%d ready in %.1fs\n", vm, time.Since(boot).Seconds()))

			upload := func(file, what string) error {
				if rc := inVM("mkdir -p /workspace && tar --warning=no-timestamp -xzPf - -C /workspace", sshx.Stream{Stdin: file}); rc != 0 {
					return fmt.Errorf("upload of %s failed (exit %d)", what, rc)
				}
				return nil
			}

			// workspace, upstream artifacts and caches
			if err := upload(jc.workspace, "the workspace"); err != nil {
				return 1, err
			}
			for _, up := range jc.upstream {
				if !up.HasArtifacts {
					continue
				}
				say(fmt.Sprintf("▶ artifacts from %s\n", up.Name))
				if err := upload(JobArtifacts(run.ID, up.Idx), "artifacts of "+up.Name); err != nil {
					return 1, err
				}
			}
		}
		// caches are scoped by branch: a job writes only its branch's entry and reads it, falling back to
		// the default branch's — so a branch can never poison what the default branch (deploys) restores
		cacheFile := func(branch, key string) string {
			h := sha1.Sum([]byte(branch + "\x00" + key))
			return filepath.Join(cacheDir, jc.project.ID, hex.EncodeToString(h[:])+".tgz")
		}
		type cacheEntry struct {
			CacheSpec
			label, file, fallback string
		}
		caches := make([]cacheEntry, len(spec.Cache))
		for i, c := range spec.Cache {
			hash := ""
			if i < len(jc.cacheHashes) {
				hash = jc.cacheHashes[i]
			}
			key := c.Key + "-" + hash
			caches[i] = cacheEntry{c, key, cacheFile(run.Branch, key), cacheFile(jc.project.DefaultBranch, key)}
		}
		if !resuming {
			upload := func(file, what string) error {
				if rc := inVM("mkdir -p /workspace && tar --warning=no-timestamp -xzPf - -C /workspace", sshx.Stream{Stdin: file}); rc != 0 {
					return fmt.Errorf("upload of %s failed (exit %d)", what, rc)
				}
				return nil
			}
			for _, c := range caches {
				hit := ""
				for _, f := range []string{c.file, c.fallback} {
					if _, err := os.Stat(f); err == nil {
						hit = f
						break
					}
				}
				if hit == "" {
					say(fmt.Sprintf("  cache %s: miss\n", c.label))
					continue
				}
				from := ""
				if hit != c.file {
					from = " (from " + jc.project.DefaultBranch + ")"
				}
				say(fmt.Sprintf("▶ restoring cache %s%s\n", c.label, from))
				if err := upload(hit, "cache "+c.label); err != nil {
					return 1, err
				}
			}

			// job bundle: env, secrets, steps and the runner
			if err := uploadBundle(ctx, jc, steps, inVM); err != nil {
				return 1, err
			}
			if err := logw.checkpoint(); err != nil {
				return 1, err
			}
			info, err := os.Stat(JobLog(run.ID, job.Idx))
			if err != nil {
				return 1, err
			}
			secretJSON, err := json.Marshal(jc.secrets)
			if err != nil {
				return 1, err
			}
			encrypted, err := seal(string(secretJSON))
			if err != nil {
				return 1, err
			}
			recovery = jobRecovery{VM: vm, Task: fmt.Sprintf("run-%d-job-%d-attempt-%d", run.ID, job.Idx, job.Attempt), Prefix: info.Size(), Hashes: jc.cacheHashes, StartedAt: t0.UnixMilli(), Secrets: encrypted}
			if err := writeRecovery(run.ID, job.Idx, recovery); err != nil {
				return 1, err
			}
		}

		remaining := time.Duration(spec.TimeoutS)*time.Second - time.Since(t0)
		startCtx, startCancel := context.WithTimeout(ctx, 15*time.Second)
		var state agent.JobState
		err := sshx.AgentJSON(startCtx, "POST", "/jobs/"+recovery.Task, agent.JobStart{VM: vm, TimeoutS: max(int(remaining.Seconds()), 1), MaxLogBytes: logw.capped.Limit, Steps: len(steps)}, &state)
		startCancel()
		if err != nil {
			return 1, fmt.Errorf("start/reattach agent job: %w", err)
		}
		followCtx, followCancel := context.WithTimeout(ctx, max(remaining, time.Second)+30*time.Second)
		exit, followErr := sshx.AgentFollow(followCtx, recovery.Task, func(b []byte) { say(string(b)) })
		followCancel()
		if ctx.Err() != nil {
			cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := sshx.AgentJSON(cancelCtx, "DELETE", "/jobs/"+recovery.Task, nil, nil); err != nil {
				say("agent cancel: " + err.Error() + "\n")
			}
			say("\n■ canceled\n")
			return 130, nil
		}
		if errors.Is(followErr, context.DeadlineExceeded) {
			say("\nagent stream unavailable past the job deadline\n")
			return 124, nil
		}
		if followErr != nil {
			return 1, followErr
		}
		if exit == 124 {
			say(fmt.Sprintf("\n✗ timed out after %ds\n", spec.TimeoutS))
		}
		ok := exit == 0

		// caches (on success) and artifacts
		if ok {
			for _, c := range caches {
				_ = os.MkdirAll(filepath.Dir(c.file), 0o700)
				tmp := c.file + ".tmp"
				f, err := os.Create(tmp)
				if err != nil {
					continue
				}
				rc := inVM(fmt.Sprintf(`cd /workspace && set -- $(ls -d %s 2>/dev/null) && [ $# -gt 0 ] && tar -czPf - "$@"`, tarPaths(c.Paths)), sshx.Stream{Stdout: f})
				f.Close()
				if st, err := os.Stat(tmp); rc == 0 && err == nil && st.Size() > 0 {
					_ = os.Rename(tmp, c.file)
					say(fmt.Sprintf("▶ saved cache %s (%s)\n", c.label, size(st.Size())))
				} else {
					_ = os.Remove(tmp)
				}
			}
		}
		if art := spec.Artifacts; art != nil && len(art.Paths) > 0 && (art.When == "always" || (art.When == "success") == ok) {
			file := JobArtifacts(run.ID, job.Idx)
			f, err := os.Create(file)
			if err == nil {
				rc := inVM(fmt.Sprintf(`cd /workspace && set -- $(ls -d %s 2>/dev/null) && [ $# -gt 0 ] && tar -czf - "$@"`, strings.Join(art.Paths, " ")), sshx.Stream{Stdout: f})
				f.Close()
				if st, err := os.Stat(file); rc == 0 && err == nil && st.Size() > 0 {
					jc.mutate(func() { job.HasArtifacts = true })
					say(fmt.Sprintf("▶ artifacts saved (%s): %s\n", size(st.Size()), strings.Join(art.Paths, ", ")))
				} else {
					_ = os.Remove(file)
					say(fmt.Sprintf("  no artifacts matched: %s\n", strings.Join(art.Paths, ", ")))
				}
			}
		}
		if ok {
			say(fmt.Sprintf("\n✓ job succeeded in %.1fs\n", time.Since(t0).Seconds()))
		} else {
			say(fmt.Sprintf("\n✗ job failed (exit %d) in %.1fs\n", exit, time.Since(t0).Seconds()))
		}
		return exit, nil
	}()
	if err != nil {
		say("\n✗ " + err.Error() + "\n")
		if ctx.Err() != nil {
			return 130
		}
		return 1
	}
	return code
}

func uploadBundle(ctx context.Context, jc jobContext, steps []phasedStep, inVM func(string, sshx.Stream) int) error {
	run, job := jc.run, jc.job
	bundle, err := os.MkdirTemp("", "spikeloader-job-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(bundle)
	ci := filepath.Join(bundle, "ci")
	if err := os.MkdirAll(filepath.Join(ci, "steps"), 0o700); err != nil {
		return err
	}
	vars := map[string]string{
		"CI": "true", "SPIKELOADER": "true",
		"CI_PROJECT": jc.project.ID, "CI_RUN_ID": strconv.FormatInt(run.ID, 10),
		"CI_JOB": job.Name, "CI_JOB_ATTEMPT": strconv.Itoa(job.Attempt), "CI_STAGE": job.Stage,
		"CI_EVENT": run.Event, "CI_BRANCH": run.Branch, "CI_COMMIT_SHA": run.Sha,
		"CI_COMMIT_SHORT_SHA": short(run.Sha, 8), "CI_COMMIT_MESSAGE": run.Message,
		"CI_COMMIT_AUTHOR": run.Author, "CI_DEFAULT_BRANCH": jc.project.DefaultBranch,
	}
	for k, v := range jc.variables {
		vars[k] = v
	}
	for k, v := range job.Spec.Variables {
		vars[k] = v
	}
	if err := os.WriteFile(filepath.Join(ci, "env"), []byte(envFile(vars)), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ci, "secrets"), []byte(envFile(jc.secrets)), 0o600); err != nil {
		return err
	}
	for i, s := range steps {
		if err := os.WriteFile(filepath.Join(ci, "steps", strconv.Itoa(i)+".sh"), []byte(s.Run+"\n"), 0o600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(ci, "run.sh"), []byte(runScript(steps)), 0o700); err != nil {
		return err
	}
	tgz := filepath.Join(bundle, "bundle.tgz")
	if b, err := exec.CommandContext(ctx, "tar", "-czf", tgz, "-C", bundle, "ci").CombinedOutput(); err != nil {
		return fmt.Errorf("packing the job bundle: %s", strings.TrimSpace(string(b)))
	}
	if rc := inVM("rm -rf /ci && tar --warning=no-timestamp -xzf - -C / && chmod 600 /ci/secrets", sshx.Stream{Stdin: tgz}); rc != 0 {
		return errors.New("upload of the job bundle failed")
	}
	return nil
}

func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ReapVMs removes leftover CI microVMs (e.g. after the daemon restarted mid-job).
func ReapVMs(keep map[int]bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var vms []agent.MicroVM
	if err := sshx.AgentJSON(ctx, "GET", "/vms", nil, &vms); err != nil {
		return err
	}
	for _, v := range vms {
		if v.Owner != nil && *v.Owner == "ci" && !keep[v.ID] {
			pid := 0
			if v.PID != nil {
				pid = *v.PID
			}
			if err := sshx.AgentJSON(ctx, "DELETE", fmt.Sprintf("/vms/%d?pid=%d", v.ID, pid), nil, nil); err != nil {
				var conflict *sshx.AgentError
				if errors.As(err, &conflict) && conflict.Status == 409 {
					continue
				}
				return err
			}
		}
	}
	return nil
}
