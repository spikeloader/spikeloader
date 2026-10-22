# k3s deploy demo

This directory ships as example source, without Git metadata. Initialize an independent Git
repository before registering it. The pipeline tests and builds a small Go HTTP
application, builds a `scratch` container from its binary artifact, pushes it to the local registry,
and deploys two replicas to the local k3s cluster using Spikeloader's built-in templates.

## Prerequisites

- The parent project's `make up` is running (daemon on port 4100, k3s and local registry).
- The pipeline's `REGISTRY` matches `docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}'`
  followed by `:5000` (default `172.17.0.1:5000`).
- A protected project secret `KUBECONFIG_DATA` contains namespace-scoped deploy credentials.
  Never commit kubeconfigs, tokens or the parent project's state.

## Register and configure

From the parent project, initialize the example once (skip this block if you already
initialized it). The application must be at the root of its own Git repository, not
at the root of the Spikeloader checkout:

```bash
git init -b main examples/k3s-demo
git -C examples/k3s-demo add .
git -C examples/k3s-demo -c user.name="Demo" -c user.email="demo@localhost" \
  commit -m "Initialize deployment demo"
```

Configure your Git identity for later commits, or supply the same `-c` options.
Run from the parent project (requires `kubectl` on the host):

```bash
set -o pipefail
bin/spikeloader ci add k3s-demo "file://$(pwd)/examples/k3s-demo" --poll 0
KUBECONFIG="$HOME/.kube/spikeloader-k3s.yaml" \
  ci/k8s/create-deployer.sh spikeloader-demo --name demo-deployer \
    --server https://172.17.0.1:6443 \
  | bin/spikeloader ci secret set k3s-demo KUBECONFIG_DATA
bin/spikeloader ci run k3s-demo main -f
```

The account can edit resources only in `spikeloader-demo`, not cluster-wide resources or RBAC.
The first run builds the `go`, `docker` and `kube` CI images and can take several minutes.
Image tags use the commit SHA. Deployment runs only on `main`; the protected secret is never
injected into jobs on unprotected branches, tags or detached commits.

## View

- Application: <http://demo.localhost:8081>
- Pipeline: <http://127.0.0.1:4100/ci/k3s-demo>
- Cluster: <http://127.0.0.1:4100/infra>

If your browser does not resolve `demo.localhost`, verify routing with:

```bash
curl -H 'Host: demo.localhost' http://127.0.0.1:8081/
```

## Publish a change

Edit `main.go`, then:

```bash
cd examples/k3s-demo
git add main.go
git commit -m "Update demo page"
cd ../..
bin/spikeloader ci run k3s-demo main -f
```

No push is needed: Spikeloader fetches commits from this local repository. Polling is disabled to
keep demonstrations explicit. The running page displays the deployed commit revision.

## Remove the demonstration

```bash
bin/spikeloader ci rm k3s-demo
KUBECONFIG="$HOME/.kube/spikeloader-k3s.yaml" kubectl delete namespace spikeloader-demo
```

Deleting the project removes its runs and secret; deleting the namespace removes the demo and
its deploy account. Registry image layers remain in the local test registry.
