package api

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"spikeloader/internal/agent"
	"spikeloader/internal/ci"
	"spikeloader/internal/jobs"
	"spikeloader/internal/k8s"
	"spikeloader/internal/lab"
)

var update = flag.Bool("update", false, "regenerate API golden JSON fixtures")

func golden(t *testing.T, name string, value any) {
	t.Helper()
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, want) {
		t.Fatalf("%s contract changed; inspect diff and regenerate intentionally with go test ./internal/api -run TestContract -update\nactual:\n%s", name, b)
	}
}

func TestContractTypes(t *testing.T) {
	pipeline, errs := ci.ParsePipeline("jobs:\n  hello:\n    steps: [echo hello]\n", nil)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	run := ci.Run{ID: 1, Project: "demo", Branch: "main", SourceRef: "refs/heads/main", Sha: "abcdef1234", Event: "manual", Status: "queued", Variables: map[string]string{}, Stages: pipeline.Stages, Jobs: []*ci.Job{{Idx: 0, Name: "hello", Stage: pipeline.Jobs[0].Stage, Image: "base", Deps: []int{}, Status: "queued", Steps: []ci.StepState{{Name: "echo hello", Phase: "main", Status: "pending"}}, Spec: pipeline.Jobs[0]}}}
	project := ci.ProjectView{ID: "demo", Name: "Demo", URL: "file:///demo", DefaultBranch: "main", ProtectedBranches: []string{"main", "release/*"}, NotifyOn: "failure", Refs: map[string]string{}, ScheduleFired: map[string]int64{}}
	host := agent.HostInfo{Hostname: "outer", Load: [3]float64{0, 0, 0}, Defaults: agent.Defaults{VCPUs: 1, MemMiB: 256}}
	hostJSON, _ := json.Marshal(host)
	vmsJSON, _ := json.Marshal([]agent.MicroVM{{ID: 1, Name: "vm1", State: "stopped", Owner: nil}})
	cluster := k8s.Overview{Configured: true, Cluster: &k8s.Cluster{Context: "demo", Server: "https://localhost:6443"}, Nodes: []k8s.Node{{Name: "node1", Roles: []string{"control-plane"}}}, Namespaces: []k8s.Namespace{{Name: "default", Phase: "Active"}}, Workloads: []k8s.Workload{{Name: "demo", Images: []string{}}}, Problems: []k8s.Problem{{Name: "pod"}}, Warnings: []k8s.Warning{{Namespace: "default", Object: "pod"}}}
	fixtures := map[string]any{
		"run": run, "run-summary": run.Summary(), "runs": []ci.RunSummary{},
		"project": project, "project-detail": map[string]any{"project": project, "webhookToken": "write-only-hook", "secrets": []ci.SecretInfo{{Name: "TOKEN", Protected: true}}, "runs": []ci.RunSummary{}},
		"secrets":        []ci.SecretInfo{{Name: "TOKEN", Protected: true}, {Name: "PUBLIC", Protected: false}},
		"lab":            lab.Status{Version: "test", Config: map[string]string{}, Host: hostJSON, VMs: vmsJSON},
		"panel-job":      jobs.Job{ID: "test", Title: "operation", Command: "host/up.sh", Status: "running", Group: "outer"},
		"panel-jobs":     []jobs.Job{},
		"kubernetes":     cluster,
		"images":         map[string]any{"built": []ci.Image{{Name: "base", SizeBytes: 1024, UsedBytes: 512, BuiltAt: 1}}, "definitions": []ci.ImageDefinition{{Name: "base", Size: "6G"}}},
		"images-offline": map[string]any{"built": nil, "definitions": []ci.ImageDefinition{}},
		"image-delete":   map[string]any{"built": []ci.Image{}},
		"vm-api":         map[string]any{"path": "/vm/config", "ok": true, "data": map[string]any{"machine-config": map[string]int{"vcpu_count": 1}}, "error": nil},
		"vm-console":     agent.Console{Text: "booted\n"},
		"vm-exec":        map[string]any{"code": 7, "stdout": "hello", "stderr": "error", "durationMs": 1},
		"log":            map[string]any{"text": "hello\n", "offset": 6, "reset": false, "done": false, "status": "running"},
		"error":          map[string]string{"error": "not authenticated"},
		"ok":             map[string]bool{"ok": true},
		"webhook":        map[string]any{"ok": true, "queued": "sync"},
		"stats":          ci.Stats{Outer: "off"},
	}
	for name, value := range fixtures {
		t.Run(name, func(t *testing.T) { golden(t, name, value) })
	}
}

func TestContractRoutes(t *testing.T) {
	t.Setenv("SPIKELOADER_AUTH", "off")
	s := New(&ci.Engine{}, nil)
	for _, tc := range []struct{ name, method, path, body string }{
		{"health", "GET", "/api/health", ""},
		{"lint", "POST", "/api/ci/lint", `{"yaml":"jobs:\n  hello:\n    steps: [echo hello]\n"}`},
		{"lint-invalid", "POST", "/api/ci/lint", `{"yaml":"not: a-pipeline"}`},
		{"overview", "GET", "/api/ci", ""},
		{"empty-secrets", "GET", "/api/ci/secrets/_global", ""},
		{"empty-runs", "GET", "/api/ci/runs", ""},
		{"missing-run", "GET", "/api/ci/runs/1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://localhost"+tc.path, bytes.NewBufferString(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			var value any
			if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
				t.Fatal(err)
			}
			if tc.name == "health" {
				m := value.(map[string]any)
				m["uptime_s"] = 0
				m["version"] = "test"
			}
			if tc.name == "overview" {
				m := value.(map[string]any)
				stats := m["stats"].(map[string]any)
				stats["maxJobs"] = 0
				stats["idleShutdownMin"] = 30
			}
			golden(t, "route-"+tc.name, map[string]any{"status": w.Code, "body": value})
		})
	}
}
