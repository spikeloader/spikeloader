#!/usr/bin/env bash
# Local Kubernetes for testing deploys: a k3s server and a plain-HTTP image registry, both Docker
# containers that CI jobs reach on the Docker bridge gateway (usually 172.17.0.1). Idempotent: creates
# what is missing, starts what is stopped and (re)writes the admin kubeconfig to ~/.kube/spikeloader-k3s.yaml.
#
#   host/k3s.sh           create/start registry + k3s
#   host/k3s.sh --reset   delete the cluster (workloads, certificates) and create it again
set -euo pipefail
source "$(dirname "$0")/common.sh"

K3S=spikeloader-k3s
REGISTRY=spikeloader-registry
KUBECONFIG_OUT="$HOME/.kube/spikeloader-k3s.yaml"
REGISTRIES="${XDG_CONFIG_HOME:-$HOME/.config}/spikeloader-k3s/registries.yaml"

command -v docker >/dev/null && docker info >/dev/null 2>&1 || die "Docker is not usable (is your user in the docker group?)"
gw=$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')
exists() { docker container inspect "$1" >/dev/null 2>&1; }

if [[ ${1:-} == --reset ]] && exists "$K3S"; then
  log "deleting the k3s cluster..."
  anon=$(docker inspect "$K3S" --format '{{range .Mounts}}{{if and (eq .Type "volume") (ne .Name "'"$K3S"'")}}{{.Name}} {{end}}{{end}}')
  docker rm -f "$K3S" >/dev/null
  # shellcheck disable=SC2086 # list of anonymous volumes
  docker volume rm "$K3S" $anon >/dev/null
fi

if ! exists "$REGISTRY"; then
  log "creating the registry at $gw:5000..."
  docker run -d --name "$REGISTRY" --restart unless-stopped \
    -p "127.0.0.1:5000:5000" -p "$gw:5000:5000" \
    -v "$REGISTRY:/var/lib/registry" "$REGISTRY_IMAGE" >/dev/null
else
  docker start "$REGISTRY" >/dev/null
fi

if ! exists "$K3S"; then
  log "creating the k3s cluster ($K3S_IMAGE)..."
  mkdir -p "$(dirname "$REGISTRIES")"
  cat >"$REGISTRIES" <<YAML
# local test registry (plain HTTP), also reachable by CI jobs at the same address
mirrors:
  "$gw:5000":
    endpoint: ["http://$gw:5000"]
YAML
  docker run -d --name "$K3S" --hostname "$K3S" --privileged --restart unless-stopped \
    -p "127.0.0.1:6443:6443" -p "$gw:6443:6443" -p "127.0.0.1:8081:80" \
    -v "$K3S:/var/lib/rancher/k3s" \
    -v "$REGISTRIES:/etc/rancher/k3s/registries.yaml:ro" \
    "$K3S_IMAGE" server --tls-san "$gw" --tls-san 127.0.0.1 --write-kubeconfig-mode 644 >/dev/null
else
  docker start "$K3S" >/dev/null
fi

for _ in $(seq 60); do
  docker exec "$K3S" test -s /etc/rancher/k3s/k3s.yaml 2>/dev/null && break
  sleep 2
done
mkdir -p "$(dirname "$KUBECONFIG_OUT")"
(umask 077; docker exec "$K3S" cat /etc/rancher/k3s/k3s.yaml | sed 's/: default$/: '"$K3S"'/' >"$KUBECONFIG_OUT")

log "waiting for the node..."
for _ in $(seq 90); do
  docker exec "$K3S" kubectl get nodes --no-headers 2>/dev/null | grep -q ' Ready ' && break
  sleep 2
done
docker exec "$K3S" kubectl get nodes --no-headers 2>/dev/null | grep -q ' Ready ' || die "k3s node did not become Ready (docker logs $K3S)"

log "k3s ready: API https://127.0.0.1:6443 (jobs: https://$gw:6443), registry $gw:5000"
log "kubeconfig: $KUBECONFIG_OUT"
