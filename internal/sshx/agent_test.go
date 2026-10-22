package sshx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			w.WriteHeader(404)
			w.Write([]byte(`{"error":"unknown job"}`))
			return
		}
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("incorrect request")
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	previous := AgentClient
	AgentClient = &http.Client{Transport: rewriteTransport{server.URL}}
	defer func() { AgentClient = previous }()
	var out struct {
		OK bool `json:"ok"`
	}
	if err := AgentJSON(context.Background(), "POST", "/test", map[string]int{"value": 1}, &out); err != nil || !out.OK {
		t.Fatalf("JSON response: %+v, %v", out, err)
	}
	err := AgentJSON(context.Background(), "GET", "/missing", nil, &out)
	var ae *AgentError
	if !errors.As(err, &ae) || ae.Status != 404 || ae.Message != "unknown job" {
		t.Fatalf("error lost: %v", err)
	}
}

type rewriteTransport struct{ base string }

func (t rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	u, err := http.NewRequestWithContext(r.Context(), r.Method, t.base+r.URL.RequestURI(), r.Body)
	if err != nil {
		return nil, err
	}
	copy.URL = u.URL
	return http.DefaultTransport.RoundTrip(copy)
}
