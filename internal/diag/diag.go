// Package diag provides the SDK's leveled logging and support/diagnostic
// facilities: structured trace logging and an environment/config snapshot for
// technical-support scenarios. It is a leaf package with no dependencies on
// other SDK packages.
package diag

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Level controls the verbosity of SDK logging.
type Level int

// Log levels, from least to most verbose.
const (
	LevelOff Level = iota
	LevelError
	LevelWarn
	LevelInfo
	LevelDebug
	LevelTrace
)

func (l Level) String() string {
	switch l {
	case LevelOff:
		return "off"
	case LevelError:
		return "error"
	case LevelWarn:
		return "warn"
	case LevelInfo:
		return "info"
	case LevelDebug:
		return "debug"
	case LevelTrace:
		return "trace"
	default:
		return "info"
	}
}

// ParseLevel parses a CAMUNDA_SDK_LOG_LEVEL value. An empty string maps to
// LevelInfo. It returns an error for unrecognized values.
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "none", "silent":
		return LevelOff, nil
	case "error":
		return LevelError, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "", "info":
		return LevelInfo, nil
	case "debug":
		return LevelDebug, nil
	case "trace", "verbose", "silly":
		return LevelTrace, nil
	default:
		return LevelInfo, fmt.Errorf("unknown log level %q (expected off/error/warn/info/debug/trace)", s)
	}
}

// Logger is a minimal leveled logger writing single-line records to an
// io.Writer. It is safe for concurrent use.
type Logger struct {
	mu    sync.Mutex
	level Level
	out   io.Writer
	clock Clock
}

// Clock is the part of the SDK clock this package needs. Declared here rather than
// imported so diag stays a leaf package (see architecture_test.go); the injected
// clock satisfies it structurally.
type Clock interface {
	Now() time.Time
}

// New returns a Logger at the given level writing to out. If out is nil, os.Stderr
// is used. clock must not be nil; the client resolves one before constructing any
// collaborator.
func New(level Level, out io.Writer, clock Clock) *Logger {
	if out == nil {
		out = os.Stderr
	}
	return &Logger{level: level, out: out, clock: clock}
}

// Level returns the logger's current level.
func (l *Logger) Level() Level { return l.level }

func (l *Logger) log(lvl Level, msg string, kv ...any) {
	if l == nil || lvl > l.level || l.level == LevelOff {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] camunda %s", l.clock.Now().UTC().Format(time.RFC3339), strings.ToUpper(lvl.String()), msg)
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(&b, " %v=%v", kv[i], kv[i+1])
	}
	b.WriteByte('\n')
	_, _ = io.WriteString(l.out, b.String())
}

// Errorf logs at error level.
func (l *Logger) Error(msg string, kv ...any) { l.log(LevelError, msg, kv...) }

// Warn logs at warn level.
func (l *Logger) Warn(msg string, kv ...any) { l.log(LevelWarn, msg, kv...) }

// Info logs at info level.
func (l *Logger) Info(msg string, kv ...any) { l.log(LevelInfo, msg, kv...) }

// Debug logs at debug level.
func (l *Logger) Debug(msg string, kv ...any) { l.log(LevelDebug, msg, kv...) }

// Trace logs at trace level.
func (l *Logger) Trace(msg string, kv ...any) { l.log(LevelTrace, msg, kv...) }

// RedactedValue replaces the value of a secret variable in an environment snapshot.
const RedactedValue = "***redacted***"

// secretKeySubstrings identify env vars whose values must be redacted in a snapshot
// when the caller's declaration does not know the variable (see EnvironmentSnapshot).
var secretKeySubstrings = []string{"SECRET", "PASSWORD", "PASSPHRASE", "TOKEN", "KEY"}

func isSecretKey(key string) bool {
	upper := strings.ToUpper(key)
	// CACHE_DIR / *_PATH keys are locations, not secrets.
	if strings.HasSuffix(upper, "_PATH") || strings.HasSuffix(upper, "_DIR") {
		return false
	}
	for _, s := range secretKeySubstrings {
		if strings.Contains(upper, s) {
			return true
		}
	}
	return false
}

// EnvironmentSnapshot collects CAMUNDA_*/ZEEBE_* environment variables for
// support diagnostics using only the name heuristic to decide what to redact.
// Prefer EnvironmentSnapshotWithSecrets, which lets the caller declare credential
// variables explicitly; the heuristic alone misses names such as CAMUNDA_MTLS_CERT.
func EnvironmentSnapshot(environ []string) map[string]string {
	return EnvironmentSnapshotWithSecrets(environ, nil)
}

// EnvironmentSnapshotWithSecrets collects CAMUNDA_*/ZEEBE_* environment variables
// for support diagnostics, redacting secret values. environ should be the output
// of os.Environ(); if nil, os.Environ() is used.
//
// isSecret is the authoritative declaration of which variables carry credential
// material (the root package builds it from ConfigSchema, which this leaf package
// cannot import). A variable it reports as secret is always redacted, whatever its
// name looks like. For a variable it does not report as secret, a name heuristic
// is the fallback, so a credential the declaration does not know about (a user's
// own ZEEBE_* leftovers) is still not written out in full. isSecret may be nil, in
// which case only the heuristic applies.
func EnvironmentSnapshotWithSecrets(environ []string, isSecret func(key string) bool) map[string]string {
	if environ == nil {
		environ = os.Environ()
	}
	out := make(map[string]string)
	for _, kv := range environ {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		key, val := kv[:eq], kv[eq+1:]
		up := strings.ToUpper(key)
		if !strings.HasPrefix(up, "CAMUNDA_") && !strings.HasPrefix(up, "ZEEBE_") {
			continue
		}
		secret := (isSecret != nil && isSecret(key)) || isSecretKey(key)
		if secret && val != "" {
			out[key] = RedactedValue
		} else {
			out[key] = val
		}
	}
	return out
}
