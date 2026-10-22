// Package ci is the CI/CD engine: pipeline parsing, git mirrors, the run queue and scheduler, and
// the executor that runs each job in a throwaway Firecracker microVM.
//
// The JSON shape of these types is the panel's API contract (web/lib/ci/types.ts mirrors it) and the
// format stored in PostgreSQL: keep field names, and which fields may be null, in sync with it.
// Nullable fields are pointers or nil slices/maps; fields that are always lists must never be nil.
package ci

// StepSpec is one command of a job.
type StepSpec struct {
	Name string `json:"name"`
	Run  string `json:"run"`
}

// CacheSpec is one cache entry of a job.
type CacheSpec struct {
	Key   string   `json:"key"`
	Paths []string `json:"paths"`
	Files []string `json:"files"`
}

// ArtifactSpec lists the files a job keeps.
type ArtifactSpec struct {
	Paths []string `json:"paths"`
	When  string   `json:"when"` // success | failure | always
}

// WhenSpec holds a job's rules.
type WhenSpec struct {
	Branches []string `json:"branches"` // nil = any
	Events   []string `json:"events"`   // nil = any
	Status   string   `json:"status"`   // success | failure | always
}

// JobSpec is a job as parsed from the pipeline (one per matrix combination).
type JobSpec struct {
	Base         string            `json:"base"` // name in the YAML (matrix jobs share it)
	Name         string            `json:"name"` // base plus the matrix values
	Stage        string            `json:"stage"`
	Image        string            `json:"image"`
	Vcpus        int               `json:"vcpus"`
	Mem          int               `json:"mem"`
	Disk         string            `json:"disk"`
	TimeoutS     int               `json:"timeoutS"`
	Needs        []string          `json:"needs"` // nil = every job of earlier stages
	Matrix       map[string]string `json:"matrix"`
	Variables    map[string]string `json:"variables"`
	Secrets      []string          `json:"secrets"`
	Before       []StepSpec        `json:"before"`
	Steps        []StepSpec        `json:"steps"`
	After        []StepSpec        `json:"after"`
	Artifacts    *ArtifactSpec     `json:"artifacts"`
	Cache        []CacheSpec       `json:"cache"`
	When         WhenSpec          `json:"when"`
	Manual       bool              `json:"manual"`
	AllowFailure bool              `json:"allowFailure"`
	Retry        int               `json:"retry"`
	Depth        int               `json:"depth"`
}

// On holds the pipeline triggers.
type On struct {
	Push     []string `json:"push"` // branch globs; nil = every branch
	Schedule []string `json:"schedule"`
	Manual   bool     `json:"manual"`
	Enabled  struct {
		Push bool `json:"push"`
	} `json:"enabled"`
}

// Pipeline is a parsed .spikeloader.yml.
type Pipeline struct {
	On        On                `json:"on"`
	Variables map[string]string `json:"variables"`
	Stages    []string          `json:"stages"`
	Jobs      []JobSpec         `json:"jobs"`
}

// Run, job and step states.
const (
	JobPending  = "pending"
	JobQueued   = "queued"
	JobManual   = "manual"
	JobRunning  = "running"
	JobSuccess  = "success"
	JobFailed   = "failed"
	JobCanceled = "canceled"
	JobSkipped  = "skipped"

	RunQueued   = "queued"
	RunRunning  = "running"
	RunWaiting  = "waiting"
	RunSuccess  = "success"
	RunFailed   = "failed"
	RunCanceled = "canceled"
)

// StepState is the live state of one step.
type StepState struct {
	Name      string `json:"name"`
	Phase     string `json:"phase"` // before | main | after
	Status    string `json:"status"`
	StartedAt *int64 `json:"startedAt"`
	EndedAt   *int64 `json:"endedAt"`
	ExitCode  *int   `json:"exitCode"`
}

// Job is one job of a run.
type Job struct {
	Idx          int         `json:"idx"`
	Name         string      `json:"name"`
	Stage        string      `json:"stage"`
	Image        string      `json:"image"`
	Deps         []int       `json:"deps"` // indexes this job waits for
	Status       string      `json:"status"`
	Reason       *string     `json:"reason"` // why it was skipped/failed without running
	Manual       bool        `json:"manual"`
	AllowFailure bool        `json:"allowFailure"`
	Attempt      int         `json:"attempt"`
	ExitCode     *int        `json:"exitCode"`
	VM           *int        `json:"vm"`
	QueuedAt     *int64      `json:"queuedAt"`
	StartedAt    *int64      `json:"startedAt"`
	EndedAt      *int64      `json:"endedAt"`
	ApprovedBy   *string     `json:"approvedBy"`
	HasArtifacts bool        `json:"hasArtifacts"`
	Steps        []StepState `json:"steps"`
	Spec         JobSpec     `json:"spec"`
}

// Run is one pipeline execution.
type Run struct {
	ID        int64             `json:"id"`
	Project   string            `json:"project"`
	Branch    string            `json:"branch"`
	Sha       string            `json:"sha"`
	Message   string            `json:"message"`
	Author    string            `json:"author"`
	Event     string            `json:"event"`     // push | manual | schedule | webhook | retry
	SourceRef string            `json:"sourceRef"` // canonical branch ref; empty for detached/legacy runs
	Status    string            `json:"status"`
	Error     *string           `json:"error"`
	CreatedAt int64             `json:"createdAt"`
	StartedAt *int64            `json:"startedAt"`
	EndedAt   *int64            `json:"endedAt"`
	Canceled  bool              `json:"canceled"`
	Variables map[string]string `json:"variables"` // pipeline-level variables
	Stages    []string          `json:"stages"`
	Jobs      []*Job            `json:"jobs"`
}

// Project is a repository the engine builds.
type Project struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	URL               string            `json:"url"`
	DefaultBranch     string            `json:"defaultBranch"`
	ProtectedBranches []string          `json:"protectedBranches"`
	PollS             int               `json:"pollS"` // seconds between polls; 0 = webhooks/manual only
	WebhookToken      string            `json:"webhookToken"`
	NotifyURL         string            `json:"notifyUrl"`
	NotifyOn          string            `json:"notifyOn"` // failure | always | never
	CreatedAt         int64             `json:"createdAt"`
	Refs              map[string]string `json:"refs"` // last seen branch heads
	LastSyncAt        *int64            `json:"lastSyncAt"`
	LastError         *string           `json:"lastError"`
	ScheduleFired     map[string]int64  `json:"scheduleFired"` // last minute each cron fired
}

// JobSummary is a job as listed in run summaries.
type JobSummary struct {
	Idx    int    `json:"idx"`
	Name   string `json:"name"`
	Stage  string `json:"stage"`
	Status string `json:"status"`
}

// RunSummary is a run without job specs and variables.
type RunSummary struct {
	ID        int64        `json:"id"`
	Project   string       `json:"project"`
	Branch    string       `json:"branch"`
	Sha       string       `json:"sha"`
	Message   string       `json:"message"`
	Author    string       `json:"author"`
	Event     string       `json:"event"`
	SourceRef string       `json:"sourceRef"`
	Status    string       `json:"status"`
	Error     *string      `json:"error"`
	CreatedAt int64        `json:"createdAt"`
	StartedAt *int64       `json:"startedAt"`
	EndedAt   *int64       `json:"endedAt"`
	Canceled  bool         `json:"canceled"`
	Stages    []string     `json:"stages"`
	Jobs      []JobSummary `json:"jobs"`
}

// ProjectView is a project as sent to the browser: no credentials in the URL, no webhook token.
type ProjectView struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	URL               string            `json:"url"`
	DefaultBranch     string            `json:"defaultBranch"`
	ProtectedBranches []string          `json:"protectedBranches"`
	PollS             int               `json:"pollS"`
	NotifyURL         string            `json:"notifyUrl"`
	NotifyOn          string            `json:"notifyOn"`
	CreatedAt         int64             `json:"createdAt"`
	Refs              map[string]string `json:"refs"`
	LastSyncAt        *int64            `json:"lastSyncAt"`
	LastError         *string           `json:"lastError"`
	ScheduleFired     map[string]int64  `json:"scheduleFired"`
	LastRun           *RunSummary       `json:"lastRun"`
}

// Image is a built CI image on the outer VM.
type Image struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	UsedBytes int64  `json:"used_bytes"`
	BuiltAt   int64  `json:"built_at"`
}

// Summary drops job specs and variables.
func (r *Run) Summary() RunSummary {
	jobs := make([]JobSummary, len(r.Jobs))
	for i, j := range r.Jobs {
		jobs[i] = JobSummary{j.Idx, j.Name, j.Stage, j.Status}
	}
	return RunSummary{
		ID: r.ID, Project: r.Project, Branch: r.Branch, Sha: r.Sha, Message: r.Message, Author: r.Author,
		Event: r.Event, Status: r.Status, Error: r.Error, CreatedAt: r.CreatedAt, StartedAt: r.StartedAt,
		SourceRef: r.SourceRef,
		EndedAt:   r.EndedAt, Canceled: r.Canceled, Stages: nonNil(r.Stages), Jobs: jobs,
	}
}

func ptr[T any](v T) *T { return &v }

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
