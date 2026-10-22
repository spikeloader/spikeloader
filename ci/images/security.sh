# Security scanners: Trivy (vulnerabilities, misconfig, secrets), Gitleaks (secrets), Semgrep (SAST).
# from: base
# size: 8G
curl -fsSL https://raw.githubusercontent.com/aquasecurity/trivy/main/contrib/install.sh | sh -s -- -b /usr/local/bin
tag=$(curl -fsSL https://api.github.com/repos/gitleaks/gitleaks/releases/latest | jq -r .tag_name)
curl -fsSL "https://github.com/gitleaks/gitleaks/releases/download/$tag/gitleaks_${tag#v}_linux_x64.tar.gz" | tar -xz -C /usr/local/bin gitleaks
pip install --quiet --break-system-packages semgrep
# warm the Trivy vulnerability database so jobs start fast
trivy fs --download-db-only --quiet --cache-dir /var/cache/trivy
chmod -R a+rwX /var/cache/trivy
trivy --version && gitleaks version && semgrep --version
