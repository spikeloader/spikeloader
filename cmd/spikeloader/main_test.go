package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommands(t *testing.T) {
	run := `{"id":7,"status":"success","project":"demo","branch":"main","sha":"abcdef1234","event":"manual","message":"demo","stages":["test"],"jobs":[{"idx":0,"name":"hello","stage":"test","status":"success","steps":[]}]}`
	for _, tc := range []struct {
		args                         []string
		method, path, response, want string
	}{
		{[]string{"ls"}, "GET", "/api/ci", `{"projects":[],"stats":{}}`, "no projects"},
		{[]string{"add", "demo", "file:///tmp/demo", "--poll", "0"}, "POST", "/api/ci/projects", `{"id":"demo","pollS":0}`, "project demo added"},
		{[]string{"rm", "demo"}, "DELETE", "/api/ci/projects/demo", `{"ok":true}`, "project demo removed"},
		{[]string{"hook", "demo"}, "GET", "/api/ci/projects/demo", `{"webhookToken":"hook"}`, "/api/ci/hooks/demo?token=hook"},
		{[]string{"fetch", "demo"}, "POST", "/api/ci/projects/demo/sync", `{"refs":{"main":"sha"}}`, "fetched 1 branches"},
		{[]string{"run", "demo"}, "POST", "/api/ci/projects/demo/run", run, "run #7 started"},
		{[]string{"runs", "demo"}, "GET", "/api/ci/runs?project=demo", "[" + run + "]", "#7"},
		{[]string{"show", "7"}, "GET", "/api/ci/runs/7", run, "hello"},
		{[]string{"cancel", "7"}, "POST", "/api/ci/runs/7", run, "run #7: success"},
		{[]string{"rerun", "7"}, "POST", "/api/ci/runs/7", run, "run #7 started"},
		{[]string{"secret", "ls", "--global"}, "GET", "/api/ci/secrets/_global", `[{"name":"TOKEN","protected":true}]`, "TOKEN [protected]"},
		{[]string{"secret", "set", "demo", "TOKEN", "--unprotected"}, "PUT", "/api/ci/secrets/demo", `[]`, "secret TOKEN saved"},
		{[]string{"secret", "rm", "demo", "TOKEN"}, "DELETE", "/api/ci/secrets/demo", `[]`, "secret TOKEN removed"},
		{[]string{"images"}, "GET", "/api/ci/images", `{"built":null,"definitions":[]}`, "none yet"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != tc.method || r.URL.RequestURI() != tc.path {
					t.Errorf("request %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("missing authorization")
				}
				if r.Method != "GET" {
					if r.Header.Get("Content-Type") != "application/json" {
						t.Error("missing JSON guard header")
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if tc.args[0] == "secret" && tc.args[1] == "set" {
						if body["value"] != "value" || body["protected"] != false {
							t.Errorf("secret body %v", body)
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(tc.response))
			}))
			defer srv.Close()
			var out bytes.Buffer
			c := client{base: srv.URL, token: "test-token", http: srv.Client(), in: strings.NewReader("value\n"), out: &out, errOut: &out}
			if err := c.run(tc.args); err != nil {
				t.Fatal(err)
			}
			if requests != 1 || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("requests=%d out=%s", requests, &out)
			}
		})
	}
}

func TestFollowAndJobActions(t *testing.T) {
	for _, cmd := range []string{"run", "logs", "retry", "approve"} {
		t.Run(cmd, func(t *testing.T) {
			var out bytes.Buffer
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/log"):
					if r.URL.Query().Get("offset") != "0" {
						t.Fatal("wrong offset")
					}
					w.Write([]byte(`{"text":"HELLO\n","offset":6,"done":true}`))
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/7"):
					var b struct {
						Action string
						Job    int
					}
					json.NewDecoder(r.Body).Decode(&b)
					if b.Action != cmd || b.Job != 0 {
						t.Errorf("%+v", b)
					}
					w.Write([]byte(`{}`))
				default:
					calls++
					w.Write([]byte(`{"id":7,"status":"success","jobs":[{"idx":0,"name":"hello","status":"success"}]}`))
				}
			}))
			defer srv.Close()
			c := client{base: srv.URL, http: srv.Client(), out: &out, errOut: &out, poll: time.Millisecond}
			args := []string{cmd, "7", "hello"}
			if cmd == "run" {
				args = []string{"run", "demo", "-f"}
			}
			if err := c.run(args); err != nil {
				t.Fatal(err)
			}
			if calls == 0 {
				t.Fatal("missing requests")
			}
			if (cmd == "run" || cmd == "logs") && strings.Count(out.String(), "HELLO") != 1 {
				t.Fatal(out.String())
			}
		})
	}
}

func TestLintAndErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "pipeline.yml")
	if err := os.WriteFile(file, []byte("jobs: {}"), 0600); err != nil {
		t.Fatal(err)
	}
	templateDir := filepath.Join(dir, ".spikeloader", "templates", "build")
	if err := os.MkdirAll(templateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "go.yaml"), []byte("steps: [true]"), 0600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Templates map[string]string }
		json.NewDecoder(r.Body).Decode(&b)
		if b.Templates["build/go"] == "" {
			t.Error("local templates missing")
		}
		w.Write([]byte(`{"ok":true,"stages":["test"],"jobs":[{"name":"hello","stage":"test","image":"base","needs":null}]}`))
	}))
	defer srv.Close()
	var out bytes.Buffer
	c := client{base: srv.URL, http: srv.Client(), out: &out, errOut: &out}
	if err := c.run([]string{"lint", file}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"unknown"}, {"add"}, {"add", "a", "b", "--poll", "bad"}, {"run"}, {"retry", "7"}, {"secret", "set", "demo"}, {"add", "--branch"}} {
		if err := c.run(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
