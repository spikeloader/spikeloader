package ci

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spikeloader/internal/agent"
	"spikeloader/internal/sshx"
)

type recoveryTransport struct{ base string }

func (t recoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	u, err := url.Parse(t.base + r.URL.RequestURI())
	if err != nil {
		return nil, err
	}
	copy.URL = u
	return http.DefaultTransport.RoundTrip(copy)
}

func TestJobRecoveryReplaysWithoutDuplicatesAndKeepsMaskSnapshot(t *testing.T) {
	previousRuns, previousKey := runsDir, secretsKeyFile
	runsDir = t.TempDir()
	secretsKeyFile = filepath.Join(t.TempDir(), "secrets.key")
	keyOnce = sync.Once{}
	defer func() {
		runsDir = previousRuns
		secretsKeyFile = previousKey
		keyOnce = sync.Once{}
		keyVal = nil
		keyErr = nil
	}()
	if err := os.MkdirAll(runDir(42), 0700); err != nil {
		t.Fatal(err)
	}
	prefix := "initial header\n"
	if err := os.WriteFile(JobLog(42, 0), []byte(prefix+"stale partial duplicated output\n"), 0600); err != nil {
		t.Fatal(err)
	}
	encrypted, err := seal(`{"TOKEN":"original-secret-value"}`)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := jobRecovery{VM: 1, Task: "run-42-job-0-attempt-1", Prefix: int64(len(prefix)), StartedAt: time.Now().UnixMilli(), Secrets: encrypted}
	if err := writeRecovery(42, 0, checkpoint); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(recoveryPath(42, 0))
	if err != nil || strings.Contains(string(raw), "original-secret-value") {
		t.Fatalf("checkpoint leaked secret: %v", err)
	}
	payload := []byte("##[step] 0\noriginal-secret-value\n##[end] 0 0\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/jobs/"+checkpoint.Task:
			json.NewEncoder(w).Encode(agent.JobState{ID: checkpoint.Task, VM: 1})
		case r.Method == "GET" && r.URL.Path == "/jobs/"+checkpoint.Task+"/log":
			zero := 0
			json.NewEncoder(w).Encode(agent.Chunk{Data: payload, Offset: int64(len(payload))})
			json.NewEncoder(w).Encode(agent.Chunk{Offset: int64(len(payload)), Code: &zero})
		default:
			t.Errorf("recovery unexpectedly prepared/restarted a VM: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer server.Close()
	previousClient := sshx.AgentClient
	sshx.AgentClient = &http.Client{Transport: recoveryTransport{server.URL}}
	defer func() { sshx.AgentClient = previousClient }()
	run := &Run{ID: 42}
	job := &Job{Idx: 0, Attempt: 1, Spec: JobSpec{TimeoutS: 60, Steps: []StepSpec{{Name: "test", Run: "echo test"}}}}
	code := runJob(context.Background(), jobContext{run: run, job: job, project: Project{ID: "demo"}, secrets: map[string]string{"TOKEN": "changed-secret"}, mutate: func(f func()) { f() }})
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	log, err := os.ReadFile(JobLog(42, 0))
	if err != nil {
		t.Fatal(err)
	}
	text := string(log)
	if strings.Contains(text, "original-secret-value") || strings.Contains(text, "stale partial") || strings.Count(text, "***") != 1 || !strings.HasPrefix(text, prefix) {
		t.Fatalf("invalid replayed log: %s", text)
	}
	if len(job.Steps) != 1 || job.Steps[0].Status != "success" {
		t.Fatalf("steps not reconstructed: %+v", job.Steps)
	}
}
