I ?= 1
K3S ?= spikeloader-k3s

.PHONY: bootstrap up k3s k3s-reset db build test down purge ssh ls console web web-dev

# The outer VM and the microVMs are managed by the CI/CD engine (it runs inside the web panel).

bootstrap:     ## self-hosted setup: prerequisites, panel, database, kernel, local k3s + registry
	@./bootstrap.sh --k3s

up: k3s db     ## bring everything up: test k3s cluster + database + web panel/CI engine (foreground)
	@echo "panel: http://127.0.0.1:4100 — sign-in link: bin/spikeloader web url (not printed: it holds the token)"
	@bin/spikeloader web start

k3s:           ## start the local k3s + registry (skipped unless created by ./bootstrap.sh --k3s)
	@if docker container inspect $(K3S) >/dev/null 2>&1; then host/k3s.sh; \
	else echo "k3s: not set up (./bootstrap.sh --k3s), skipping"; fi

k3s-reset:     ## delete the local k3s cluster (workloads, certificates) and create it again
	@host/k3s.sh --reset

db:            ## start the PostgreSQL container (platform data)
	@bin/spikeloader db start

down:          ## shut down the outer VM now (keeps the disk; the next CI job starts it again)
	@host/down.sh

purge:         ## shut down and delete the outer VM disk
	@host/down.sh --purge

ssh:           ## shell on the outer VM
	@host/ssh.sh

ls:            ## list microVMs (CI jobs)
	@host/ssh.sh sudo fcctl ls

console:       ## serial console log of microVM I
	@host/ssh.sh sudo fcctl console $(I)

web:           ## daemon (CI/CD engine + API) and web panel at http://127.0.0.1:4100
	@bin/spikeloader web start

web-dev: db    ## same, with the web UI in hot-reload mode (the daemon is rebuilt when Go sources change)
	@bin/spikeloader web dev

build:         ## build the daemon, guest agent (Go) and web UI (Next.js) when their sources changed
	@bin/spikeloader build

test:          ## Go tests and vet, UI type check and lint
	@bash scripts/setup-test.sh
	@for script in bootstrap.sh bin/spikeloader host/*.sh guest/*.sh guest/fcctl ci/images/*.sh ci/k8s/*.sh scripts/*.sh; do bash -n "$$script" || exit; done
	@PATH="$$HOME/.local/go/bin:$$PATH" go vet ./... && PATH="$$HOME/.local/go/bin:$$PATH" go test ./...
	@cd examples/k3s-demo && PATH="$$HOME/.local/go/bin:$$PATH" go test ./...
	@cd web && npx next typegen && npx tsc --noEmit -p . && npx eslint .
