# Docker Engine + buildx + compose, started at boot (builds and pushes images inside the job).
# from: base
# size: 12G
pacman -Syu --noconfirm --needed docker docker-buildx docker-compose iptables-nft 2> >(grep -v " is up to date -- skipping" >&2) >/dev/null
mkdir -p /etc/docker
# keep Docker's networks off 172.17.0.0/16: on the host that range is where the jobs reach host services
# (k3s, registries…), and a local docker0 at 172.17.0.1 would shadow them
printf '{ "bip": "10.213.0.1/24", "default-address-pools": [{ "base": "10.214.0.0/16", "size": 24 }] }\n' >/etc/docker/daemon.json
systemctl enable docker containerd
docker --version
