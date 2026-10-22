package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"spikeloader/internal/auth"
	"spikeloader/internal/ci"
)

func TestPublicPaths(t *testing.T) {
	for _, path := range []string{"/login", "/api/auth/login", "/api/health", "/favicon.ico", "/icon.svg", "/manifest.webmanifest", "/_next/static/a.js", "/_next/image", "/icons/a.svg", "/apple-icon.png", "/api/ci/hooks/demo"} {
		if !public(path) {
			t.Errorf("not public: %s", path)
		}
	}
	for _, path := range []string{"/", "/ci", "/api/status", "/api/ci", "/api/auth/logout", "/ws/terminal", "/api/ci/hooks", "/api/ci/hooks-evil/demo"} {
		if public(path) {
			t.Errorf("unexpectedly public: %s", path)
		}
	}
}

func TestGuard(t *testing.T) {
	t.Setenv("SPIKELOADER_ALLOWED_HOSTS", "panel.local")
	for _, tc := range []struct {
		host, origin, content string
		mutating              bool
		status                int
	}{
		{"localhost:4100", "", "", false, 200},
		{"127.0.0.1:4100", "", "application/json", true, 200},
		{"[::1]:4100", "http://[::1]:4100", "application/json; charset=utf-8", true, 200},
		{"panel.local", "https://panel.local", "application/json", true, 200},
		{"evil.test", "", "application/json", true, 403},
		{"localhost", "https://evil.test", "application/json", true, 403},
		{"localhost", "null", "application/json", true, 403},
		{"localhost", "", "text/plain", true, 415},
		{"localhost", "", "", true, 415},
	} {
		r := httptest.NewRequest("POST", "http://localhost/api/test", nil)
		r.Host = tc.host
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", tc.content)
		w := httptest.NewRecorder()
		ok := guard(w, r, tc.mutating)
		if w.Code != tc.status || ok != (tc.status == 200) {
			t.Errorf("%+v: status=%d ok=%v", tc, w.Code, ok)
		}
	}
}

func TestAuthenticationAndSignIn(t *testing.T) {
	t.Setenv("SPIKELOADER_AUTH", "on")
	t.Setenv("SPIKELOADER_TOKEN", "contract-token")
	s := New(&ci.Engine{}, nil)
	request := func(method, path, body, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		if method != "GET" {
			r.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/ci", "/api/status", "/ws/terminal"} {
		w := request("GET", path, "", "", nil)
		if w.Code != 401 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		if w.Header().Get("X-Frame-Options") != "DENY" {
			t.Fatal("missing security headers")
		}
	}
	w := request("GET", "/ci/projects/demo", "", "", nil)
	if w.Code != 307 || !strings.HasPrefix(w.Header().Get("Location"), "/login?next=") {
		t.Fatal(w)
	}
	if w := request("GET", "/api/health", "", "", nil); w.Code != 200 {
		t.Fatal(w)
	}
	if w := request("POST", "/api/auth/login", `{"token":"wrong"}`, "", nil); w.Code != 401 {
		t.Fatal(w)
	}
	login := request("POST", "/api/auth/login", `{"token":"contract-token"}`, "", nil)
	if login.Code != 200 {
		t.Fatal(login)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal(cookies)
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != auth.SessionMaxAge || cookie.Path != "/" {
		t.Fatal(cookie)
	}
	for _, mode := range []string{"cookie", "bearer"} {
		var c *http.Cookie
		token := ""
		if mode == "cookie" {
			c = cookie
		} else {
			token = "contract-token"
		}
		if w := request("GET", "/api/ci", "", token, c); w.Code != 200 {
			t.Fatalf("%s: %d %s", mode, w.Code, w.Body)
		}
	}
	t.Setenv("SPIKELOADER_TOKEN", "rotated")
	if w := request("GET", "/api/ci", "", "", cookie); w.Code != 401 {
		t.Fatal("rotation did not invalidate session")
	}
	logout := request("POST", "/api/auth/logout", `{}`, "rotated", nil)
	if logout.Code != 200 || logout.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal(logout)
	}
}

func TestWebhookAndMalformedChanges(t *testing.T) {
	t.Setenv("SPIKELOADER_AUTH", "on")
	t.Setenv("SPIKELOADER_TOKEN", "test")
	s := New(&ci.Engine{}, nil)
	for _, header := range []string{"", "X-Spikeloader-Token", "X-Gitlab-Token", "Authorization"} {
		r := httptest.NewRequest("POST", "http://untrusted.example/api/ci/hooks/unknown", strings.NewReader(`{}`))
		if header != "" {
			r.Header.Set(header, "wrong")
		} else {
			r.URL.RawQuery = "token=wrong"
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 401 || !strings.Contains(w.Body.String(), "invalid webhook token") {
			t.Fatalf("%s: %d %s", header, w.Code, w.Body)
		}
	}
	for _, path := range []string{"/api/ci/projects", "/api/ci/projects/demo", "/api/ci/secrets/_global"} {
		method := "POST"
		if strings.HasSuffix(path, "demo") {
			method = "PATCH"
		}
		if strings.Contains(path, "secrets") {
			method = "PUT"
		}
		for _, body := range []string{`{`, `{"protected":"false"}`, `{} {}`} {
			if body == `{"protected":"false"}` && !strings.Contains(path, "secrets") {
				continue
			}
			r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer test")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatalf("%s %s: %d %s", path, body, w.Code, w.Body)
			}
		}
	}
}
