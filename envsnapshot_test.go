package camunda

import (
	"strings"
	"testing"

	"github.com/camunda/orchestration-cluster-api-go/v11/internal/diag"
)

// TestEnvironmentSnapshotRedactsEverySchemaSecret ties the support-snapshot
// redaction to the ConfigSchema Secret declarations: every variable (and every
// alias) the schema marks Secret must be redacted, whatever its name looks like.
// A name heuristic alone misses CAMUNDA_MTLS_CERT, which carries credential
// material but contains none of the heuristic's substrings.
func TestEnvironmentSnapshotRedactsEverySchemaSecret(t *testing.T) {
	const sentinel = "CAMUNDA-SENTINEL-SCHEMA-SECRET-9f3a"

	var environ, secretKeys []string
	for _, f := range ConfigSchema {
		for _, k := range f.Keys {
			environ = append(environ, k+"="+sentinel)
			if f.Secret {
				secretKeys = append(secretKeys, k)
			}
		}
	}
	if len(secretKeys) == 0 {
		t.Fatal("ConfigSchema declares no secrets; the guard would be vacuous")
	}

	snap := environmentSnapshot(environ)
	for _, k := range secretKeys {
		got, ok := snap[k]
		if !ok {
			t.Errorf("%s: missing from snapshot", k)
			continue
		}
		if strings.Contains(got, sentinel) {
			t.Errorf("%s is marked Secret in ConfigSchema but the snapshot leaked its value", k)
		}
	}
}

// TestEnvironmentSnapshotKeepsHeuristicForUnknownVariables covers variables the
// schema does not know about (a user's own ZEEBE_* leftovers): the name
// heuristic still applies so an unregistered credential is not written out.
func TestEnvironmentSnapshotKeepsHeuristicForUnknownVariables(t *testing.T) {
	snap := environmentSnapshot([]string{
		"ZEEBE_LEGACY_API_TOKEN=tok-123",
		"ZEEBE_SOMETHING_ELSE=visible",
	})
	if snap["ZEEBE_LEGACY_API_TOKEN"] != diag.RedactedValue {
		t.Errorf("unknown token-like variable should be redacted, got %q", snap["ZEEBE_LEGACY_API_TOKEN"])
	}
	if snap["ZEEBE_SOMETHING_ELSE"] != "visible" {
		t.Errorf("unknown benign variable should be kept, got %q", snap["ZEEBE_SOMETHING_ELSE"])
	}
}

// TestEnvironmentSnapshotKeepsKnownNonSecrets: a variable the schema knows is not
// secret stays visible even when its name looks like a credential to the
// heuristic (CAMUNDA_TOKEN_AUDIENCE, CAMUNDA_TOKEN_SCOPE), so diagnostics keep
// the configuration a support engineer needs.
func TestEnvironmentSnapshotKeepsKnownNonSecrets(t *testing.T) {
	var environ, plain []string
	for _, f := range ConfigSchema {
		if f.Secret {
			continue
		}
		for _, k := range f.Keys {
			environ = append(environ, k+"=visible-value")
			plain = append(plain, k)
		}
	}
	snap := environmentSnapshot(environ)
	for _, k := range plain {
		if snap[k] != "visible-value" {
			t.Errorf("%s is not Secret in ConfigSchema but was redacted: %q", k, snap[k])
		}
	}
	for _, k := range []string{"CAMUNDA_TOKEN_AUDIENCE", "CAMUNDA_TOKEN_SCOPE"} {
		if _, ok := snap[k]; !ok {
			t.Errorf("%s no longer in ConfigSchema; update this test's pinned examples", k)
		}
	}
}
