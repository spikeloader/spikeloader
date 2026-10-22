package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"spikeloader/internal/env"
)

type memoryRuns struct {
	mu      sync.Mutex
	deleted []int64
}

func (*memoryRuns) Save(*Run) {}
func (*memoryRuns) Track(*Run, []byte) <-chan error {
	done := make(chan error, 1)
	done <- nil
	return done
}
func (m *memoryRuns) Delete(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, id)
}

type fakeExecutor struct {
	started chan string
	release chan int
}

func (*fakeExecutor) Prepare(context.Context, *Run) (*workspace, error) {
	return &workspace{hashes: map[int][]string{}}, nil
}
func (f *fakeExecutor) Run(ctx context.Context, jc jobContext) int {
	f.started <- fmt.Sprintf("%d:%d", jc.run.ID, jc.job.Idx)
	select {
	case rc := <-f.release:
		return rc
	case <-ctx.Done():
		return 130
	}
}
func (*fakeExecutor) Remove(context.Context, int) error { return nil }

func testEngine(t *testing.T) *Engine {
	t.Helper()
	oldRuns := runsDir
	runsDir = t.TempDir()
	t.Cleanup(func() { runsDir = oldRuns })
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("CI_KEEP_RUNS=5\nCI_MAX_JOBS=0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// env.Root is process-local; tests deliberately do not run in parallel.
	oldRoot := env.Root
	env.Root = root
	t.Cleanup(func() { env.Root = oldRoot })
	e := &Engine{projects: []*Project{{ID: "demo", DefaultBranch: "main", NotifyOn: "never"}}, runs: map[int64]*Run{}, secrets: Secrets{}, aborts: map[string]context.CancelFunc{}, saveTimers: map[int64]*time.Timer{}, workspaces: map[int64]*workspace{}, runStore: &memoryRuns{}, executor: &fakeExecutor{started: make(chan string, 20), release: make(chan int, 20)}}
	t.Cleanup(func() {
		e.mu.Lock()
		e.outerOK = false
		for _, timer := range e.saveTimers {
			timer.Stop()
		}
		for _, cancel := range e.aborts {
			cancel()
		}
		e.mu.Unlock()
		waitEngine(t, e, func() bool { return len(e.aborts) == 0 })
		e.mu.Lock()
		for _, timer := range e.saveTimers {
			timer.Stop()
		}
		e.mu.Unlock()
	})
	return e
}
func waitEngine(t *testing.T, e *Engine, predicate func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		e.mu.Lock()
		ok := predicate()
		e.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("engine did not settle")
}
func testRun(id int64, specs ...JobSpec) *Run {
	r := &Run{ID: id, Project: "demo", Branch: "main", SourceRef: "refs/heads/main", Status: RunQueued, Stages: []string{"test"}, Jobs: []*Job{}, Variables: map[string]string{}}
	for i, spec := range specs {
		if spec.Vcpus == 0 {
			spec.Vcpus = 1
		}
		if spec.Mem == 0 {
			spec.Mem = 128
		}
		if spec.When.Status == "" {
			spec.When.Status = "success"
		}
		r.Jobs = append(r.Jobs, &Job{Idx: i, Name: fmt.Sprintf("job-%d", i), Status: JobPending, Spec: spec, Deps: []int{}, Steps: []StepState{}, Manual: spec.Manual, AllowFailure: spec.AllowFailure})
	}
	return r
}
func TestEngineDAGRulesAndApproval(t *testing.T) {
	e := testEngine(t)
	r := testRun(1, JobSpec{}, JobSpec{Manual: true}, JobSpec{When: WhenSpec{Status: "failure"}}, JobSpec{When: WhenSpec{Status: "always"}})
	for i := 1; i < 4; i++ {
		r.Jobs[i].Deps = []int{0}
	}
	e.runs[1] = r
	e.tick()
	if r.Jobs[0].Status != JobQueued || r.Jobs[1].Status != JobPending {
		t.Fatal("dependencies not respected")
	}
	r.Jobs[0].Status = JobSuccess
	e.tick()
	if r.Jobs[1].Status != JobManual || r.Jobs[2].Status != JobSkipped || r.Jobs[3].Status != JobQueued {
		t.Fatalf("%+v", r.Jobs)
	}
	if _, err := e.Approve(1, 1); err != nil {
		t.Fatal(err)
	}
	if r.Jobs[1].Status != JobQueued || r.Jobs[1].ApprovedBy == nil {
		t.Fatal("manual approval did not queue")
	}
	if _, err := e.Approve(1, 1); err == nil {
		t.Fatal("approved twice")
	}
	r.Jobs[0].Status = JobFailed
	r.Jobs[1].Status = JobPending
	r.Jobs[2].Status = JobPending
	r.Jobs[3].Status = JobPending
	e.tick()
	if r.Jobs[1].Status != JobSkipped || r.Jobs[2].Status != JobQueued || r.Jobs[3].Status != JobQueued {
		t.Fatal("failure/always rules")
	}
}
func TestEngineCapacityFIFOAndCancellation(t *testing.T) {
	e := testEngine(t)
	e.outerOK = true
	e.host = &hostCapacity{cpus: 1, memMiB: 1536}
	for id := int64(1); id <= 3; id++ {
		e.runs[id] = testRun(id, JobSpec{Vcpus: 2, Mem: 512})
	}
	e.mu.Lock()
	e.tick()
	e.mu.Unlock()
	f := e.executor.(*fakeExecutor)
	select {
	case got := <-f.started:
		if got != "1:0" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("job not started")
	}
	e.mu.Lock()
	if e.runs[2].Jobs[0].Status != JobQueued || e.runs[3].Jobs[0].Status != JobQueued {
		t.Fatal("capacity exceeded")
	}
	e.mu.Unlock()
	if _, err := e.Cancel(1); err != nil {
		t.Fatal(err)
	}
	waitEngine(t, e, func() bool { return e.runs[1].Status == RunCanceled && e.runs[2].Jobs[0].Status == JobRunning })
	select {
	case got := <-f.started:
		if got != "2:0" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO next job not started")
	}
	for _, id := range []int64{2, 3} {
		if _, err := e.Cancel(id); err != nil {
			t.Fatal(err)
		}
	}
	waitEngine(t, e, func() bool { return len(e.aborts) == 0 })
}
func TestEngineRetryAndRetention(t *testing.T) {
	e := testEngine(t)
	r := testRun(10, JobSpec{Retry: 1})
	e.runs[r.ID] = r
	e.outerOK = true
	e.host = &hostCapacity{cpus: 1, memMiB: 2048}
	f := e.executor.(*fakeExecutor)
	e.mu.Lock()
	e.tick()
	e.mu.Unlock()
	<-f.started
	f.release <- 1
	<-f.started
	e.mu.Lock()
	if r.Jobs[0].Attempt != 2 {
		t.Fatal(r.Jobs[0].Attempt)
	}
	e.mu.Unlock()
	f.release <- 0
	waitEngine(t, e, func() bool { return r.Status == RunSuccess && len(e.aborts) == 0 })
	e.mu.Lock()
	e.outerOK = false
	e.mu.Unlock()
	if _, err := e.RetryJob(10, 0); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	if r.Jobs[0].Status != JobQueued || r.Jobs[0].Attempt != 2 {
		t.Fatal("manual retry state")
	}
	for id := int64(1); id <= 9; id++ {
		old := testRun(id)
		old.Status = RunSuccess
		e.runs[id] = old
	}
	r.Status = RunSuccess
	e.finished(r)
	if len(e.runs) != 5 {
		t.Fatalf("retention kept %d", len(e.runs))
	}
	e.mu.Unlock()
}
func TestEngineSecretDenial(t *testing.T) {
	e := testEngine(t)
	r := testRun(1, JobSpec{Secrets: []string{"TOKEN"}})
	r.Branch = "feature"
	r.SourceRef = "refs/heads/feature"
	e.secrets = Secrets{"demo": {"TOKEN": {Value: "never-injected", Protected: true}}}
	e.runs[1] = r
	e.outerOK = true
	e.host = &hostCapacity{cpus: 1, memMiB: 2048}
	e.mu.Lock()
	e.tick()
	e.mu.Unlock()
	waitEngine(t, e, func() bool { return r.Status == RunFailed && len(e.aborts) == 0 })
	e.mu.Lock()
	defer e.mu.Unlock()
	if r.Jobs[0].Reason == nil || *r.Jobs[0].Reason != "secret TOKEN is only available on protected branches" {
		b, _ := json.Marshal(r)
		t.Fatal(string(b))
	}

	select {
	case <-e.executor.(*fakeExecutor).started:
		t.Fatal("denied job reached VM executor")
	default:
	}
}

func TestEngineDownstreamRetryAndRules(t *testing.T) {
	e := testEngine(t)
	r := testRun(1, JobSpec{}, JobSpec{}, JobSpec{})
	r.Status = RunFailed
	r.Jobs[0].Status = JobFailed
	r.Jobs[1].Status = JobSkipped
	r.Jobs[1].Reason = ptr("upstream failed")
	r.Jobs[1].Deps = []int{0}
	r.Jobs[2].Status = JobSkipped
	r.Jobs[2].Reason = ptr("rules")
	r.Jobs[2].Deps = []int{1}
	e.runs[1] = r
	if _, err := e.RetryJob(1, 0); err != nil {
		t.Fatal(err)
	}
	if r.Jobs[0].Status != JobQueued || r.Jobs[1].Status != JobPending || r.Jobs[2].Status != JobSkipped {
		t.Fatal("retry did not reset downstream while preserving rules")
	}
	if _, err := e.RetryJob(1, 2); err == nil {
		t.Fatal("retried a rules-skipped job")
	}
	if _, err := e.Cancel(1); err != nil {
		t.Fatal(err)
	}
	for _, j := range r.Jobs {
		if !terminal[j.Status] {
			t.Fatal("queued/pending job not canceled")
		}
	}
}

func TestEngineAllowFailureAndOversized(t *testing.T) {
	e := testEngine(t)
	r := testRun(1, JobSpec{AllowFailure: true}, JobSpec{})
	r.Jobs[0].Status = JobFailed
	r.Jobs[1].Deps = []int{0}
	e.runs[1] = r
	e.tick()
	if r.Jobs[1].Status != JobQueued {
		t.Fatal("allowed failure blocked downstream")
	}
	r.Jobs[1].Status = JobSuccess
	e.tick()
	if r.Status != RunSuccess {
		t.Fatal(r.Status)
	}
	big := testRun(2, JobSpec{Vcpus: 33, Mem: 128})
	e.runs[2] = big
	e.outerOK = true
	e.host = &hostCapacity{cpus: 1, memMiB: 2048}
	e.tick()
	if big.Jobs[0].Status != JobFailed || big.Jobs[0].Reason == nil {
		t.Fatal("oversized job did not fail explicitly")
	}
}

func TestWebhookToken(t *testing.T) {
	e := testEngine(t)
	e.projects[0].WebhookToken = "correct"
	done := make(chan struct{})
	close(done)
	e.syncing = map[string]chan struct{}{"demo": done}
	for _, token := range []string{"", "wrong", "correct-extra"} {
		err := e.CheckWebhook("demo", token)
		if ce, ok := IsError(err); !ok || ce.Status != 401 {
			t.Fatalf("%q: %v", token, err)
		}
	}
	if err := e.CheckWebhook("demo", "correct"); err != nil {
		t.Fatal(err)
	}
}
