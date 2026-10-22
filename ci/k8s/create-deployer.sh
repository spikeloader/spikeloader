#!/usr/bin/env bash
# Creates a deploy account for spikeloader CI in a Kubernetes cluster and prints its kubeconfig.
# Run it once on a machine whose kubectl is admin on the cluster:
#
#   ci/k8s/create-deployer.sh my-app                     # namespace my-app (created if missing)
#   ci/k8s/create-deployer.sh my-app staging production  # one account, several namespaces
#   ci/k8s/create-deployer.sh my-app --server https://k8s.example.com:6443   # API URL the jobs use
#
# then store it as a secret:  ci/k8s/create-deployer.sh my-app | spikeloader ci secret set <project> KUBECONFIG_DATA
#
# The account can only manage resources inside those namespaces (ClusterRole "edit" bound per
# namespace): no cluster-wide access, no RBAC changes. Revoke it by deleting the ServiceAccount.
set -euo pipefail

server="" name=spikeloader-deployer namespaces=()
while (($#)); do
  case $1 in
    --server) server=$2; shift 2 ;;
    --name) name=$2; shift 2 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) namespaces+=("$1"); shift ;;
  esac
done
((${#namespaces[@]})) || { echo "usage: $0 <namespace>... [--server URL] [--name NAME]" >&2; exit 1; }
home=${namespaces[0]}

for ns in "${namespaces[@]}"; do
  kubectl get namespace "$ns" >/dev/null 2>&1 || kubectl create namespace "$ns" >&2
done

kubectl apply -f - >&2 <<YAML
apiVersion: v1
kind: ServiceAccount
metadata: { name: $name, namespace: $home, labels: { app.kubernetes.io/managed-by: spikeloader } }
---
apiVersion: v1
kind: Secret
type: kubernetes.io/service-account-token
metadata:
  name: $name-token
  namespace: $home
  annotations: { kubernetes.io/service-account.name: $name }
YAML

for ns in "${namespaces[@]}"; do
  kubectl apply -f - >&2 <<YAML
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: { name: $name, namespace: $ns, labels: { app.kubernetes.io/managed-by: spikeloader } }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: edit }
subjects: [{ kind: ServiceAccount, name: $name, namespace: $home }]
YAML
done

# the token controller fills the secret asynchronously
for _ in $(seq 30); do
  token=$(kubectl -n "$home" get secret "$name-token" -o jsonpath='{.data.token}' 2>/dev/null | base64 -d)
  [[ -n $token ]] && break
  sleep 1
done
[[ -n ${token:-} ]] || { echo "the token was not issued (is the token controller running?)" >&2; exit 1; }

ca=$(kubectl -n "$home" get secret "$name-token" -o jsonpath='{.data.ca\.crt}')
[[ -n $server ]] || server=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
cluster=$(kubectl config view --minify -o jsonpath='{.clusters[0].name}')

cat <<YAML
apiVersion: v1
kind: Config
clusters:
- name: $cluster
  cluster: { server: $server, certificate-authority-data: $ca }
users:
- name: $name
  user: { token: $token }
contexts:
- name: $name@$cluster
  context: { cluster: $cluster, user: $name, namespace: $home }
current-context: $name@$cluster
YAML
echo "✓ $name can deploy to: ${namespaces[*]} (API $server)" >&2
