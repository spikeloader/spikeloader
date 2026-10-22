// Package api is the daemon's HTTP server: the JSON API used by the web UI and the CLI, the CI
// webhooks, the terminal WebSocket, sign-in, and a reverse proxy to the Next.js UI for every other
// path. The route contract (paths, bodies, JSON shapes) is the one web/ expects; see
// docs/backend.md before changing it.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"spikeloader/internal/auth"
	"spikeloader/internal/ci"
	"spikeloader/internal/env"
)

// Server wires the engine into HTTP.
type Server struct {
	Engine  *ci.Engine
	UI      *url.URL // Next.js UI upstream; nil = no UI
	started time.Time
	mux     *http.ServeMux
	ui      *httputil.ReverseProxy
}

// New builds the handler.
func New(e *ci.Engine, ui *url.URL) *Server {
	s := &Server{Engine: e, UI: ui, started: time.Now(), mux: http.NewServeMux()}
	if ui != nil {
		s.ui = httputil.NewSingleHostReverseProxy(ui)
		s.ui.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "spikeloader: the web UI is not up yet (starting?) — "+err.Error()+"\n")
		}
	}
	s.routes()
	return s
}

/* ---------- middleware ---------- */

// public paths skip sign-in: the login page and its API, health, CI webhooks (they carry their own
// token) and static assets.
func public(path string) bool {
	switch path {
	case "/login", "/api/auth/login", "/api/health", "/favicon.ico", "/icon.svg", "/manifest.webmanifest":
		return true
	}
	for _, p := range []string{"/_next/static/", "/_next/image", "/apple-icon", "/icons/", "/api/ci/hooks/"} {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// localHost reports whether a Host/Origin header names this machine (blocks DNS rebinding and
// cross-site WebSocket hijacking). SPIKELOADER_ALLOWED_HOSTS adds names (e.g. behind a proxy).
func localHost(host string) bool {
	if host == "" {
		return false
	}
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	name = strings.Trim(name, "[]")
	allowed := []string{"localhost", "127.0.0.1", "::1"}
	for _, h := range strings.Split(os.Getenv("SPIKELOADER_ALLOWED_HOSTS"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			allowed = append(allowed, h)
		}
	}
	for _, a := range allowed {
		if name == a {
			return true
		}
	}
	return false
}

// guard: routes run commands as root, so only requests aimed at this machine are accepted, and
// changes must come from the panel itself as JSON (blocks CSRF: a form cannot send JSON).
func guard(w http.ResponseWriter, r *http.Request, mutating bool) bool {
	if !localHost(r.Host) {
		writeError(w, 403, "host not allowed")
		return false
	}
	if mutating {
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !localHost(u.Host) {
				writeError(w, 403, "origin not allowed")
				return false
			}
		}
		if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			writeError(w, 415, "content-type must be application/json")
			return false
		}
	}
	return true
}

func securityHeaders(h http.Header) {
	// the panel runs commands as root: no framing (clickjacking), no MIME sniffing, no referrer leaks
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
}

// ServeHTTP enforces sign-in, then dispatches to the API or the UI.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w.Header())
	path := r.URL.Path
	if !public(path) && !auth.Authorized(r) {
		if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/ws/") {
			writeError(w, 401, "not authenticated")
			return
		}
		login := "/login"
		if path != "/" {
			login += "?next=" + url.QueryEscape(r.URL.RequestURI())
		}
		http.Redirect(w, r, login, http.StatusTemporaryRedirect)
		return
	}
	if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/ws/") {
		s.mux.ServeHTTP(w, r)
		return
	}
	if s.ui == nil {
		writeError(w, 404, "no web UI configured (SPIKELOADER_UI=off)")
		return
	}
	s.ui.ServeHTTP(w, r)
}

/* ---------- helpers ---------- */

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeRaw(w http.ResponseWriter, status int, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// respond maps engine errors to their status, anything else to 500.
func respond(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		if e, ok := ci.IsError(err); ok {
			writeError(w, e.Status, e.Msg)
			return
		}
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, status, v)
}

// body decodes a JSON body; a missing or invalid body leaves v at its zero value.
func body(r *http.Request, v any) {
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(v)
}

func checkedBody(w http.ResponseWriter, r *http.Request, v any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	if err := decoder.Decode(v); err != nil {
		writeError(w, 400, "invalid JSON body: "+err.Error())
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeError(w, 400, "expected one JSON body")
		return false
	}
	return true
}

// handle registers a guarded API route.
func (s *Server) handle(pattern string, mutating bool, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if guard(w, r, mutating) {
			h(w, r)
		}
	})
}

// Listen serves until ctx is done.
func (s *Server) Listen(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Printf("spikeloader %s at http://%s", env.Version(), addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
