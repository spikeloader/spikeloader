# Latest stable Go.
# from: base
# size: 8G
version=$(curl -fsSL 'https://go.dev/VERSION?m=text' | head -1)
curl -fsSL "https://go.dev/dl/$version.linux-amd64.tar.gz" | tar -xz -C /usr/local
ln -sf /usr/local/go/bin/go /usr/local/go/bin/gofmt /usr/local/bin/
go version
