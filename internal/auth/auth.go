// Package auth implements the SDK's authentication strategies and OAuth token
// management. It supports three strategies:
//
//   - OAuth: OAuth 2.0 client-credentials grant. Tokens are fetched from the
//     configured token endpoint, cached in memory (and optionally on disk so they
//     survive process restarts), and refreshed shortly before expiry.
//   - Basic: HTTP Basic authentication.
//   - None: no authentication (e.g. local development).
//
// Authentication is applied to outgoing requests via an http.RoundTripper.
//
// # On-disk token cache
//
// The on-disk cache is opt-in (OAuthConfig.CacheDir, set from
// CAMUNDA_OAUTH_CACHE_DIR or WithOAuthCacheDir) and holds a bearer token, so it
// is treated as credential storage:
//
//   - The directory is created mode 0700 and each entry is written mode 0600.
//   - Entries are written to a temp file and renamed into place, so the mode is
//     reapplied on every rewrite and a concurrent reader never sees a torn file.
//   - The cache is disabled — neither read nor written — when the directory is
//     writable by group or other, because the entry path is derived from values
//     an attacker may know and a writable directory lets them both widen the
//     file's mode and substitute the token the SDK presents to the cluster.
//   - An entry found expired is removed rather than left on disk.
//
// On Windows these guarantees do not hold. Go's permission bits are largely
// ignored there: a file inherits the ACL of its parent directory, MkdirAll's
// 0700 does not restrict that ACL, and the permission bits os.Stat reports do
// not describe the effective access, so the group/other-writable check is
// skipped. The cache therefore has no operating-system-enforced protection on
// Windows and should not be enabled on a shared Windows host.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Strategy selects the authentication mechanism.
type Strategy int

// Strategies.
const (
	None Strategy = iota
	Basic
	OAuth
)

// OAuthConfig holds the OAuth 2.0 client-credentials parameters.
type OAuthConfig struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Audience     string
	Scope        string
	// CacheDir, when non-empty, persists fetched tokens to disk.
	CacheDir string
	// HTTPClient is used to fetch tokens. If nil, a client with a 30s timeout is used.
	HTTPClient *http.Client
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

type diskToken struct {
	AccessToken        string `json:"access_token"`
	RefreshAfterUnixMs int64  `json:"refresh_after_unix_ms"`
}

// TokenSource fetches and caches OAuth client-credentials tokens. It is safe for
// concurrent use; a mutex serializes refreshes so concurrent callers share a
// single in-flight fetch (single-flight).
type TokenSource struct {
	cfg   OAuthConfig
	clock Clock

	mu           sync.Mutex
	token        string
	refreshAfter time.Time
}

// Clock is the part of the SDK clock this package needs. Declared here rather than
// imported so auth stays a leaf package (see architecture_test.go); the injected
// clock satisfies it structurally.
type Clock interface {
	Now() time.Time
}

// NewTokenSource builds a token source. clock is positional rather than a field on
// OAuthConfig so the compiler rejects a caller that forgets it; token expiry is
// resolved through it.
func NewTokenSource(cfg OAuthConfig, clock Clock) *TokenSource {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &TokenSource{cfg: cfg, clock: clock}
}

func (ts *TokenSource) cachePath() string {
	sum := sha256.Sum256([]byte(ts.cfg.TokenURL + "|" + ts.cfg.ClientID + "|" + ts.cfg.Audience))
	return filepath.Join(ts.cfg.CacheDir, "camunda-oauth-"+hex.EncodeToString(sum[:8])+".json")
}

// Token returns a valid access token, fetching or refreshing as needed.
func (ts *TokenSource) Token(ctx context.Context) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	now := ts.clock.Now()
	if ts.token != "" && now.Before(ts.refreshAfter) {
		return ts.token, nil
	}

	// Try the on-disk cache (shared across processes) before hitting the network.
	if ts.cfg.CacheDir != "" {
		if tok, refreshAfter, ok := ts.readDisk(); ok {
			if now.Before(refreshAfter) {
				ts.token, ts.refreshAfter = tok, refreshAfter
				return tok, nil
			}
			// Expired. Nothing used to remove it, so a stale bearer token stayed
			// readable on disk indefinitely — surviving the process and any
			// credential rotation. Drop it now rather than waiting for a
			// successful refresh, which may never come.
			ts.removeDisk()
		}
	}

	tok, refreshAfter, err := ts.fetch(ctx, now)
	if err != nil {
		return "", err
	}
	ts.token, ts.refreshAfter = tok, refreshAfter
	if ts.cfg.CacheDir != "" {
		ts.writeDisk(tok, refreshAfter)
	}
	return tok, nil
}

func (ts *TokenSource) fetch(ctx context.Context, now time.Time) (string, time.Time, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", ts.cfg.ClientID)
	form.Set("client_secret", ts.cfg.ClientSecret)
	if ts.cfg.Audience != "" {
		form.Set("audience", ts.cfg.Audience)
	}
	if ts.cfg.Scope != "" {
		form.Set("scope", ts.cfg.Scope)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := ts.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", time.Time{}, fmt.Errorf("token endpoint returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", time.Time{}, fmt.Errorf("decoding token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("token endpoint returned an empty access_token")
	}

	lifetime := tr.ExpiresIn
	if lifetime <= 0 {
		lifetime = 60 // conservative default when the endpoint omits expires_in
	}
	// Refresh a little before expiry (90% of the lifetime).
	refreshAfter := now.Add(time.Duration(float64(lifetime)*0.9) * time.Second)
	return tr.AccessToken, refreshAfter, nil
}

// cacheDirUsable reports whether the cache directory is safe to hold a bearer
// token: it must exist, be a directory, and not be writable by group or other.
//
// MkdirAll's 0700 only applies to a directory it creates; on a directory that
// already exists it is a no-op, so a pre-existing world-writable CacheDir kept
// its mode. The cache path is a hash of the token URL, client id and audience,
// all of which an attacker may know, so write access to the directory is enough
// both to pre-create the file at a wide mode and to substitute the token the SDK
// then presents to the cluster. Neither reading nor writing is worth doing there.
//
// On Windows the permission bits Go reports do not describe the ACL that
// actually governs access, so the check would be meaningless; see the package
// documentation for what the cache does and does not guarantee there.
func cacheDirUsable(dir string) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return false
	}
	return fi.Mode().Perm()&0o022 == 0
}

func (ts *TokenSource) readDisk() (string, time.Time, bool) {
	if !cacheDirUsable(ts.cfg.CacheDir) {
		return "", time.Time{}, false
	}
	data, err := os.ReadFile(ts.cachePath())
	if err != nil {
		return "", time.Time{}, false
	}
	var dt diskToken
	if err := json.Unmarshal(data, &dt); err != nil || dt.AccessToken == "" {
		return "", time.Time{}, false
	}
	return dt.AccessToken, time.UnixMilli(dt.RefreshAfterUnixMs), true
}

// removeDisk drops the cached entry. Best-effort, like the rest of the cache.
func (ts *TokenSource) removeDisk() {
	if !cacheDirUsable(ts.cfg.CacheDir) {
		return
	}
	_ = os.Remove(ts.cachePath())
}

// writeDisk persists the token for other processes sharing CacheDir.
//
// The write goes to a temp file in the same directory which is then renamed into
// place. The rename is atomic, so a concurrent reader sees either the old entry
// or the new one and never a truncated file, and it installs the temp file's own
// 0600 mode — where os.WriteFile applied its perm only when creating the file,
// leaving an existing cache file at whatever mode it already had.
func (ts *TokenSource) writeDisk(token string, refreshAfter time.Time) {
	if err := os.MkdirAll(ts.cfg.CacheDir, 0o700); err != nil {
		return
	}
	if !cacheDirUsable(ts.cfg.CacheDir) {
		return
	}
	data, err := json.Marshal(diskToken{AccessToken: token, RefreshAfterUnixMs: refreshAfter.UnixMilli()})
	if err != nil {
		return
	}

	// Best-effort: token caching is an optimization, not a correctness
	// requirement, so every failure below simply leaves the cache as it was.
	tmp, err := os.CreateTemp(ts.cfg.CacheDir, ".camunda-oauth-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	// CreateTemp already uses 0600; set it explicitly so the guarantee rests on
	// this code rather than on a standard-library default.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, ts.cachePath()); err != nil {
		_ = os.Remove(tmpName)
	}
}

// Transport applies authentication to outgoing requests.
type Transport struct {
	// Base is the underlying RoundTripper. If nil, http.DefaultTransport is used.
	Base http.RoundTripper
	// Strategy selects the auth mechanism.
	Strategy Strategy
	// BasicUsername/BasicPassword are used when Strategy == Basic.
	BasicUsername string
	BasicPassword string
	// TokenSource supplies bearer tokens when Strategy == OAuth.
	TokenSource *TokenSource
}

func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

// RoundTrip implements http.RoundTripper. It never mutates the caller's request.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	switch t.Strategy {
	case Basic:
		r.SetBasicAuth(t.BasicUsername, t.BasicPassword)
	case OAuth:
		if t.TokenSource == nil {
			return nil, fmt.Errorf("oauth strategy selected but no token source configured")
		}
		tok, err := t.TokenSource.Token(req.Context())
		if err != nil {
			return nil, err
		}
		r.Header.Set("Authorization", "Bearer "+tok)
	case None:
		// no-op
	}
	return t.base().RoundTrip(r)
}
