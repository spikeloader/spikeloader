# Base CI image: everything else builds on top of it.
# size: 6G
pacman -Syu --noconfirm --needed \
  git base-devel pkgconf ca-certificates curl wget unzip zip xz jq rsync which \
  openssh python python-pip gnupg lsb-release file 2> >(grep -v " is up to date -- skipping" >&2) >/dev/null
git config --system --add safe.directory '*'
mkdir -p /workspace
