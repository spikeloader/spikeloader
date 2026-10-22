package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoutes(t *testing.T) {
	for _, tc := range []struct {
		path string
		code int
		text string
	}{
		{"/", http.StatusOK, "Deployed by Spikeloader"},
		{"/", http.StatusOK, "Spikeloader CI"},
		{"/healthz", http.StatusOK, "ok\n"},
		{"/missing", http.StatusNotFound, "404"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler().ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.text) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}
