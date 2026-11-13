package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// skipIfNoUnixPerms skips a test whose assertion is about POSIX permission bits.
// Go's perm argument is largely ignored on Windows (the file inherits the parent
// directory's ACL), so these assertions are meaningless there.
func skipIfNoUnixPerms(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
}

// cachingEndpoint is a stub token endpoint shared by every TokenSource in a test,
// so each source derives the same cache path (which is a hash of the token URL,
// client id and audience) and they really do contend for one cache file.
type cachingEndpoint struct {
	url   string
	calls *int32
}

func newCachingEndpoint(t *testing.T) *cachingEndpoint {
	t.Helper()
	var calls int32
	srv := tokenServer(t, &calls)
	t.Cleanup(srv.Close)
	return &cachingEndpoint{url: srv.URL, calls: &calls}
}

func (e *cachingEndpoint) source(dir string, clock Clock) *TokenSource {
	return NewTokenSource(OAuthConfig{
		TokenURL:     e.url,
		ClientID:     "id",
		ClientSecret: "sec",
		CacheDir:     dir,
	}, clock)
}

// TestDiskCacheEnforcesOwnerOnlyModeOnRewrite is the reported defect: os.WriteFile
// applies its perm argument only when it creates the file, so a cache file that
// already exists keeps whatever mode it had. The cache path is derived from
// values an attacker can predict, so pre-creating it at a wide mode was enough to
// keep a bearer token readable.
//
// The subtests cover the class — every route by which the file can come to exist
// — not only the pre-created case.
func TestDiskCacheEnforcesOwnerOnlyModeOnRewrite(t *testing.T) {
	skipIfNoUnixPerms(t)

	tests := []struct {
		name string
		// prepare runs before the first token fetch and may pre-create the cache
		// file at a deliberately wide mode.
		prepare func(t *testing.T, path string)
		// writes is how many times a token is written to disk.
		writes int
	}{
		{
			name:    "fresh file",
			prepare: func(*testing.T, string) {},
			writes:  1,
		},
		{
			name: "pre-created world-readable",
			prepare: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
					t.Fatalf("pre-create: %v", err)
				}
			},
			writes: 1,
		},
		{
			name: "pre-created world-writable",
			prepare: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("{}"), 0o666); err != nil {
					t.Fatalf("pre-create: %v", err)
				}
			},
			writes: 1,
		},
		{
			name:    "rewritten after expiry",
			prepare: func(*testing.T, string) {},
			writes:  2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			start := time.Unix(1_000_000, 0)
			clk := &settableClock{t: start}
			ts := newCachingEndpoint(t).source(dir, clk)
			tc.prepare(t, ts.cachePath())

			for i := range tc.writes {
				if _, err := ts.Token(context.Background()); err != nil {
					t.Fatalf("Token: %v", err)
				}
				// Advance past the refresh window so the next call refetches and
				// rewrites the cache file.
				clk.set(start.Add(time.Duration(i+1) * 4000 * time.Second))
			}

			fi, err := os.Stat(ts.cachePath())
			if err != nil {
				t.Fatalf("stat cache file: %v", err)
			}
			if got := fi.Mode().Perm(); got != 0o600 {
				t.Errorf("cache file mode = %04o, want 0600", got)
			}
		})
	}
}

// TestDiskCacheWriteIsAtomic asserts a reader never observes a partially written
// cache file. The old implementation truncated in place, so a concurrent reader
// could decode a short read; the replacement writes a temp file and renames it.
func TestDiskCacheWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	start := time.Unix(1_000_000, 0)
	endpoint := newCachingEndpoint(t)

	// Prime the cache so there is always a file to read.
	primer := endpoint.source(dir, &settableClock{t: start})
	if _, err := primer.Token(context.Background()); err != nil {
		t.Fatalf("prime: %v", err)
	}
	path := primer.cachePath()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writers: repeatedly rewrite the one cache file all sources share.
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ts := endpoint.source(dir, &settableClock{t: start})
			for i := range 50 {
				select {
				case <-stop:
					return
				default:
				}
				ts.writeDisk(fmt.Sprintf("token-%d-%d", w, i), start.Add(time.Hour))
			}
		}()
	}

	// Readers: every file they manage to read must be complete and decodable.
	var bad atomic.Int32
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				select {
				case <-stop:
					return
				default:
				}
				data, err := os.ReadFile(path)
				if err != nil {
					continue // a rename window is fine; a torn read is not
				}
				var dt diskToken
				if json.Unmarshal(data, &dt) != nil || dt.AccessToken == "" {
					bad.Add(1)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(stop)

	if n := bad.Load(); n != 0 {
		t.Errorf("observed %d torn or undecodable reads of the cache file", n)
	}
}

// TestDiskCacheLeavesNoTemporaryFiles guards the replacement's own failure mode:
// a write-via-rename that forgets to clean up would litter the cache directory
// with token-bearing temp files.
func TestDiskCacheLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	start := time.Unix(1_000_000, 0)
	clk := &settableClock{t: start}
	ts := newCachingEndpoint(t).source(dir, clk)

	for i := range 3 {
		if _, err := ts.Token(context.Background()); err != nil {
			t.Fatalf("Token: %v", err)
		}
		clk.set(start.Add(time.Duration(i+1) * 4000 * time.Second))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("cache dir holds %d entries (%s), want exactly the cache file",
			len(entries), strings.Join(names, ", "))
	}
}

// TestDiskCacheCreatesOwnerOnlyDirectory covers the directory the cache lives in,
// not just the file.
func TestDiskCacheCreatesOwnerOnlyDirectory(t *testing.T) {
	skipIfNoUnixPerms(t)

	dir := filepath.Join(t.TempDir(), "nested", "cache")
	ts := newCachingEndpoint(t).source(dir, &settableClock{t: time.Unix(1_000_000, 0)})
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("cache dir mode = %04o, want 0700", got)
	}
}

// TestDiskCacheSkippedForUnsafeDirectory covers the hole MkdirAll leaves: it is a
// no-op on a directory that already exists, so a pre-existing group- or
// world-writable cache directory kept its mode. Anyone able to write there can
// swap the token the SDK then presents to the cluster, so the cache is disabled
// rather than trusted.
func TestDiskCacheSkippedForUnsafeDirectory(t *testing.T) {
	skipIfNoUnixPerms(t)

	for _, mode := range []os.FileMode{0o777, 0o770, 0o707, 0o722} {
		t.Run(fmt.Sprintf("mode_%04o", mode), func(t *testing.T) {
			dir := t.TempDir()
			endpoint := newCachingEndpoint(t)
			start := time.Unix(1_000_000, 0)
			ts := endpoint.source(dir, &settableClock{t: start})

			// Plant a valid-looking entry an attacker with write access could have
			// left, before widening the directory.
			planted, err := json.Marshal(diskToken{
				AccessToken:        "attacker-supplied-token",
				RefreshAfterUnixMs: start.Add(time.Hour).UnixMilli(),
			})
			if err != nil {
				t.Fatalf("marshal planted entry: %v", err)
			}
			if err := os.WriteFile(ts.cachePath(), planted, 0o600); err != nil {
				t.Fatalf("plant entry: %v", err)
			}
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}

			tok, err := ts.Token(context.Background())
			if err != nil {
				t.Fatalf("Token: %v", err)
			}
			// Read side: a token from a directory anyone can write to is not
			// trusted, so the endpoint is consulted instead.
			if tok == "attacker-supplied-token" {
				t.Errorf("used a token cached in a %04o directory", mode)
			}
			if *endpoint.calls != 1 {
				t.Errorf("made %d token fetches, want 1 (the planted entry must be ignored)", *endpoint.calls)
			}
			// Write side: the freshly fetched token is not written back there.
			data, err := os.ReadFile(ts.cachePath())
			if err != nil {
				t.Fatalf("read cache path: %v", err)
			}
			if !bytes.Equal(data, planted) {
				t.Errorf("token written to a %04o directory: %s", mode, data)
			}
		})
	}
}

// TestDiskCacheStillWorksForSafeDirectory is the companion: the hardening must
// not disable the cache for the ordinary 0700 case.
func TestDiskCacheStillWorksForSafeDirectory(t *testing.T) {
	dir := t.TempDir()
	endpoint := newCachingEndpoint(t)
	start := time.Unix(1_000_000, 0)
	ts := endpoint.source(dir, &settableClock{t: start})
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if _, err := os.Stat(ts.cachePath()); err != nil {
		t.Fatalf("expected a cache file in a 0700 directory: %v", err)
	}
	// And a restarted process still reuses it.
	fresh := endpoint.source(dir, &settableClock{t: start})
	if _, err := fresh.Token(context.Background()); err != nil {
		t.Fatalf("fresh Token: %v", err)
	}
	if *endpoint.calls != 1 {
		t.Errorf("made %d token fetches, want 1 (the disk cache must still be used)", *endpoint.calls)
	}
}

// TestDiskCacheRemovesExpiredEntry covers the "no cleanup" half of the issue: an
// expired entry was returned, ignored, and then left on disk indefinitely — a
// stale bearer token outliving the process and any credential rotation.
func TestDiskCacheRemovesExpiredEntry(t *testing.T) {
	dir := t.TempDir()
	start := time.Unix(1_000_000, 0)
	endpoint := newCachingEndpoint(t)
	ts := endpoint.source(dir, &settableClock{t: start})

	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	path := ts.cachePath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected a cache file: %v", err)
	}

	// A fresh source (a restarted process) reading a now-expired entry must not
	// leave it behind. Its own fetch is made to fail, so the only thing that can
	// remove the file is the expiry cleanup rather than a successful rewrite.
	fresh := endpoint.source(dir, &settableClock{t: start.Add(48 * time.Hour)})
	fresh.cfg.HTTPClient = &http.Client{Transport: failingRoundTripper{}}

	if _, err := fresh.Token(context.Background()); err == nil {
		t.Fatal("expected the failing token request to return an error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expired cache entry survived; stat err = %v", err)
	}
}

// failingRoundTripper fails every token request, so a test can observe what the
// SDK does with the on-disk cache when it cannot refresh.
type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("token endpoint unreachable")
}

// TestDiskCacheKeepsValidEntry is the companion guard: cleanup must remove only
// entries that are actually expired.
func TestDiskCacheKeepsValidEntry(t *testing.T) {
	dir := t.TempDir()
	start := time.Unix(1_000_000, 0)
	endpoint := newCachingEndpoint(t)
	ts := endpoint.source(dir, &settableClock{t: start})
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	fresh := endpoint.source(dir, &settableClock{t: start})
	if _, err := fresh.Token(context.Background()); err != nil {
		t.Fatalf("fresh Token: %v", err)
	}
	if _, err := os.Stat(ts.cachePath()); err != nil {
		t.Errorf("valid cache entry was removed: %v", err)
	}
}
