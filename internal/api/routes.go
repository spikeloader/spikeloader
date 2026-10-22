package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"spikeloader/internal/agent"
	"spikeloader/internal/auth"
	"spikeloader/internal/ci"
	"spikeloader/internal/env"
	"spikeloader/internal/jobs"
	"spikeloader/internal/k8s"
	"spikeloader/internal/lab"
	"spikeloader/internal/sshx"
)

const logChunk = 512 * 1024

var (
	imageNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	ansiRe      = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	colorRe     = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	fileNameRe  = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	bearerRe    = regexp.MustCompile(`(?i)^Bearer\s+`)
)

// Firecracker API GET endpoints exposed to the panel (read-only).
var vmAPIPaths = map[string]bool{"/": true, "/vm/config": true, "/machine-config": true, "/version": true, "/mmds": true, "/balloon": true, "/balloon/statistics": true}

func (s *Server) routes() {
	e := s.Engine

	/* ---------- session ---------- */

	s.handle("POST /api/auth/login", true, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Token string `json:"token"`
		}
		body(r, &b)
		if !auth.CheckToken(b.Token) {
			time.Sleep(600 * time.Millisecond) // slows down attempts
			writeError(w, 401, "invalid token")
			return
		}
		cookie := &http.Cookie{Name: auth.Cookie, Value: auth.SessionValue(), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: auth.SessionMaxAge}
		cookie.Secure = r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
		http.SetCookie(w, cookie)
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	s.handle("POST /api/auth/logout", true, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: auth.Cookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// liveness: no auth, exposes no lab data
	s.mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "service": "spikeloader", "version": env.Version(), "uptime_s": int(time.Since(s.started).Seconds()), "backend": "go"})
	})

	/* ---------- lab ---------- */

	s.handle("GET /api/status", false, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, lab.GetStatus())
	})
	s.handle("GET /api/jobs", false, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, jobs.List())
	})
	s.handle("GET /api/jobs/{id}", false, func(w http.ResponseWriter, r *http.Request) {
		j, ok := jobs.Get(r.PathValue("id"))
		if !ok {
			writeError(w, 404, "job not found")
			return
		}
		writeJSON(w, 200, j)
	})
	s.handle("GET /api/k8s", false, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, k8s.Get(r.Context()))
	})

	/* ---------- microVMs (debugging) ---------- */

	s.handle("GET /api/vms/{id}/api", false, func(w http.ResponseWriter, r *http.Request) {
		id, ok := lab.ParseID(r.PathValue("id"))
		path := r.URL.Query().Get("path")
		if path == "" {
			path = "/vm/config"
		}
		if !ok || !vmAPIPaths[path] {
			paths := make([]string, 0, len(vmAPIPaths))
			for p := range vmAPIPaths {
				paths = append(paths, p)
			}
			writeJSON(w, 400, map[string]any{"error": "invalid parameters", "paths": paths})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		var data any
		err := sshx.AgentJSON(ctx, "GET", fmt.Sprintf("/vms/%d/api?path=%s", id, url.QueryEscape(path)), nil, &data)
		var errMsg any
		if err != nil {
			errMsg = err.Error()
		}
		writeJSON(w, 200, map[string]any{"path": path, "ok": err == nil, "data": data, "error": errMsg})
	})
	s.handle("GET /api/vms/{id}/console", false, func(w http.ResponseWriter, r *http.Request) {
		id, ok := lab.ParseID(r.PathValue("id"))
		if !ok {
			writeError(w, 400, "invalid id")
			return
		}
		lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
		if lines == 0 {
			lines = 300
		}
		lines = min(max(lines, 10), 5000)
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		var res agent.Console
		if err := sshx.AgentJSON(ctx, "GET", fmt.Sprintf("/vms/%d/console?lines=%d", id, lines), nil, &res); err != nil {
			writeError(w, 502, err.Error())
			return
		}
		// strip ANSI sequences from the serial console
		writeJSON(w, 200, map[string]string{"text": strings.ReplaceAll(ansiRe.ReplaceAllString(res.Text, ""), "\r", "")})
	})
	s.handle("POST /api/vms/{id}/exec", true, func(w http.ResponseWriter, r *http.Request) {
		id, ok := lab.ParseID(r.PathValue("id"))
		if !ok {
			writeError(w, 400, "invalid id")
			return
		}
		var b struct {
			Command string `json:"command"`
		}
		body(r, &b)
		if strings.TrimSpace(b.Command) == "" || len(b.Command) > 8192 {
			writeError(w, 400, "invalid command")
			return
		}
		started := time.Now()
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		var stdout, stderr strings.Builder
		code, err := sshx.AgentFrames(ctx, "POST", fmt.Sprintf("/vms/%d/exec?command=%s", id, url.QueryEscape(b.Command)), nil, sshx.Stream{Stdout: &stdout, OnData: func(b []byte) { stderr.Write(b) }})
		if err != nil {
			writeError(w, 502, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"code": code, "stdout": stdout.String(), "stderr": stderr.String(), "durationMs": time.Since(started).Milliseconds()})
	})

	/* ---------- CI ---------- */

	s.handle("GET /api/ci", false, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, e.Overview())
	})
	s.handle("POST /api/ci/lint", true, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			YAML      *string           `json:"yaml"`
			Templates map[string]string `json:"templates"`
		}
		body(r, &b)
		if b.YAML == nil {
			writeJSON(w, 400, map[string]any{"ok": false, "errors": []string{"yaml is required"}})
			return
		}
		builtin := ci.TemplateReaderFor(r.Context(), "", "")
		pl, errs := ci.ParsePipeline(*b.YAML, func(name string) (string, bool) {
			if t, ok := b.Templates[name]; ok {
				return t, true
			}
			return builtin(name)
		})
		if pl == nil {
			writeJSON(w, 200, map[string]any{"ok": false, "errors": errs})
			return
		}
		deps := ci.Dependencies(pl.Jobs, pl.Stages)
		out := []map[string]any{}
		for i, j := range pl.Jobs {
			needs := []string{}
			for _, d := range deps[i] {
				needs = append(needs, pl.Jobs[d].Name)
			}
			steps := []string{}
			for _, list := range [][]ci.StepSpec{j.Before, j.Steps, j.After} {
				for _, st := range list {
					steps = append(steps, st.Name)
				}
			}
			out = append(out, map[string]any{"name": j.Name, "stage": j.Stage, "image": j.Image, "needs": needs, "steps": steps, "manual": j.Manual, "secrets": j.Secrets})
		}
		writeJSON(w, 200, map[string]any{"ok": true, "errors": []string{}, "on": pl.On, "stages": pl.Stages, "jobs": out})
	})

	s.handle("GET /api/ci/images", false, func(w http.ResponseWriter, r *http.Request) {
		var built any
		list, err := ci.ListImages()
		switch {
		case err == nil:
			built = list
		case !lab.OuterRunning():
			built = nil // the outer VM is off: images cannot be listed
		default:
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"built": built, "definitions": ci.BuiltinImages()})
	})
	s.handle("DELETE /api/ci/images", true, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Name string `json:"name"`
		}
		body(r, &b)
		if !imageNameRe.MatchString(b.Name) {
			writeError(w, 500, "invalid image name")
			return
		}
		if err := ci.RemoveImage(b.Name); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		list, err := ci.ListImages()
		respond(w, 200, map[string]any{"built": list}, err)
	})

	s.handle("POST /api/ci/projects", true, func(w http.ResponseWriter, r *http.Request) {
		var in ci.ProjectInput
		if !checkedBody(w, r, &in) {
			return
		}
		v, err := e.CreateProject(in)
		respond(w, 201, v, err)
	})
	s.handle("GET /api/ci/projects/{id}", false, func(w http.ResponseWriter, r *http.Request) {
		v, err := e.ProjectDetail(r.PathValue("id"))
		respond(w, 200, v, err)
	})
	s.handle("PATCH /api/ci/projects/{id}", true, func(w http.ResponseWriter, r *http.Request) {
		var patch ci.ProjectPatch
		if !checkedBody(w, r, &patch) {
			return
		}
		v, err := e.UpdateProject(r.PathValue("id"), patch)
		respond(w, 200, v, err)
	})
	s.handle("DELETE /api/ci/projects/{id}", true, func(w http.ResponseWriter, r *http.Request) {
		err := e.DeleteProject(r.PathValue("id"))
		respond(w, 200, map[string]bool{"ok": true}, err)
	})
	s.handle("POST /api/ci/projects/{id}/run", true, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Ref string `json:"ref"`
		}
		body(r, &b)
		v, err := e.Trigger(r.Context(), r.PathValue("id"), b.Ref)
		respond(w, 201, v, err)
	})
	s.handle("POST /api/ci/projects/{id}/sync", true, func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := e.Sync(r.Context(), id); err != nil {
			respond(w, 0, nil, err)
			return
		}
		v, err := e.ProjectView(id)
		respond(w, 200, v, err)
	})

	s.handle("GET /api/ci/secrets/{scope}", false, func(w http.ResponseWriter, r *http.Request) {
		v, err := e.SecretNames(r.PathValue("scope"))
		respond(w, 200, v, err)
	})
	s.handle("PUT /api/ci/secrets/{scope}", true, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Name      string `json:"name"`
			Value     any    `json:"value"`
			Protected *bool  `json:"protected"`
		}
		if !checkedBody(w, r, &b) {
			return
		}
		v, err := e.SetSecret(r.PathValue("scope"), b.Name, b.Value, b.Protected)
		respond(w, 200, v, err)
	})
	s.handle("DELETE /api/ci/secrets/{scope}", true, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Name string `json:"name"`
		}
		body(r, &b)
		writeJSON(w, 200, e.DeleteSecret(r.PathValue("scope"), b.Name))
	})

	s.handle("GET /api/ci/runs", false, func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 50
		}
		writeJSON(w, 200, e.ListRuns(r.URL.Query().Get("project"), min(limit, 500)))
	})
	s.handle("GET /api/ci/runs/{id}", false, func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		b, err := e.RunJSON(id)
		if err != nil {
			respond(w, 0, nil, err)
			return
		}
		writeRaw(w, 200, b)
	})
	// { action: "cancel" | "rerun" | "retry" | "approve", job?: index }
	s.handle("POST /api/ci/runs/{id}", true, func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		var b struct {
			Action string `json:"action"`
			Job    int    `json:"job"`
		}
		body(r, &b)
		var v ci.RunSummary
		var err error
		status := 200
		switch b.Action {
		case "cancel":
			v, err = e.Cancel(id)
		case "rerun":
			v, err = e.Rerun(r.Context(), id)
			status = 201
		case "retry":
			v, err = e.RetryJob(id, b.Job)
		case "approve":
			v, err = e.Approve(id, b.Job)
		default:
			writeError(w, 400, "invalid action")
			return
		}
		respond(w, status, v, err)
	})
	// log tail from `offset` (bytes); the browser polls with the returned offset; ?raw=1 downloads it all
	s.handle("GET /api/ci/runs/{id}/jobs/{job}/log", false, func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		idx, _ := strconv.Atoi(r.PathValue("job"))
		info, err := e.JobInfo(id, idx)
		if err != nil {
			respond(w, 0, nil, err)
			return
		}
		file := ci.JobLog(info.Run, info.Idx)
		q := r.URL.Query()
		if q.Get("raw") != "" {
			b, _ := os.ReadFile(file)
			name := fmt.Sprintf("%s-%d-%s.log", info.Project, info.Run, fileNameRe.ReplaceAllString(info.Name, "_"))
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
			_, _ = w.Write([]byte(colorRe.ReplaceAllString(string(b), "")))
			return
		}
		offset, _ := strconv.ParseInt(q.Get("offset"), 10, 64)
		offset = max(offset, 0)
		var size int64
		if st, err := os.Stat(file); err == nil {
			size = st.Size()
		}
		start := offset
		if offset > size {
			start = 0
		}
		n := min(size-start, logChunk)
		text := ""
		if n > 0 {
			if f, err := os.Open(file); err == nil {
				buf := make([]byte, n)
				m, _ := f.ReadAt(buf, start)
				f.Close()
				text = string(buf[:m])
				n = int64(m)
			}
		}
		finished := !map[string]bool{"pending": true, "queued": true, "running": true, "manual": true}[info.Status]
		writeJSON(w, 200, map[string]any{"text": text, "offset": start + max(n, 0), "reset": start != offset, "done": finished && start+max(n, 0) >= size, "status": info.Status})
	})
	s.handle("GET /api/ci/runs/{id}/jobs/{job}/artifacts", false, func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		idx, _ := strconv.Atoi(r.PathValue("job"))
		info, err := e.JobInfo(id, idx)
		if err != nil {
			writeError(w, 404, "run not found")
			return
		}
		if !info.HasArtifacts {
			writeError(w, 404, "this job has no artifacts")
			return
		}
		f, err := os.Open(ci.JobArtifacts(info.Run, info.Idx))
		if err != nil {
			writeError(w, 404, "this job has no artifacts")
			return
		}
		defer f.Close()
		st, _ := f.Stat()
		name := fmt.Sprintf("%s-%d-%s.tar.gz", info.Project, info.Run, fileNameRe.ReplaceAllString(info.Name, "_"))
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		_, _ = io.Copy(w, f)
	})

	// Generic webhook (GitHub, GitLab, Gitea, Bitbucket, curl…): any POST with the project token makes
	// the engine fetch the remote now. No session and no Host check: it is meant to be reached
	// through a tunnel. The payload is ignored, so every provider works the same.
	s.mux.HandleFunc("POST /api/ci/hooks/{id}", func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		for _, h := range []string{"X-Spikeloader-Token", "X-Gitlab-Token"} {
			if token == "" {
				token = r.Header.Get(h)
			}
		}
		if token == "" {
			token = strings.TrimSpace(bearerRe.ReplaceAllString(r.Header.Get("Authorization"), ""))
		}
		if err := e.CheckWebhook(r.PathValue("id"), token); err != nil {
			if ce, ok := ci.IsError(err); ok {
				if ce.Status == 404 {
					writeError(w, 401, "invalid webhook token")
				} else {
					writeError(w, ce.Status, ce.Msg)
				}
				return
			}
			writeError(w, 500, "webhook failed")
			return
		}
		writeJSON(w, 202, map[string]any{"ok": true, "queued": "sync"})
	})

	s.mux.HandleFunc("GET /ws/terminal", s.terminal)
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
