# Node.js LTS (22.x) with corepack (pnpm/yarn).
# from: base
# size: 8G
url=https://nodejs.org/dist/latest-v22.x
tarball=$(curl -fsSL "$url/SHASUMS256.txt" | grep -o 'node-v[0-9.]*-linux-x64.tar.xz' | head -1)
curl -fsSL "$url/$tarball" | tar -xJ -C /usr/local --strip-components=1
corepack enable
node --version && npm --version
