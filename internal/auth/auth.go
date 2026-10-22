// Package auth implements the panel's token sign-in.
//
// The token lives in .lab/web-token (0600) and is generated on first use. A session is a cookie
// holding HMAC-SHA256(token, "spikeloader-session-v1"): no server-side state, and rotating the token
// (`spikeloader web token --rotate`) ends every session. The CLI sends the token as a bearer.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"spikeloader/internal/env"
)

const (
	Cookie        = "spikeloader_session"
	SessionMaxAge = 30 * 24 * 3600
)

var tokenFile = filepath.Join(env.State, "web-token")

// Enabled is false only with SPIKELOADER_AUTH=off (refused off loopback by the daemon).
func Enabled() bool { return os.Getenv("SPIKELOADER_AUTH") != "off" }

// Token returns the access token, creating it on first use.
func Token() (string, error) {
	if t := os.Getenv("SPIKELOADER_TOKEN"); t != "" {
		return t, nil
	}
	b, err := os.ReadFile(tokenFile)
	if err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	t := base64.RawURLEncoding.EncodeToString(raw)
	if err := os.MkdirAll(env.State, 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(tokenFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		// another process created it first
		b, rerr := os.ReadFile(tokenFile)
		if rerr != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	defer f.Close()
	_, err = f.WriteString(t + "\n")
	return t, err
}

// SessionValue is the cookie value for the current token.
func SessionValue() string {
	t, _ := Token()
	m := hmac.New(sha256.New, []byte(t))
	m.Write([]byte("spikeloader-session-v1"))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// equal compares in constant time (hashing first hides the length).
func equal(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// CheckToken validates a token typed at sign-in or sent by the CLI.
func CheckToken(input string) bool {
	t, err := Token()
	return err == nil && input != "" && equal(strings.TrimSpace(input), t)
}

var bearer = regexp.MustCompile(`(?i)^Bearer\s+(\S+)$`)

// Authorized reports whether the request carries a valid session cookie or bearer token.
func Authorized(r *http.Request) bool {
	if !Enabled() {
		return true
	}
	if c, err := r.Cookie(Cookie); err == nil && c.Value != "" && equal(c.Value, SessionValue()) {
		return true
	}
	if m := bearer.FindStringSubmatch(r.Header.Get("Authorization")); m != nil && CheckToken(m[1]) {
		return true
	}
	return false
}
