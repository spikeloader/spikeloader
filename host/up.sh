#!/usr/bin/env bash
# Starts the outer VM (Arch Linux + nested KVM) and provisions Firecracker inside it.
set -euo pipefail
source "$(dirname "$0")/common.sh"

[[ -r /dev/kvm && -w /dev/kvm ]] || die "/dev/kvm not accessible (add your user to the kvm group)."
nested=$(cat /sys/module/kvm_intel/parameters/nested /sys/module/kvm_amd/parameters/nested 2>/dev/null || true)
[[ $nested == *Y* || $nested == *1* ]] || die "Nested virtualization is disabled on the host (kvm_intel/kvm_amd nested=1)."

resolve_backend
log "backend: $BACKEND"

# the microVM kernel is built on the host once (cached in .lab/kernel/)
kernel=$("$ROOT/host/kernel.sh")
agent=$(bash "$ROOT/host/agent.sh")

if outer_running; then
  log "outer VM is already running."
else
  if [[ $BACKEND == docker ]]; then
    log "building image $IMAGE..."
    docker build -q -t "$IMAGE" -f "$ROOT/host/qemu.Dockerfile" "$ROOT/host" >/dev/null
  fi

  [[ -f $STATE/id_ed25519 ]] || ssh-keygen -q -t ed25519 -N '' -C fclab -f "$STATE/id_ed25519"

  # a different OUTER_IMG_URL means a new base image, and the disk built on the old one goes with it
  if [[ ! -f $STATE/base.img || $(cat "$STATE/base.img.url" 2>/dev/null) != "$OUTER_IMG_URL" ]]; then
    log "downloading the Arch Linux cloud image..."
    rm -f "$STATE/disk.qcow2"
    curl -fL --progress-bar -o "$STATE/base.img.part" "$OUTER_IMG_URL"
    mv "$STATE/base.img.part" "$STATE/base.img"
    echo "$OUTER_IMG_URL" >"$STATE/base.img.url"
  fi

  if [[ ! -f $STATE/disk.qcow2 ]]; then
    # a new disk gets a new SSH host key
    rm -f "$STATE/known_hosts"
    tool qemu-img create -q -f qcow2 -F qcow2 -b "$STATE/base.img" "$STATE/disk.qcow2" "$OUTER_DISK"
  fi

  cat >"$STATE/user-data" <<EOF
#cloud-config
hostname: spikeloader-host
users:
  - name: lab
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - $(cat "$STATE/id_ed25519.pub")
ssh_pwauth: false
EOF
  tool cloud-localds "$STATE/seed.iso" "$STATE/user-data"

  qemu_args=(-enable-kvm -machine q35 -cpu host -smp "$OUTER_CPUS" -m "$OUTER_MEM"
             -drive "if=virtio,file=$STATE/disk.qcow2,format=qcow2"
             -drive "if=virtio,file=$STATE/seed.iso,format=raw,readonly=on"
             -device virtio-net-pci,netdev=n0
             -display none -serial "file:$STATE/console.log")

  log "starting outer VM ($OUTER_CPUS vCPU, $OUTER_MEM RAM)..."
  if [[ $BACKEND == docker ]]; then
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    docker run -d --name "$CONTAINER" --user "$(id -u):$(id -g)" \
      --device /dev/kvm --group-add "$(stat -c %g /dev/kvm)" \
      -v "$STATE:$STATE" -p "127.0.0.1:$SSH_PORT:2222" "$IMAGE" \
      qemu-system-x86_64 "${qemu_args[@]}" \
      -netdev "user,id=n0,hostfwd=tcp:0.0.0.0:2222-:22" >/dev/null
  else
    qemu-system-x86_64 "${qemu_args[@]}" \
      -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$SSH_PORT-:22" \
      -daemonize -pidfile "$STATE/qemu.pid"
  fi
  echo "$BACKEND" >"$STATE/backend"
fi

wait_ssh() {
  for _ in $(seq 120); do
    outer_ssh true 2>/dev/null && return
    sleep 3
  done
  die "SSH did not respond; see $STATE/console.log"
}

provision() {
  outer_ssh 'rm -rf /tmp/fclab && mkdir -p /tmp/fclab'
  scp -q "${KEY_OPTS[@]}" -P "$SSH_PORT" "$ROOT"/guest/* "$ROOT/.env" lab@127.0.0.1:/tmp/fclab/
  scp -q "${KEY_OPTS[@]}" -P "$SSH_PORT" "$kernel" lab@127.0.0.1:/tmp/fclab/vmlinux
  scp -q "${KEY_OPTS[@]}" -P "$SSH_PORT" "$agent" lab@127.0.0.1:/tmp/fclab/fcagent
  outer_ssh 'sudo bash /tmp/fclab/setup.sh'
}

log "waiting for outer VM SSH..."
wait_ssh

log "provisioning Firecracker inside the outer VM..."
# exit code 100: the package upgrade brought a new kernel and the VM is rebooting into it
status=0
provision || status=$?
if ((status == 100)); then
  sleep 10
  wait_ssh
  provision
elif ((status)); then
  exit "$status"
fi

log "outer VM ready."
