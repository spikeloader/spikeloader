// fcagent serves the outer VM's inspection API over a local Unix socket.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"spikeloader/internal/fcagent"
)

func main() {
	socket := flag.String("socket", "/run/fcagent.sock", "Unix socket path")
	lab := flag.String("lab", "/var/lib/fclab", "lab state directory")
	flag.Parse()
	if os.Geteuid() != 0 {
		log.Fatal("fcagent must run as root inside the outer VM")
	}
	listener, err := fcagent.Listen(*socket)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           fcagent.New(*lab).Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()
	log.Printf("fcagent listening on %s", *socket)
	err = server.Serve(listener)
	stop()
	<-done
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
