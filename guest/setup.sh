#!/usr/bin/env bash
# Provisions the outer VM (Arch Linux): Firecracker, microVM kernel/rootfs, networking and NAT.
# Idempotent: safe to run again.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
source "$HERE/.env"

LAB=/var/lib/fclab
mkdir -p "$LAB/vms" "$LAB/images"
log() { printf '\033[1;32m[outer]\033[0m %s\n' "$*"; }

[[ -c /dev/kvm ]] || { echo "/dev/kvm missing in the outer VM: nested KVM is not enabled" >&2; exit 1; }

# first boot: cloud-init grows the disk and pacman-init sets up the keyring
cloud-init status --wait >/dev/null 2>&1 || true

log "installing packages..."
pkgs=(iproute2 e2fsprogs curl jq ethtool arch-install-scripts)
# the image may ship iptables or iptables-nft; either works, they conflict with each other
command -v iptables >/dev/null || pkgs+=(iptables-nft)
pacman -Syu --noconfirm --needed "${pkgs[@]}" 2> >(grep -v "is up to date -- skipping" >&2) >/dev/null
# an upgraded kernel removes the running kernel's modules (netfilter included): reboot into it and
# let host/up.sh run this script again (exit code 100)
if [[ ! -d /usr/lib/modules/$(uname -r) ]]; then
  log "kernel upgraded to $(pacman -Q linux | cut -d' ' -f2); rebooting the outer VM..."
  systemctl reboot --no-block
  exit 100
fi

if ! firecracker --version 2>/dev/null | grep -q "${FC_VERSION#v}"; then
  log "installing Firecracker $FC_VERSION..."
  tmp=$(mktemp -d)
  curl -fsSL "https://github.com/firecracker-microvm/firecracker/releases/download/$FC_VERSION/firecracker-$FC_VERSION-x86_64.tgz" \
    | tar -xz -C "$tmp"
  install -m 0755 "$tmp/release-$FC_VERSION-x86_64/firecracker-$FC_VERSION-x86_64" /usr/local/bin/firecracker
  install -m 0755 "$tmp/release-$FC_VERSION-x86_64/jailer-$FC_VERSION-x86_64" /usr/local/bin/jailer
  rm -rf "$tmp"
fi
install -m 0755 "$HERE/fcctl" /usr/local/bin/fcctl
install -m 0644 "$HERE/.env" "$LAB/.env"
rm -f "$LAB/lab.env"

agent_changed=0
if ! cmp -s "$HERE/fcagent" /usr/local/bin/fcagent; then
  install -m 0755 "$HERE/fcagent" /usr/local/bin/fcagent.new
  mv -f /usr/local/bin/fcagent.new /usr/local/bin/fcagent
  agent_changed=1
fi
if ! cmp -s "$HERE/fcagent.service" /etc/systemd/system/fcagent.service; then
  install -m 0644 "$HERE/fcagent.service" /etc/systemd/system/fcagent.service
  systemctl daemon-reload
  agent_changed=1
fi
systemctl enable fcagent.service
if ((agent_changed)) || ! systemctl is-active --quiet fcagent.service; then
  systemctl restart fcagent.service
fi
# Fail provisioning if the agent did not become responsive.
for attempt in $(seq 30); do
  if curl -fsS --unix-socket /run/fcagent.sock http://localhost/images >/dev/null; then
    break
  fi
  ((attempt < 30)) || { echo "fcagent did not become ready (journalctl -u fcagent)" >&2; exit 1; }
  sleep 1
done

# the kernel is built on the host (host/kernel.sh) and uploaded with this script
if ! cmp -s "$HERE/vmlinux" "$LAB/vmlinux"; then
  log "installing microVM kernel $GUEST_KERNEL..."
  install -m 0644 "$HERE/vmlinux" "$LAB/vmlinux"
fi

[[ -f $LAB/id_ed25519 ]] || ssh-keygen -q -t ed25519 -N '' -C fclab-microvm -f "$LAB/id_ed25519"

# rebuilt when its inputs change; CI images derive from it, so they go too
stamp="$GUEST_ROOTFS|$GUEST_PACKAGES|$ROOTFS_SIZE|$(sha256sum "$HERE/fcnet-setup.sh" | cut -c1-16)"
if [[ ! -f $LAB/base-rootfs.ext4 || $(cat "$LAB/base-rootfs.stamp" 2>/dev/null) != "$stamp" ]]; then
  log "building the base rootfs (Arch Linux: $GUEST_PACKAGES)..."
  rm -f "$LAB"/images/*.ext4
  tmp=$(mktemp -d)
  root=$tmp/root
  mkdir "$root"
  # shellcheck disable=SC2086 # GUEST_PACKAGES is a list
  pacstrap -c -K "$root" $GUEST_PACKAGES >/dev/null
  # root access via SSH key; host keys made now so every microVM boots without generating them
  install -d -m 0700 "$root/root/.ssh"
  install -m 0600 "$LAB/id_ed25519.pub" "$root/root/.ssh/authorized_keys"
  ssh-keygen -q -A -f "$root"
  # networking: IP/gateway derived from the MAC + hostname via cmdline
  install -m 0755 "$HERE/fcnet-setup.sh" "$root/usr/local/bin/fcnet-setup.sh"
  cat >"$root/etc/systemd/system/fcnet.service" <<'EOF'
[Unit]
Description=microVM network (IP from the MAC address)
Before=network.target sshd.service
Wants=network.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/fcnet-setup.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF
  systemctl --root="$root" enable fcnet.service sshd.service >/dev/null 2>&1
  # no interactive first-boot prompts on the serial console
  systemctl --root="$root" mask systemd-firstboot.service >/dev/null 2>&1
  rm -f "$root/etc/resolv.conf"
  printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' >"$root/etc/resolv.conf"
  echo LANG=C.UTF-8 >"$root/etc/locale.conf"
  truncate -s "$ROOTFS_SIZE" "$tmp/rootfs.ext4"
  mkfs.ext4 -q -F -d "$root" "$tmp/rootfs.ext4"
  mv "$tmp/rootfs.ext4" "$LAB/base-rootfs.ext4"
  echo "$stamp" >"$LAB/base-rootfs.stamp"
  rm -rf "$tmp"
fi

if [[ $(</proc/sys/kernel/hostname) != spikeloader-host ]]; then
  log "hostname: spikeloader-host"
  # keep cloud-init from restoring the old hostname at boot
  echo "preserve_hostname: true" >/etc/cloud/cloud.cfg.d/99-spikeloader.cfg
  hostnamectl set-hostname spikeloader-host
  grep -q 'spikeloader-host' /etc/hosts || echo "127.0.1.1 spikeloader-host" >>/etc/hosts
fi

# CI jobs and the panel share one multiplexed SSH connection: allow many sessions on it
if ! grep -q '^MaxSessions 64' /etc/ssh/sshd_config /etc/ssh/sshd_config.d/*.conf 2>/dev/null; then
  if grep -q '^Include /etc/ssh/sshd_config.d/' /etc/ssh/sshd_config; then
    echo "MaxSessions 64" >/etc/ssh/sshd_config.d/90-spikeloader.conf
  else
    echo "MaxSessions 64" >>/etc/ssh/sshd_config
  fi
  systemctl reload sshd
fi

log "configuring routing and NAT..."
cat >/etc/sysctl.d/90-fclab.conf <<EOF
net.ipv4.ip_forward = 1
EOF
sysctl -q -p /etc/sysctl.d/90-fclab.conf
wan=$(ip -o route show default | awk '{print $5; exit}')
iptables -t nat -C POSTROUTING -s 172.16.0.0/16 -o "$wan" -j MASQUERADE 2>/dev/null \
  || iptables -t nat -A POSTROUTING -s 172.16.0.0/16 -o "$wan" -j MASQUERADE
iptables -C FORWARD -s 172.16.0.0/16 -j ACCEPT 2>/dev/null || iptables -A FORWARD -s 172.16.0.0/16 -j ACCEPT
iptables -C FORWARD -d 172.16.0.0/16 -j ACCEPT 2>/dev/null || iptables -A FORWARD -d 172.16.0.0/16 -j ACCEPT

log "isolating the microVMs..."
# inserted before the ACCEPTs above: one job cannot reach another job's microVM, nor the QEMU
# user-network addresses (10.0.2.2 is the host's own 127.0.0.1 with the native backend, 10.0.2.3 its DNS)
for rule in "-s 172.16.0.0/16 -d 172.16.0.0/16 -j DROP" "-s 172.16.0.0/16 -d 10.0.2.0/24 -j REJECT"; do
  # shellcheck disable=SC2086 # the rule is a word list
  iptables -C FORWARD $rule 2>/dev/null || iptables -I FORWARD 1 $rule
done
# nor any service of the outer VM itself (sshd…): only replies to connections the VM opened get in
for rule in "-s 172.16.0.0/16 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT" "-s 172.16.0.0/16 -j DROP"; do
  # shellcheck disable=SC2086
  iptables -C INPUT $rule 2>/dev/null || iptables -A INPUT $rule
done

log "ok: $(firecracker --version | head -1), kernel $GUEST_KERNEL, rootfs $GUEST_ROOTFS"
