package main

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

var revision = "development"

func handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Spikeloader deploy demo</title></head>
<body style="font-family:system-ui;max-width:48rem;margin:5rem auto;padding:1rem;background:#101827;color:#e5edf8">
<h1>Deployed by Spikeloader</h1>
<p>Local Git &rarr; Spikeloader CI &rarr; container registry &rarr; k3s</p>
<p>Revision: <code>%s</code></p>
<p>Edit this application, commit to main, and run the pipeline to publish a new version.</p>
</body></html>
`, revision)
	})
	return mux
}

func main() {
	server := &http.Server{
		Addr: ":8080", Handler: handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
	log.Printf("demo listening on :8080, revision %s", revision)
	log.Fatal(server.ListenAndServe())
}
