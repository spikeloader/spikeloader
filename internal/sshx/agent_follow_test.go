package sshx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"spikeloader/internal/agent"
)

func TestAgentFollowResumesAtByteOffset(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			if r.URL.Query().Get("offset") != "0" {
				t.Error("bad initial offset")
			}
			json.NewEncoder(w).Encode(agent.Chunk{Data: []byte("first\n"), Offset: 6})
			return // Simulate transport EOF without a final status frame.
		}
		if r.URL.Query().Get("offset") != "6" {
			t.Error("restarted at wrong offset")
		}
		code := 0
		json.NewEncoder(w).Encode(agent.Chunk{Data: []byte("second\n"), Offset: 13})
		json.NewEncoder(w).Encode(agent.Chunk{Offset: 13, Code: &code})
	}))
	defer server.Close()
	old := AgentClient
	AgentClient = &http.Client{Transport: rewriteTransport{server.URL}}
	defer func() { AgentClient = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out strings.Builder
	code, err := AgentFollow(ctx, "job", func(b []byte) { out.Write(b) })
	if err != nil || code != 0 || out.String() != "first\nsecond\n" || calls.Load() != 2 {
		t.Fatalf("output %q code %d err %v calls %d", out.String(), code, err, calls.Load())
	}
}
