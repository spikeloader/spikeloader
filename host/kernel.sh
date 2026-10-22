#!/usr/bin/env bash
# Builds the microVM kernel (uncompressed vmlinux, what Firecracker boots) once, inside an Arch Linux
# container, and caches it in .lab/kernel/vmlinux-$GUEST_KERNEL. Prints the path.
#
# Config: Firecracker's guest config $GUEST_KERNEL_CONFIG (at $FC_VERSION), then `make olddefconfig`
# for the newer kernel, plus EXTRAS below. Everything is built in (=y): the rootfs has no modules.
set -euo pipefail
source "$(dirname "$0")/common.sh"

out="$STATE/kernel/vmlinux-$GUEST_KERNEL"
if [[ -f $out ]]; then
  echo "$out"
  exit 0
fi
command -v docker >/dev/null || die "Docker is needed to build the microVM kernel"

# on top of Firecracker's config: what CI jobs need that it leaves out
EXTRAS=(
  IP_NF_RAW IP6_NF_RAW            # iptables "raw" table (Docker >= 28 filters container IPs there)
  NFT_MASQ NFT_REDIR NFT_CT NFT_FIB_INET NFT_FIB_IPV4 NFT_FIB_IPV6 NF_TABLES_INET NF_TABLES_IPV6
  NETFILTER_XT_TARGET_MASQUERADE NETFILTER_XT_MATCH_IPVS NETFILTER_XT_MARK
  CGROUP_HUGETLB CGROUP_NET_PRIO BLK_CGROUP CFS_BANDWIDTH
  BTRFS_FS XFS_FS                 # mounting filesystems built by jobs
)

major=${GUEST_KERNEL%%.*}
url="https://cdn.kernel.org/pub/linux/kernel/v$major.x/linux-$GUEST_KERNEL.tar.xz"
config_url="https://raw.githubusercontent.com/firecracker-microvm/firecracker/$FC_VERSION/resources/guest_configs/$GUEST_KERNEL_CONFIG"
mkdir -p "$STATE/kernel"

log "building microVM kernel $GUEST_KERNEL (once; a few minutes)..." >&2
docker run --rm -i \
  -e "URL=$url" -e "CONFIG_URL=$config_url" -e "VERSION=$GUEST_KERNEL" -e "EXTRAS=${EXTRAS[*]}" \
  -e "OWNER=$(id -u):$(id -g)" -v "$STATE/kernel:/out" \
  archlinux:latest bash -euo pipefail >&2 <<'SH'
pacman -Syu --noconfirm --needed base-devel bc cpio pahole libelf python perl xz >/dev/null
cd /tmp
curl -fsSL "$URL" | tar -xJ
cd "linux-$VERSION"
curl -fsSL -o .config "$CONFIG_URL"
# These panic settings changed from bool to int in newer kernels.
for opt in BOOTPARAM_SOFTLOCKUP_PANIC BOOTPARAM_HUNG_TASK_PANIC; do
  if grep -A 1 "^config $opt$" lib/Kconfig.debug | grep -q '^[[:space:]]*int ' \
    && grep -qx "# CONFIG_$opt is not set" .config; then
    scripts/config --set-val "$opt" 0
  fi
done
make olddefconfig >/dev/null
for opt in $EXTRAS; do scripts/config --enable "$opt"; done
make olddefconfig >/dev/null
make -j"$(nproc)" vmlinux >/dev/null
install -m 0644 vmlinux "/out/vmlinux-$VERSION.tmp"
install -m 0644 .config "/out/vmlinux-$VERSION.config"
chown "$OWNER" "/out/vmlinux-$VERSION.tmp" "/out/vmlinux-$VERSION.config"
SH
mv "$out.tmp" "$out"
echo "$out"
