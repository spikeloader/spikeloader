// spikeloaderd is the spikeloader daemon: the CI/CD engine, the HTTP API, and the front door of
// the web panel (it supervises the Next.js UI and proxies it).
//
// Configuration (environment):
//
//	SPIKELOADER_HOST, SPIKELOADER_PORT   listen address (default 127.0.0.1:4100)
//	SPIKELOADER_UI                       prod (default: `next start`), dev (`next dev`), off, or an http:// URL
//	SPIKELOADER_UI_PORT                  internal port of the supervised UI (default 4110)
//	SPIKELOADER_AUTH=off                 no sign-in (refused unless listening on loopback)
//	SPIKELOADER_ROOT                     repository checkout (default: found from the binary's location)
//	DATABASE_URL                         PostgreSQL (default: the spikeloader-postgres container)
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"spikeloader/internal/api"
	"spikeloader/internal/auth"
	"spikeloader/internal/ci"
	"spikeloader/internal/db"
	"spikeloader/internal/env"
	"spikeloader/internal/jobs"
	"spikeloader/internal/sshx"
)

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	defer sshx.CloseAgent()
	log.SetFlags(log.Ltime)
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("spikeloaderd", env.Version())
		return
	}
	// not HOST/PORT: zsh sets HOST to the machine name, and an exported one would put the panel on the network
	host := getenv("SPIKELOADER_HOST", "127.0.0.1")
	port := getenv("SPIKELOADER_PORT", "4100")
	loopback := host == "127.0.0.1" || host == "localhost" || host == "::1"
	if !auth.Enabled() && !loopback {
		log.Fatalf("✗ SPIKELOADER_AUTH=off is only allowed on a loopback address (SPIKELOADER_HOST=%s)", host)
	}
	if os.Geteuid() == 0 {
		log.Fatal("✗ do not run spikeloader as root: files would end up owned by root")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if _, err := auth.Token(); err != nil {
		log.Fatalf("✗ access token: %v", err)
	}
	if err := db.Open(ctx); err != nil {
		log.Fatalf("✗ %v", err)
	}
	if err := jobs.Load(ctx); err != nil {
		log.Printf("! panel history not loaded: %v", err)
	}
	engine, err := ci.New(ctx)
	if err != nil {
		log.Fatalf("✗ CI engine: %v", err)
	}

	ui, err := startUI(ctx)
	if err != nil {
		log.Fatalf("✗ web UI: %v", err)
	}

	if !auth.Enabled() {
		log.Print("! authentication disabled (SPIKELOADER_AUTH=off)")
	}
	if !loopback {
		log.Printf("! listening on %s: reachable from the network over plain HTTP — put it behind TLS", host)
	}
	addr := net.JoinHostPort(host, port)
	if err := api.New(engine, ui).Listen(ctx, addr); err != nil {
		if isAddrInUse(err) {
			log.Fatalf("✗ port %s is already in use (another panel running?). Use SPIKELOADER_PORT=<other>", port)
		}
		log.Fatalf("✗ %v", err)
	}
	db.Flush(5 * time.Second)
}

func isAddrInUse(err error) bool {
	var se *os.SyscallError
	for e := err; e != nil; {
		if op, ok := e.(*net.OpError); ok {
			e = op.Err
			continue
		}
		if s, ok := e.(*os.SyscallError); ok {
			se = s
		}
		break
	}
	return se != nil && se.Err == syscall.EADDRINUSE
}

// startUI returns the UI upstream, starting and supervising `next start|dev` when it is local.
func startUI(ctx context.Context) (*url.URL, error) {
	mode := getenv("SPIKELOADER_UI", "prod")
	switch mode {
	case "off":
		return nil, nil
	case "prod", "dev":
	default:
		return url.Parse(mode)
	}
	uiPort := getenv("SPIKELOADER_UI_PORT", "4110")
	web := filepath.Join(env.Root, "web")
	next := filepath.Join(web, "node_modules", ".bin", "next")
	if _, err := os.Stat(next); err != nil {
		return nil, fmt.Errorf("%s not found: install the UI (cd web && npm ci)", next)
	}
	sub := "start"
	if mode == "dev" {
		sub = "dev"
	} else if _, err := os.Stat(filepath.Join(web, ".next", "BUILD_ID")); err != nil {
		return nil, fmt.Errorf("the UI is not built: cd web && npm run build")
	}
	go func() {
		for backoff := time.Second; ctx.Err() == nil; backoff = min(backoff*2, 30*time.Second) {
			started := time.Now()
			cmd := exec.CommandContext(ctx, next, sub, "-H", "127.0.0.1", "-p", uiPort)
			cmd.Dir = web
			cmd.Env = append(os.Environ(), "NEXT_TELEMETRY_DISABLED=1")
			if mode == "prod" {
				cmd.Env = append(cmd.Env, "NODE_ENV=production")
			}
			cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
			cmd.WaitDelay = 5 * time.Second
			out, _ := cmd.StdoutPipe()
			cmd.Stderr = cmd.Stdout
			if err := cmd.Start(); err != nil {
				log.Printf("ui: %v", err)
			} else {
				go prefix(out, "ui: ")
				err := cmd.Wait()
				if ctx.Err() != nil {
					return
				}
				log.Printf("ui: exited (%v); restarting", err)
			}
			if time.Since(started) > time.Minute {
				backoff = time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
		}
	}()
	return url.Parse("http://127.0.0.1:" + uiPort)
}

func prefix(r io.Reader, p string) {
	s := bufio.NewScanner(r)
	for s.Scan() {
		log.Print(p + s.Text())
	}
}
