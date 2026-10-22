package ci

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"spikeloader/internal/agent"
	"spikeloader/internal/env"
	"spikeloader/internal/jobs"
	"spikeloader/internal/lab"
	"spikeloader/internal/loglimit"
	"spikeloader/internal/sshx"
)

// The engine lives in the daemon: it polls the remotes, fires schedules, keeps the run queue and
// dispatches jobs to microVMs while they fit in the outer VM. It also drives the outer VM: started
// when a job waits, stopped after CI_IDLE_SHUTDOWN_MIN minutes idle.
//
// Concurrency: every field below is guarded by mu. Slow work (git, ssh, image builds) runs without
// the lock and applies its results under it. Persisting snapshots happens under the lock too, so a
// stored run is always consistent.

var (
	terminal  = set(JobSuccess, JobFailed, JobCanceled, JobSkipped)
	activeRun = set(RunQueued, RunRunning, RunWaiting)
	reserved  = set("images", "runs", "validate", "library", "secrets")
	secretRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	idRe      = regexp.MustCompile(`[^a-z0-9]+`)
)

// Error is an engine error with the HTTP status it maps to.
type Error struct {
	Msg    string
	Status int
}

func (e *Error) Error() string { return e.Msg }

func fail(status int, format string, a ...any) error {
	return &Error{fmt.Sprintf(format, a...), status}
}

type workspace struct {
	done   chan struct{}
	file   string
	hashes map[int][]string
	err    error
}

type hostCapacity struct{ cpus, memMiB int }

// Engine is the CI engine (one per daemon).
type Engine struct {
	executor   jobExecutor
	runStore   runStore
	mu         sync.Mutex
	projects   []*Project
	runs       map[int64]*Run
	secrets    Secrets
	aborts     map[string]context.CancelFunc
	workspaces map[int64]*workspace
	saveTimers map[int64]*time.Timer
	syncing    map[string]chan struct{}
	pipelines  map[string]*Pipeline // by sha, for schedules (nil = no pipeline)

	outerOK      bool
	outer        string // off → starting → online
	host         *hostCapacity
	lastActivity time.Time
	reaped       bool
	lastMinute   int64
}

// New loads the state from PostgreSQL and starts the scheduler.
func New(ctx context.Context) (*Engine, error) {
	if err := ensureDirs(); err != nil {
		return nil, err
	}
	projects, err := loadProjects(ctx)
	if err != nil {
		return nil, err
	}
	secrets, err := loadSecrets(ctx)
	if err != nil {
		return nil, err
	}
	runs, err := loadRuns(ctx)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		projects: projects, runs: map[int64]*Run{}, secrets: secrets,
		aborts: map[string]context.CancelFunc{}, workspaces: map[int64]*workspace{},
		saveTimers: map[int64]*time.Timer{}, syncing: map[string]chan struct{}{},
		pipelines: map[string]*Pipeline{}, outer: "off", lastActivity: time.Now(),
	}
	now := time.Now().UnixMilli()
	type resumed struct {
		r *Run
		j *Job
	}
	var resume []resumed
	for _, r := range runs {
		if activeRun[r.Status] {
			for _, j := range r.Jobs {
				if j.Status == JobRunning {
					checkpoint, err := readRecovery(r.ID, j.Idx)
					if err == nil {
						j.VM = ptr(checkpoint.VM)
						resume = append(resume, resumed{r, j})
					} else {
						reason := "interrupted before the agent owned the job"
						if !errors.Is(err, os.ErrNotExist) {
							reason = "invalid recovery checkpoint: " + err.Error()
						}
						j.Status, j.Reason, j.EndedAt, j.VM = JobFailed, ptr(reason), ptr(now), nil
					}
				}
			}
		}
		e.runs[r.ID] = r
	}
	for _, item := range resume {
		ctx, cancel := context.WithCancel(context.Background())
		e.aborts[fmt.Sprintf("%d:%d", item.r.ID, item.j.Idx)] = cancel
		go e.execute(ctx, item.r, item.j)
	}
	go e.loop(time.Second, func() { e.mu.Lock(); e.tick(); e.mu.Unlock() })
	go e.loop(10*time.Second, e.housekeeping)
	return e, nil
}

func (e *Engine) loop(every time.Duration, f func()) {
	f()
	for range time.Tick(every) {
		f()
	}
}

/* ---------- settings ---------- */

func number(v string) float64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(n) {
		return 0
	}
	return n
}

// maxJobs is the optional hard cap on parallel jobs; 0 = as many as the outer VM can hold.
func maxJobs() int { return int(math.Max(number(env.Load()["CI_MAX_JOBS"]), 0)) }

func idleShutdownMin() int {
	v, ok := env.Load()["CI_IDLE_SHUTDOWN_MIN"]
	if !ok || v == "" {
		return 30
	}
	return int(math.Max(number(v), 0))
}

// capacity is what jobs may use: the outer VM's RAM minus 1 GiB, and its vCPUs overcommitted 2×.
func (e *Engine) capacity() *hostCapacity {
	if e.host == nil {
		return nil
	}
	return &hostCapacity{e.host.cpus * 2, max(e.host.memMiB-1024, 512)}
}

/* ---------- projects ---------- */

func (e *Engine) project(id string) (*Project, error) {
	for _, p := range e.projects {
		if p.ID == id {
			return p, nil
		}
	}
	return nil, fail(404, "project %s not found", id)
}

func (e *Engine) view(p *Project) ProjectView {
	v := ProjectView{
		ID: p.ID, Name: p.Name, URL: Redact(p.URL), DefaultBranch: p.DefaultBranch, PollS: p.PollS,
		ProtectedBranches: append([]string{}, p.ProtectedBranches...),
		NotifyURL:         p.NotifyURL, NotifyOn: p.NotifyOn, CreatedAt: p.CreatedAt, Refs: p.Refs,
		LastSyncAt: p.LastSyncAt, LastError: p.LastError, ScheduleFired: p.ScheduleFired,
	}
	var last *Run
	for _, r := range e.runs {
		if r.Project == p.ID && (last == nil || r.ID > last.ID) {
			last = r
		}
	}
	if last != nil {
		s := last.Summary()
		v.LastRun = &s
	}
	return v
}

func clampPoll(v any) int {
	n := jsNumber(v)
	if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return 0
	}
	return int(math.Min(math.Max(math.Round(n), 15), 86400))
}

func randomToken() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ProjectInput is POST /api/ci/projects.
type ProjectInput struct {
	Name              string   `json:"name"`
	URL               string   `json:"url"`
	DefaultBranch     string   `json:"defaultBranch"`
	ProtectedBranches []string `json:"protectedBranches"`
	PollS             any      `json:"pollS"`
}

// CreateProject registers a repository and fetches it.
func (e *Engine) CreateProject(in ProjectInput) (ProjectView, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	name := strings.TrimSpace(in.Name)
	id := strings.Trim(idRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if id == "" {
		return ProjectView{}, fail(400, "name is required")
	}
	if reserved[id] {
		return ProjectView{}, fail(400, "%q is a reserved name", id)
	}
	if _, err := e.project(id); err == nil {
		return ProjectView{}, fail(409, "project %s already exists", id)
	}
	url := strings.TrimSpace(in.URL)
	if url == "" || strings.ContainsAny(url, " \t\n\r") || strings.HasPrefix(url, "-") {
		return ProjectView{}, fail(400, "a git URL is required")
	}
	branch := strings.TrimSpace(in.DefaultBranch)
	if branch == "" {
		branch = "main"
	}
	poll := in.PollS
	if poll == nil {
		poll = int64(60)
	}
	p := &Project{
		ID: id, Name: name, URL: url, DefaultBranch: branch, PollS: clampPoll(poll),
		WebhookToken: randomToken(), NotifyOn: "failure", CreatedAt: time.Now().UnixMilli(),
		Refs: map[string]string{}, ScheduleFired: map[string]int64{},
		ProtectedBranches: in.ProtectedBranches,
	}
	if err := validateProtectedBranches(p.ProtectedBranches); err != nil {
		return ProjectView{}, err
	}
	p.normalize()
	e.projects = append(e.projects, p)
	saveProject(p)
	go func() { _ = e.Sync(context.Background(), id) }()
	return e.view(p), nil
}

// ProjectPatch is PATCH /api/ci/projects/:id (absent fields are left alone).
type ProjectPatch struct {
	Name              *string   `json:"name"`
	URL               *string   `json:"url"`
	DefaultBranch     *string   `json:"defaultBranch"`
	ProtectedBranches *[]string `json:"protectedBranches"`
	PollS             any       `json:"pollS"`
	NotifyURL         *string   `json:"notifyUrl"`
	NotifyOn          *string   `json:"notifyOn"`
	RotateToken       bool      `json:"rotateToken"`
}

// UpdateProject changes a project's settings.
func (e *Engine) UpdateProject(id string, patch ProjectPatch) (ProjectView, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := e.project(id)
	if err != nil {
		return ProjectView{}, err
	}
	if patch.ProtectedBranches != nil {
		if err := validateProtectedBranches(*patch.ProtectedBranches); err != nil {
			return ProjectView{}, err
		}
	}
	if patch.Name != nil {
		if n := strings.TrimSpace(*patch.Name); n != "" {
			p.Name = n
		}
	}
	if patch.URL != nil {
		if u := strings.TrimSpace(*patch.URL); u != "" && u != Redact(p.URL) {
			if strings.ContainsAny(u, " \t\n\r") || strings.HasPrefix(u, "-") {
				return ProjectView{}, fail(400, "invalid git URL")
			}
			p.URL = u
		}
	}
	if patch.DefaultBranch != nil {
		if b := strings.TrimSpace(*patch.DefaultBranch); b != "" {
			p.DefaultBranch = b
		}
	}
	if patch.PollS != nil {
		p.PollS = clampPoll(patch.PollS)
	}
	if patch.NotifyURL != nil {
		u := strings.TrimSpace(*patch.NotifyURL)
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return ProjectView{}, fail(400, "notification URL must be http(s)")
		}
		p.NotifyURL = u
	}
	if patch.NotifyOn != nil {
		if !set("failure", "always", "never")[*patch.NotifyOn] {
			return ProjectView{}, fail(400, "notifyOn must be failure, always or never")
		}
		p.NotifyOn = *patch.NotifyOn
	}
	if patch.RotateToken {
		p.WebhookToken = randomToken()
	}
	if patch.ProtectedBranches != nil {
		p.ProtectedBranches = append([]string{}, (*patch.ProtectedBranches)...)
	}
	saveProject(p)
	return e.view(p), nil
}

// DeleteProject removes a project with its runs, secrets, mirror and caches.
func (e *Engine) DeleteProject(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.project(id); err != nil {
		return err
	}
	for rid, r := range e.runs {
		if r.Project == id {
			e.cancel(r)
			e.storage().Delete(rid)
			delete(e.runs, rid)
		}
	}
	kept := e.projects[:0]
	for _, p := range e.projects {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	e.projects = kept
	delete(e.secrets, id)
	deleteProjectRecords(id)
	_ = os.RemoveAll(mirrorDir(id))
	_ = os.RemoveAll(filepath.Join(cacheDir, id))
	return nil
}

// ProjectDetail is GET /api/ci/projects/:id.
func (e *Engine) ProjectDetail(id string) (any, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := e.project(id)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"project": e.view(p), "webhookToken": p.WebhookToken,
		"secrets": e.secretNames(id), "runs": e.listRuns(id, 50),
	}, nil
}

// ProjectView returns one project as the browser sees it.
func (e *Engine) ProjectView(id string) (ProjectView, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := e.project(id)
	if err != nil {
		return ProjectView{}, err
	}
	return e.view(p), nil
}

/* ---------- secrets ---------- */

func (e *Engine) secretNames(scope string) []SecretInfo {
	names := []SecretInfo{}
	for n, secret := range e.secrets[scope] {
		names = append(names, SecretInfo{Name: n, Protected: secret.Protected})
	}
	sort.Slice(names, func(i, j int) bool { return names[i].Name < names[j].Name })
	return names
}

// SecretNames lists the secret names of a project or of "_global" (values are write-only).
func (e *Engine) SecretNames(scope string) ([]SecretInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if scope != "_global" {
		if _, err := e.project(scope); err != nil {
			return nil, err
		}
	}
	return e.secretNames(scope), nil
}

// SetSecret stores (encrypted) a secret value.
func (e *Engine) SetSecret(scope, name string, value any, protected *bool) ([]SecretInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if scope != "_global" {
		if _, err := e.project(scope); err != nil {
			return nil, err
		}
	}
	if !secretRe.MatchString(name) {
		return nil, fail(400, "secret names must look like MY_SECRET")
	}
	v, ok := value.(string)
	if !ok || v == "" {
		return nil, fail(400, "value is required")
	}
	protect := true
	if previous, ok := e.secrets[scope][name]; ok {
		protect = previous.Protected
	}
	if protected != nil {
		protect = *protected
	}
	if err := saveSecret(scope, name, v, protect); err != nil {
		return nil, err
	}
	if e.secrets[scope] == nil {
		e.secrets[scope] = map[string]Secret{}
	}
	e.secrets[scope][name] = Secret{Value: v, Protected: protect}
	return e.secretNames(scope), nil
}

// DeleteSecret removes a secret.
func (e *Engine) DeleteSecret(scope, name string) []SecretInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.secrets[scope], name)
	deleteSecretRecord(scope, name)
	return e.secretNames(scope)
}

/* ---------- triggers ---------- */

// CheckWebhook validates a project's webhook token and fetches the remote.
func (e *Engine) CheckWebhook(id, token string) error {
	e.mu.Lock()
	p, err := e.project(id)
	var want string
	if err == nil {
		want = p.WebhookToken
	}
	e.mu.Unlock()
	if err != nil {
		return err
	}
	a, b := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(want))
	if token == "" || subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
		return fail(401, "invalid webhook token")
	}
	go func() { _ = e.Sync(context.Background(), id) }()
	return nil
}

// Sync fetches the remote and starts push runs for branches that moved. Concurrent calls for the
// same project share one fetch.
func (e *Engine) Sync(ctx context.Context, id string) error {
	e.mu.Lock()
	if wait, ok := e.syncing[id]; ok {
		e.mu.Unlock()
		<-wait
		return nil
	}
	p, err := e.project(id)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	done := make(chan struct{})
	e.syncing[id] = done
	url := p.URL
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.syncing, id)
		e.mu.Unlock()
		close(done)
	}()

	refs, syncErr := syncMirror(ctx, id, url)

	e.mu.Lock()
	p, err = e.project(id)
	if err != nil { // deleted meanwhile
		e.mu.Unlock()
		return nil
	}
	type moved struct{ branch, sha string }
	var changed []moved
	if syncErr == nil {
		first := p.LastSyncAt == nil || (len(p.Refs) == 0 && p.LastError != nil)
		if !first {
			for b, sha := range refs {
				if p.Refs[b] != sha {
					changed = append(changed, moved{b, sha})
				}
			}
		}
	}
	e.mu.Unlock()

	sort.Slice(changed, func(a, b int) bool { return changed[a].branch < changed[b].branch })
	for _, m := range changed {
		if _, err := e.createRun(ctx, id, m.branch, m.sha, "push", "refs/heads/"+m.branch); err != nil {
			log.Printf("ci: %s %s: %v", id, m.branch, err)
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if p, err = e.project(id); err != nil {
		return nil
	}
	if syncErr != nil {
		p.LastError = ptr(syncErr.Error())
	} else {
		p.Refs, p.LastError = refs, nil
	}
	p.LastSyncAt = ptr(time.Now().UnixMilli())
	saveProject(p)
	return nil
}

// Trigger starts a manual run of a branch, tag or commit (default: the default branch).
func (e *Engine) Trigger(ctx context.Context, id, ref string) (RunSummary, error) {
	if err := e.Sync(ctx, id); err != nil {
		return RunSummary{}, err
	}
	e.mu.Lock()
	p, err := e.project(id)
	if err != nil {
		e.mu.Unlock()
		return RunSummary{}, err
	}
	if p.LastError != nil {
		msg := *p.LastError
		e.mu.Unlock()
		return RunSummary{}, fail(502, "git: %s", msg)
	}
	wanted := strings.TrimSpace(ref)
	if wanted == "" {
		wanted = p.DefaultBranch
	}
	branch := wanted
	sourceRef := ""
	if _, ok := p.Refs[wanted]; ok {
		sourceRef = "refs/heads/" + wanted
	}
	if _, ok := p.Refs[wanted]; !ok {
		names := make([]string, 0, len(p.Refs))
		for b := range p.Refs {
			names = append(names, b)
		}
		sort.Strings(names)
		for _, b := range names {
			if strings.HasPrefix(p.Refs[b], wanted) {
				branch = b
				break
			}
		}
	}
	sha := p.Refs[branch]
	name := p.Name
	e.mu.Unlock()

	if sha == "" {
		if sha, err = resolveRef(ctx, id, wanted); err != nil || sha == "" {
			return RunSummary{}, fail(404, "ref %q not found in %s", wanted, name)
		}
	}
	run, err := e.createRun(ctx, id, branch, sha, "manual", sourceRef)
	if err != nil {
		return RunSummary{}, err
	}
	if run == nil {
		return RunSummary{}, fail(404, "no %s at %s", PipelineFiles[0], short(sha, 8))
	}
	return *run, nil
}

func loadPipeline(ctx context.Context, project, sha string) (pl *Pipeline, errs []string, found bool) {
	for _, f := range PipelineFiles {
		if text, ok := readAt(ctx, project, sha, f); ok {
			pl, errs = ParsePipeline(text, TemplateReaderFor(ctx, project, sha))
			return pl, errs, true
		}
	}
	return nil, nil, false
}

// createRun creates a run, or returns nil when the commit has no pipeline or the trigger does not
// apply.
func (e *Engine) createRun(ctx context.Context, projectID, branch, sha, event, sourceRef string) (*RunSummary, error) {
	pl, errs, found := loadPipeline(ctx, projectID, sha)
	if !found {
		return nil, nil
	}
	if pl != nil && event == "push" {
		if !pl.On.Enabled.Push {
			return nil, nil
		}
		if pl.On.Push != nil && !anyGlob(pl.On.Push, branch) {
			return nil, nil
		}
	}
	if pl != nil && event == "manual" && !pl.On.Manual {
		return nil, fail(400, "this pipeline disables manual runs (on.manual: false)")
	}
	message, author := commitInfo(ctx, projectID, sha)
	id, err := nextRunID(ctx)
	if err != nil {
		return nil, err
	}
	run := &Run{
		ID: id, Project: projectID, Branch: branch, Sha: sha, Message: message, Author: author,
		Event: event, Status: RunQueued, CreatedAt: time.Now().UnixMilli(),
		SourceRef: sourceRef,
		Variables: map[string]string{}, Stages: []string{}, Jobs: []*Job{},
	}
	if pl == nil {
		run.Status, run.Error, run.EndedAt = RunFailed, ptr(strings.Join(errs, "\n")), ptr(time.Now().UnixMilli())
	} else {
		run.Variables, run.Stages = pl.Variables, pl.Stages
		deps := Dependencies(pl.Jobs, pl.Stages)
		for idx, spec := range pl.Jobs {
			ruled := (spec.When.Branches != nil && !anyGlob(spec.When.Branches, branch)) ||
				(spec.When.Events != nil && !contains(spec.When.Events, event))
			j := &Job{
				Idx: idx, Name: spec.Name, Stage: spec.Stage, Image: spec.Image, Deps: deps[idx],
				Status: JobPending, Manual: spec.Manual, AllowFailure: spec.AllowFailure,
				Steps: []StepState{}, Spec: spec,
			}
			if ruled {
				j.Status, j.Reason = JobSkipped, ptr("rules")
			}
			run.Jobs = append(run.Jobs, j)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runs[run.ID] = run
	e.storage().Save(run)
	if run.Status == RunFailed {
		e.finished(run)
	} else {
		e.tick()
	}
	s := run.Summary()
	return &s, nil
}

func anyGlob(patterns []string, v string) bool {
	for _, p := range patterns {
		if GlobMatch(p, v) {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

/* ---------- run control ---------- */

func (e *Engine) run(id int64) (*Run, error) {
	r, ok := e.runs[id]
	if !ok {
		return nil, fail(404, "run %d not found", id)
	}
	return r, nil
}

// RunJSON returns the full run (job specs included) as JSON.
func (e *Engine) RunJSON(id int64) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := e.run(id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(r)
}

// JobInfo is what the log and artifact routes need about a job.
type JobInfo struct {
	Run          int64
	Project      string
	Idx          int
	Name         string
	Status       string
	HasArtifacts bool
}

// JobInfo returns one job of a run.
func (e *Engine) JobInfo(runID int64, idx int) (JobInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := e.run(runID)
	if err != nil {
		return JobInfo{}, err
	}
	if idx < 0 || idx >= len(r.Jobs) {
		return JobInfo{}, fail(404, "job not found")
	}
	j := r.Jobs[idx]
	return JobInfo{r.ID, r.Project, j.Idx, j.Name, j.Status, j.HasArtifacts}, nil
}

func (e *Engine) cancel(r *Run) {
	if !activeRun[r.Status] {
		return
	}
	r.Canceled = true
	now := time.Now().UnixMilli()
	for _, j := range r.Jobs {
		if j.Status == JobRunning {
			if stop := e.aborts[fmt.Sprintf("%d:%d", r.ID, j.Idx)]; stop != nil {
				stop()
			}
		} else if !terminal[j.Status] {
			j.Status, j.EndedAt, j.Reason = JobCanceled, ptr(now), ptr("canceled")
		}
	}
	e.persist(r, true)
	e.tick()
}

// Cancel stops a run: running jobs are killed, pending ones canceled.
func (e *Engine) Cancel(id int64) (RunSummary, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := e.run(id)
	if err != nil {
		return RunSummary{}, err
	}
	e.cancel(r)
	return r.Summary(), nil
}

// Rerun starts a new run for the same commit.
func (e *Engine) Rerun(ctx context.Context, id int64) (RunSummary, error) {
	e.mu.Lock()
	r, err := e.run(id)
	if err != nil {
		e.mu.Unlock()
		return RunSummary{}, err
	}
	project, branch, sha, sourceRef := r.Project, r.Branch, r.Sha, r.SourceRef
	_, perr := e.project(project)
	e.mu.Unlock()
	if perr != nil {
		return RunSummary{}, perr
	}
	run, err := e.createRun(ctx, project, branch, sha, "retry", sourceRef)
	if err != nil {
		return RunSummary{}, err
	}
	if run == nil {
		return RunSummary{}, fail(400, "the commit no longer has a pipeline")
	}
	return *run, nil
}

// RetryJob re-runs a finished job and everything downstream of it.
func (e *Engine) RetryJob(id int64, idx int) (RunSummary, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := e.run(id)
	if err != nil {
		return RunSummary{}, err
	}
	if idx < 0 || idx >= len(r.Jobs) {
		return RunSummary{}, fail(404, "job not found")
	}
	j := r.Jobs[idx]
	if !terminal[j.Status] || (j.Reason != nil && *j.Reason == "rules") {
		return RunSummary{}, fail(400, "only finished jobs can be retried")
	}
	reset := map[int]bool{idx: true}
	for changed := true; changed; {
		changed = false
		for _, o := range r.Jobs {
			if reset[o.Idx] {
				continue
			}
			for _, d := range o.Deps {
				if reset[d] {
					reset[o.Idx], changed = true, true
					break
				}
			}
		}
	}
	for i := range reset {
		o := r.Jobs[i]
		if o.Reason != nil && *o.Reason == "rules" {
			continue
		}
		o.Status, o.Reason, o.ExitCode, o.StartedAt, o.EndedAt, o.QueuedAt, o.ApprovedBy, o.HasArtifacts, o.Steps =
			JobPending, nil, nil, nil, nil, nil, nil, false, []StepState{}
		_ = os.Remove(JobArtifacts(r.ID, i))
	}
	r.Status, r.EndedAt, r.Canceled, r.Error = RunRunning, nil, false, nil
	e.persist(r, true)
	e.tick()
	return r.Summary(), nil
}

// Approve releases a manual job.
func (e *Engine) Approve(id int64, idx int) (RunSummary, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := e.run(id)
	if err != nil {
		return RunSummary{}, err
	}
	if idx < 0 || idx >= len(r.Jobs) || r.Jobs[idx].Status != JobManual {
		return RunSummary{}, fail(400, "this job is not waiting for approval")
	}
	j := r.Jobs[idx]
	j.Status, j.QueuedAt, j.ApprovedBy = JobQueued, ptr(time.Now().UnixMilli()), ptr("panel")
	e.persist(r, true)
	e.tick()
	return r.Summary(), nil
}

/* ---------- scheduler (all with mu held) ---------- */

// persist saves the run now, or debounced (500 ms) for frequent changes like step progress.
func (e *Engine) persist(r *Run, now bool) {
	if t := e.saveTimers[r.ID]; t != nil {
		t.Stop()
		delete(e.saveTimers, r.ID)
	}
	if e.runs[r.ID] != r {
		return // deleted (e.g. with its project) while a job was still finishing
	}
	if now {
		e.storage().Save(r)
		return
	}
	e.saveTimers[r.ID] = time.AfterFunc(500*time.Millisecond, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		delete(e.saveTimers, r.ID)
		if e.runs[r.ID] == r {
			e.storage().Save(r)
		}
	})
}

// tick advances every run and starts queued jobs, oldest first, while they fit in the outer VM.
func (e *Engine) tick() {
	ids := make([]int64, 0, len(e.runs))
	for id, r := range e.runs {
		if activeRun[r.Status] {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	running, cpus, mem := 0, 0, 0
	type queued struct {
		r *Run
		j *Job
	}
	var queue []queued
	for _, id := range ids {
		r := e.runs[id]
		if r == nil || !activeRun[r.Status] {
			continue
		}
		e.advance(r)
		for _, j := range r.Jobs {
			if j.Status == JobRunning {
				running++
				cpus += j.Spec.Vcpus
				mem += j.Spec.Mem
			}
			if j.Status == JobQueued && e.aborts[fmt.Sprintf("%d:%d", r.ID, j.Idx)] == nil {
				queue = append(queue, queued{r, j})
			}
		}
	}
	if running > 0 || len(queue) > 0 {
		e.lastActivity = time.Now()
	}
	cap := e.capacity()
	if !e.outerOK || cap == nil || jobs.Busy("outer") {
		return
	}
	limit := maxJobs()
	for _, q := range queue {
		if q.j.Spec.Mem > cap.memMiB || q.j.Spec.Vcpus > cap.cpus {
			q.j.Status, q.j.EndedAt = JobFailed, ptr(time.Now().UnixMilli())
			q.j.Reason = ptr(fmt.Sprintf("needs %d vCPU / %d MiB; the outer VM offers %d / %d", q.j.Spec.Vcpus, q.j.Spec.Mem, cap.cpus, cap.memMiB))
			e.persist(q.r, true)
			continue
		}
		// FIFO: a job that does not fit yet waits for capacity instead of being overtaken
		if (limit > 0 && running >= limit) || cpus+q.j.Spec.Vcpus > cap.cpus || mem+q.j.Spec.Mem > cap.memMiB {
			break
		}
		running++
		cpus += q.j.Spec.Vcpus
		mem += q.j.Spec.Mem
		e.start(q.r, q.j)
	}
}

// advance moves pending jobs whose dependencies finished and updates the run status.
func (e *Engine) advance(r *Run) {
	changed := false
	now := time.Now().UnixMilli()
	for again := true; again; {
		again = false
		for _, j := range r.Jobs {
			if j.Status != JobPending || r.Canceled {
				continue
			}
			ready, ok := true, true
			for _, d := range j.Deps {
				dj := r.Jobs[d]
				if !terminal[dj.Status] {
					ready = false
					break
				}
				reason := ""
				if dj.Reason != nil {
					reason = *dj.Reason
				}
				if !(dj.Status == JobSuccess || (dj.Status == JobFailed && dj.AllowFailure) || (dj.Status == JobSkipped && reason != "upstream failed")) {
					ok = false
				}
			}
			if !ready {
				continue
			}
			switch when := j.Spec.When.Status; {
			case when == "success" && !ok:
				j.Status, j.Reason, j.EndedAt = JobSkipped, ptr("upstream failed"), ptr(now)
			case when == "failure" && ok:
				j.Status, j.Reason, j.EndedAt = JobSkipped, ptr("nothing failed upstream"), ptr(now)
			case j.Manual && j.ApprovedBy == nil:
				j.Status = JobManual
			default:
				j.Status, j.QueuedAt = JobQueued, ptr(now)
			}
			again, changed = true, true
		}
	}
	status := r.Status
	all, hasRunning, hasQueued, hasManual := true, false, false, false
	for _, j := range r.Jobs {
		all = all && terminal[j.Status]
		hasRunning = hasRunning || j.Status == JobRunning
		hasQueued = hasQueued || j.Status == JobQueued
		hasManual = hasManual || j.Status == JobManual
	}
	switch {
	case all:
		failed := false
		for _, j := range r.Jobs {
			if (j.Status == JobFailed && !j.AllowFailure) || (j.Status == JobSkipped && j.Reason != nil && *j.Reason == "upstream failed") {
				failed = true
			}
		}
		switch {
		case r.Canceled:
			status = RunCanceled
		case failed:
			status = RunFailed
		default:
			status = RunSuccess
		}
	case hasRunning:
		status = RunRunning
	case hasQueued:
		status = RunQueued
		if r.StartedAt != nil {
			status = RunRunning
		}
	case hasManual:
		status = RunWaiting
	}
	if status != r.Status {
		r.Status = status
		changed = true
		if !activeRun[status] {
			e.finished(r)
			return
		}
	}
	if changed {
		e.persist(r, false)
	}
}

func (e *Engine) start(r *Run, j *Job) {
	key := fmt.Sprintf("%d:%d", r.ID, j.Idx)
	ctx, stop := context.WithCancel(context.Background())
	e.aborts[key] = stop
	now := time.Now().UnixMilli()
	j.Status, j.StartedAt, j.EndedAt, j.ExitCode, j.Reason = JobRunning, ptr(now), nil, nil, nil
	j.Attempt++
	if r.StartedAt == nil {
		r.StartedAt = ptr(now)
	}
	r.Status = RunRunning
	e.persist(r, true)
	go e.execute(ctx, r, j)
}

// execute runs a started job (without the lock) and records the result.
func (e *Engine) execute(ctx context.Context, r *Run, j *Job) {
	key := fmt.Sprintf("%d:%d", r.ID, j.Idx)
	code := 1
	var ws *workspace
	checkpoint, recoveryErr := readRecovery(r.ID, j.Idx)
	var err error
	if recoveryErr == nil {
		ws = &workspace{hashes: map[int][]string{j.Idx: checkpoint.Hashes}}
	} else if errors.Is(recoveryErr, os.ErrNotExist) {
		ws, err = e.execution().Prepare(ctx, r)
	} else {
		err = recoveryErr
	}
	if err == nil {
		e.mu.Lock()
		p, perr := e.project(r.Project)
		var jc jobContext
		if perr == nil {
			var secrets map[string]string
			if recoveryErr == nil {
				// A runner already owns these values; replay uses its original masking snapshot.
				secrets = map[string]string{}
			} else {
				secrets, perr = selectSecrets(*p, *r, j.Spec.Secrets, e.secrets)
			}
			var upstream []Job
			for _, d := range j.Deps {
				upstream = append(upstream, *r.Jobs[d])
			}
			vars := map[string]string{}
			for k, v := range r.Variables {
				vars[k] = v
			}
			jc = jobContext{
				run: r, job: j, project: *p, workspace: ws.file, cacheHashes: ws.hashes[j.Idx],
				upstream: upstream, variables: vars, secrets: secrets,
				mutate: func(f func()) { e.mu.Lock(); f(); e.persist(r, false); e.mu.Unlock() },
			}
		}
		e.mu.Unlock()
		if perr != nil {
			err = perr
		} else {
			code = e.execution().Run(ctx, jc)
		}
	}
	if err != nil {
		appendLog(JobLog(r.ID, j.Idx), "\n✗ "+err.Error()+"\n")
		e.mu.Lock()
		j.Reason = ptr(err.Error())
		e.mu.Unlock()
		code = 1
	}

	e.mu.Lock()
	vm := j.VM
	j.ExitCode, j.EndedAt = ptr(code), ptr(time.Now().UnixMilli())
	switch {
	case ctx.Err() != nil:
		j.Status, j.Reason = JobCanceled, ptr("canceled")
	case code == 0:
		j.Status = JobSuccess
	case j.Attempt <= j.Spec.Retry:
		appendLog(JobLog(r.ID, j.Idx), fmt.Sprintf("\n↻ retrying (%d/%d)\n\n", j.Attempt, j.Spec.Retry))
		j.Status, j.QueuedAt = JobQueued, ptr(time.Now().UnixMilli())
	default:
		j.Status = JobFailed
		if code == 124 {
			j.Reason = ptr("timed out")
		}
	}
	e.persist(r, true)
	snapshot, marshalErr := json.Marshal(r)
	var persisted <-chan error
	if marshalErr == nil && e.runs[r.ID] == r {
		persisted = e.storage().Track(r, snapshot)
	}
	e.mu.Unlock()
	// Do not destroy the VM until the terminal status is durable. A crash during artifact
	// collection can still replay the runner and retry collection from the same VM.
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 35*time.Second)
	persistErr := marshalErr
	if persistErr == nil && persisted != nil {
		select {
		case persistErr = <-persisted:
		case <-persistCtx.Done():
			persistErr = persistCtx.Err()
		}
	}
	persistCancel()
	if persistErr != nil {
		log.Printf("ci: keep recovery VM after persistence failure: %v", persistErr)
		e.mu.Lock()
		j.Status, j.Reason = JobFailed, ptr("could not persist job outcome; recovery VM retained: "+persistErr.Error())
		delete(e.aborts, key)
		e.persist(r, true)
		e.mu.Unlock()
		return
	}
	if vm != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err := e.execution().Remove(cleanup, *vm)
		cancel()
		if err != nil {
			log.Printf("ci: remove vm%d: %v", *vm, err)
		}
	}
	if err := os.Remove(recoveryPath(r.ID, j.Idx)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("ci: remove recovery: %v", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.aborts, key)
	j.VM = nil
	e.persist(r, true)
	e.tick()
}

func appendLog(file, text string) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		log.Printf("ci: job log: %v", err)
		return
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("ci: job log: %v", err)
		return
	}
	defer f.Close()
	limit, err := loglimit.Config()
	if err != nil {
		log.Printf("ci: %v", err)
		limit = loglimit.DefaultBytes
	}
	info, err := f.Stat()
	if err != nil {
		log.Printf("ci: stat job log: %v", err)
		return
	}
	capped := loglimit.Writer{Out: f, Limit: limit, Written: info.Size(), Truncated: info.Size() >= limit}
	if _, err := capped.Write([]byte(text)); err != nil {
		log.Printf("ci: write job log: %v", err)
	}
}

// workspace packs the run's checkout once and shares it between its jobs.
func (e *Engine) workspace(ctx context.Context, r *Run) (*workspace, error) {
	e.mu.Lock()
	file := filepath.Join(runDir(r.ID), "workspace.tgz")
	ws := e.workspaces[r.ID]
	stale := false
	if ws != nil {
		select {
		case <-ws.done:
			_, statErr := os.Stat(file)
			stale = ws.err != nil || statErr != nil
		default:
		}
	}
	if ws == nil || stale {
		p, err := e.project(r.Project)
		if err != nil {
			e.mu.Unlock()
			return nil, err
		}
		depth := 1
		zero := false
		type entry struct{ idx, k int }
		var lists []entry
		var files [][]string
		for _, j := range r.Jobs {
			if j.Spec.Depth == 0 {
				zero = true
			}
			depth = max(depth, j.Spec.Depth)
			for k, c := range j.Spec.Cache {
				lists = append(lists, entry{j.Idx, k})
				files = append(files, c.Files)
			}
		}
		if zero {
			depth = 0
		}
		ws = &workspace{done: make(chan struct{}), file: file, hashes: map[int][]string{}}
		e.workspaces[r.ID] = ws
		projectID, url, sha, branch := p.ID, p.URL, r.Sha, r.Branch
		go func() {
			defer close(ws.done)
			_ = os.MkdirAll(filepath.Dir(file), 0o700)
			hashes, err := packWorkspace(context.Background(), projectID, url, sha, branch, depth, file, files)
			if err != nil {
				ws.err = err
				return
			}
			for i, l := range lists {
				h := ws.hashes[l.idx]
				for len(h) <= l.k {
					h = append(h, "")
				}
				h[l.k] = hashes[i]
				ws.hashes[l.idx] = h
			}
		}()
	}
	e.mu.Unlock()
	select {
	case <-ws.done:
		return ws, ws.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *Engine) finished(r *Run) {
	if r.EndedAt == nil {
		r.EndedAt = ptr(time.Now().UnixMilli())
	}
	e.persist(r, true)
	delete(e.workspaces, r.ID)
	_ = os.Remove(filepath.Join(runDir(r.ID), "workspace.tgz"))
	e.notify(r)
	// retention: keep the newest CI_KEEP_RUNS runs per project
	keep := int(math.Max(number(env.Get("CI_KEEP_RUNS", "50")), 5))
	var old []*Run
	for _, x := range e.runs {
		if x.Project == r.Project && !activeRun[x.Status] {
			old = append(old, x)
		}
	}
	sort.Slice(old, func(a, b int) bool { return old[a].ID > old[b].ID })
	if len(old) > keep {
		for _, o := range old[keep:] {
			e.storage().Delete(o.ID)
			delete(e.runs, o.ID)
		}
	}
}

func (e *Engine) notify(r *Run) {
	p, err := e.project(r.Project)
	if err != nil || p.NotifyURL == "" || p.NotifyOn == "never" || (p.NotifyOn == "failure" && r.Status != RunFailed) {
		return
	}
	icon := "⏹"
	switch r.Status {
	case RunSuccess:
		icon = "✅"
	case RunFailed:
		icon = "❌"
	}
	text := fmt.Sprintf("%s %s #%d %s — %s @ %s: %s", icon, p.Name, r.ID, r.Status, r.Branch, short(r.Sha, 8), r.Message)
	body, _ := json.Marshal(map[string]any{"text": text, "content": text, "run": r.Summary()})
	url := p.NotifyURL
	go func() {
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err == nil {
			resp.Body.Close()
		}
	}()
}

/* ---------- housekeeping ---------- */

// housekeeping (every 10 s): outer VM health and lifecycle, leftover VMs, polling and schedules.
func (e *Engine) housekeeping() {
	now := time.Now()
	up := lab.OuterRunning()
	ok := up && !jobs.Busy("outer") && sshx.Remote(8*time.Second, "true").Code == 0

	e.mu.Lock()
	wasOK := e.outerOK
	e.outerOK = ok
	switch {
	case ok:
		e.outer = "online"
	case up || jobs.Busy("outer"):
		e.outer = "starting"
	default:
		e.outer = "off"
	}
	if ok && !wasOK {
		e.lastActivity = now
	}
	e.mu.Unlock()

	if ok {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		var h agent.HostInfo
		err := sshx.AgentJSON(ctx, "GET", "/host-info", nil, &h)
		cancel()
		if err == nil && h.CPUs > 0 {
			e.mu.Lock()
			e.host = &hostCapacity{h.CPUs, int(h.MemTotalKB / 1024)}
			e.mu.Unlock()
		}
	}

	// the outer VM follows the queue: started when a job waits, stopped after CI_IDLE_SHUTDOWN_MIN idle
	e.mu.Lock()
	waiting, active := false, false
	for _, r := range e.runs {
		if !activeRun[r.Status] {
			continue
		}
		for _, j := range r.Jobs {
			waiting = waiting || j.Status == JobQueued
			active = active || j.Status == JobQueued || j.Status == JobRunning
		}
	}
	idle := idleShutdownMin()
	stopIdle := false
	switch {
	case waiting && e.outer == "off" && !jobs.Busy("outer"):
		jobs.Start("Start outer VM (a CI job is waiting)", "outer", "host/up.sh", "bash", "host/up.sh")
		e.outer = "starting"
	case e.outerOK && !active && idle > 0 && now.Sub(e.lastActivity) > time.Duration(idle)*time.Minute && !jobs.Busy("outer"):
		stopIdle = true
		e.outerOK, e.outer = false, "starting"
	}
	reap := e.outerOK && !e.reaped
	busyVMs := map[int]bool{}
	if reap {
		e.reaped = true
		for _, r := range e.runs {
			for _, j := range r.Jobs {
				if j.VM != nil && j.Status == JobRunning {
					busyVMs[*j.VM] = true
				}
			}
		}
	}
	var poll []string
	for _, p := range e.projects {
		last := int64(0)
		if p.LastSyncAt != nil {
			last = *p.LastSyncAt
		}
		if p.PollS > 0 && now.UnixMilli()-last >= int64(p.PollS)*1000 {
			poll = append(poll, p.ID)
		}
	}
	minute := now.Unix() / 60
	var scheduled []string
	if minute != e.lastMinute {
		e.lastMinute = minute
		for _, p := range e.projects {
			scheduled = append(scheduled, p.ID)
		}
	}
	e.mu.Unlock()

	if stopIdle {
		sshx.CloseMaster()
		jobs.Start(fmt.Sprintf("Stop outer VM (idle for %d min)", idle), "outer", "host/down.sh", "bash", "host/down.sh")
	}
	if reap {
		if err := ReapVMs(busyVMs); err != nil {
			log.Printf("ci: reap VMs: %v", err)
			e.mu.Lock()
			e.reaped = false
			e.mu.Unlock()
		}
	}
	for _, id := range poll {
		go func() { _ = e.Sync(context.Background(), id) }()
	}
	for _, id := range scheduled {
		go e.schedules(id, minute)
	}
	e.mu.Lock()
	e.tick()
	e.mu.Unlock()
}

func (e *Engine) schedules(projectID string, minute int64) {
	ctx := context.Background()
	e.mu.Lock()
	p, err := e.project(projectID)
	if err != nil {
		e.mu.Unlock()
		return
	}
	branch := p.DefaultBranch
	sha := p.Refs[branch]
	pl, cached := e.pipelines[sha]
	e.mu.Unlock()
	if sha == "" {
		return
	}
	if !cached {
		pl, _, _ = loadPipeline(ctx, projectID, sha)
		e.mu.Lock()
		e.pipelines[sha] = pl
		e.mu.Unlock()
	}
	if pl == nil {
		return
	}
	at := time.Unix(minute*60, 0)
	for _, expr := range pl.On.Schedule {
		e.mu.Lock()
		p, err := e.project(projectID)
		if err != nil || p.ScheduleFired[expr] == minute || !CronMatches(expr, at) {
			e.mu.Unlock()
			continue
		}
		p.ScheduleFired[expr] = minute
		saveProject(p)
		e.mu.Unlock()
		if _, err := e.createRun(ctx, projectID, branch, sha, "schedule", "refs/heads/"+branch); err != nil {
			log.Printf("ci: schedule %s %q: %v", projectID, expr, err)
		}
	}
}

/* ---------- queries ---------- */

func (e *Engine) listRuns(project string, limit int) []RunSummary {
	list := make([]*Run, 0, len(e.runs))
	for _, r := range e.runs {
		if project == "" || r.Project == project {
			list = append(list, r)
		}
	}
	sort.Slice(list, func(a, b int) bool { return list[a].ID > list[b].ID })
	out := []RunSummary{}
	for i, r := range list {
		if i >= limit {
			break
		}
		out = append(out, r.Summary())
	}
	return out
}

// ListRuns lists run summaries, newest first.
func (e *Engine) ListRuns(project string, limit int) []RunSummary {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.listRuns(project, limit)
}

// Capacity is the stats' view of the outer VM capacity.
type Capacity struct {
	Cpus       int `json:"cpus"`
	MemMiB     int `json:"memMiB"`
	UsedVcpus  int `json:"usedVcpus"`
	UsedMemMiB int `json:"usedMemMiB"`
}

// Stats is the queue and outer VM state.
type Stats struct {
	MaxJobs         int       `json:"maxJobs"`
	Running         int       `json:"running"`
	Queued          int       `json:"queued"`
	Outer           string    `json:"outer"`
	OuterOK         bool      `json:"outerOk"`
	Capacity        *Capacity `json:"capacity"`
	IdleShutdownMin int       `json:"idleShutdownMin"`
	IdleShutdownAt  *int64    `json:"idleShutdownAt"`
}

func (e *Engine) stats() Stats {
	s := Stats{MaxJobs: maxJobs(), Outer: e.outer, OuterOK: e.outerOK, IdleShutdownMin: idleShutdownMin()}
	usedV, usedM := 0, 0
	for _, r := range e.runs {
		for _, j := range r.Jobs {
			switch j.Status {
			case JobRunning:
				s.Running++
				usedV += j.Spec.Vcpus
				usedM += j.Spec.Mem
			case JobQueued:
				s.Queued++
			}
		}
	}
	if c := e.capacity(); c != nil {
		s.Capacity = &Capacity{c.cpus, c.memMiB, usedV, usedM}
	}
	if e.outerOK && s.Running == 0 && s.Queued == 0 && s.IdleShutdownMin > 0 {
		s.IdleShutdownAt = ptr(e.lastActivity.Add(time.Duration(s.IdleShutdownMin) * time.Minute).UnixMilli())
	}
	return s
}

// Overview is GET /api/ci: projects, recent runs, queue and the built-in images/templates.
func (e *Engine) Overview() map[string]any {
	e.mu.Lock()
	views := make([]ProjectView, 0, len(e.projects))
	for _, p := range e.projects {
		views = append(views, e.view(p))
	}
	out := map[string]any{
		"projects": views, "runs": e.listRuns("", 30), "stats": e.stats(),
		"globalSecrets": e.secretNames("_global"),
	}
	e.mu.Unlock()
	out["templates"] = BuiltinTemplates()
	images := []map[string]any{}
	for _, d := range BuiltinImages() {
		images = append(images, map[string]any{"name": d.Name, "description": d.Description, "from": d.From})
	}
	out["images"] = images
	return out
}

// IsError reports whether err is an engine error (with its HTTP status).
func IsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}
