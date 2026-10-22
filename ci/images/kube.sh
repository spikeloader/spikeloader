# Kubernetes tooling: kubectl, helm, kubeconform and kube-login (kubeconfig from the KUBECONFIG_DATA secret).
# from: base
# size: 8G
version=$(curl -fsSL https://dl.k8s.io/release/stable.txt)
curl -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/$version/bin/linux/amd64/kubectl"
chmod +x /usr/local/bin/kubectl
curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash >/dev/null
tag=$(curl -fsSL https://api.github.com/repos/yannh/kubeconform/releases/latest | jq -r .tag_name)
curl -fsSL "https://github.com/yannh/kubeconform/releases/download/$tag/kubeconform-linux-amd64.tar.gz" | tar -xz -C /usr/local/bin kubeconform

# kube-login: writes ~/.kube/config from $KUBECONFIG_DATA (plain YAML or base64) and selects a context
cat >/usr/local/bin/kube-login <<'SH'
#!/bin/bash
set -euo pipefail
: "${KUBECONFIG_DATA:?add the cluster kubeconfig as the KUBECONFIG_DATA secret and list it in the job secrets}"
install -d -m 700 ~/.kube
if printf '%s' "$KUBECONFIG_DATA" | grep -q 'apiVersion'; then
  printf '%s\n' "$KUBECONFIG_DATA" >~/.kube/config
else
  printf '%s' "$KUBECONFIG_DATA" | base64 -d >~/.kube/config
fi
chmod 600 ~/.kube/config
[ -z "${1:-}" ] || kubectl config use-context "$1" >/dev/null
echo "cluster: $(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}') · context: $(kubectl config current-context)"
kubectl version 2>/dev/null | sed 's/^/  /' || true
SH
chmod +x /usr/local/bin/kube-login

# kube-diagnose: what went wrong in a namespace (used by the deploy templates on failure)
cat >/usr/local/bin/kube-diagnose <<'SH'
#!/bin/bash
ns=${1:-default}
echo "── pods in $ns"
kubectl -n "$ns" get pods -o wide 2>&1 | head -40
echo "── recent events in $ns"
kubectl -n "$ns" get events --sort-by=.lastTimestamp 2>&1 | tail -25
for p in $(kubectl -n "$ns" get pods --no-headers 2>/dev/null | awk '$3 !~ /Running|Completed/ {print $1}' | head -3); do
  echo "── describe pod/$p"
  kubectl -n "$ns" describe pod "$p" 2>&1 | sed -n '/^Containers:/,$p' | head -40
  echo "── logs pod/$p"
  kubectl -n "$ns" logs "$p" --all-containers --tail=30 2>&1 | head -40
done
SH
chmod +x /usr/local/bin/kube-diagnose
kubectl version --client && helm version --short && kubeconform -v
