# Python 3 with uv, pipx and build headers.
# from: base
# size: 8G
pacman -Syu --noconfirm --needed python python-pipx 2> >(grep -v " is up to date -- skipping" >&2) >/dev/null
curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin UV_NO_MODIFY_PATH=1 sh
python3 --version && uv --version
