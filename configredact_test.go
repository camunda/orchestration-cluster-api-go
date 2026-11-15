package camunda

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/camunda/orchestration-cluster-api-go/v11/internal/diag"
)

// cfgSentinel values are distinctive so a leak into formatted output is
// unambiguous rather than a coincidental substring match.
const (
	cfgSentinelClientSecret = "CAMUNDA-SENTINEL-CLIENT-SECRET-9f3a"
	cfgSentinelBasicPw      = "CAMUNDA-SENTINEL-BASIC-PASSWORD-9f3a"
	cfgSentinelKeyPass      = "CAMUNDA-SENTINEL-KEY-PASSPHRASE-9f3a"
	cfgSentinelCertPEM      = "CAMUNDA-SENTINEL-CERT-PEM-9f3a"
	cfgSentinelKeyPEM       = "CAMUNDA-SENTINEL-KEY-PEM-9f3a"
)

func sentinelConfig() *Config {
	return &Config{
		RestAddress:       "http://localhost:8080",
		GrpcAddress:       "localhost:26500",
		AuthStrategy:      AuthOAuth,
		ClientID:          "sentinel-client-id",
		ClientSecret:      cfgSentinelClientSecret,
		OAuthURL:          "https://idp.example/oauth/token",
		BasicAuthUsername: "sentinel-user",
		BasicAuthPassword: cfgSentinelBasicPw,
		TLS: TLSConfig{
			Cert:          cfgSentinelCertPEM,
			Key:           cfgSentinelKeyPEM,
			CA:            "ca-pem-not-secret",
			CertPath:      "/etc/camunda/cert.pem",
			KeyPassphrase: cfgSentinelKeyPass,
		},
	}
}

func allSentinels() []string {
	return []string{
		cfgSentinelClientSecret,
		cfgSentinelBasicPw,
		cfgSentinelKeyPass,
		cfgSentinelCertPEM,
		cfgSentinelKeyPEM,
	}
}

// TestConfigFormattingRedactsEverySecret covers the defect class rather than the
// single reported verb: every fmt verb that renders a Config or a TLSConfig must
// redact every field ConfigSchema marks Secret. A user or an SDK contributor
// logging the resolved configuration is the realistic path, and it must be safe
// whichever verb they reach for.
func TestConfigFormattingRedactsEverySecret(t *testing.T) {
	cfg := sentinelConfig()

	renders := map[string]string{
		"Config.String":    cfg.String(),
		"Config.GoString":  cfg.GoString(),
		"TLSConfig.String": cfg.TLS.String(),
	}
	// The verbs are held in variables so each is exercised through the fmt
	// dispatch path (which is where the leak was), rather than being constant-
	// folded away by a linter suggesting String() instead.
	for _, verb := range []string{"%v", "%+v", "%s", "%#v"} {
		renders["Config pointer "+verb] = fmt.Sprintf(verb, cfg)
		renders["Config value "+verb] = fmt.Sprintf(verb, *cfg)
		renders["TLSConfig "+verb] = fmt.Sprintf(verb, cfg.TLS)
	}

	for name, out := range renders {
		for _, secret := range allSentinels() {
			if strings.Contains(out, secret) {
				t.Errorf("%s leaked secret %q: %s", name, secret, out)
			}
		}
		if !strings.Contains(out, redactedPlaceholder) {
			t.Errorf("%s rendered no %s marker: %s", name, redactedPlaceholder, out)
		}
	}
}

// TestConfigFormattingKeepsNonSecrets guards the other half: redaction must not
// swallow the fields that make a config dump worth having in the first place.
func TestConfigFormattingKeepsNonSecrets(t *testing.T) {
	cfg := sentinelConfig()
	out := fmt.Sprintf("%+v", cfg)
	for _, want := range []string{
		"http://localhost:8080",
		"localhost:26500",
		"sentinel-client-id",
		"https://idp.example/oauth/token",
		"sentinel-user",
		"OAUTH",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Config formatting dropped non-secret %q: %s", want, out)
		}
	}
	tlsOut := fmt.Sprintf("%v", cfg.TLS)
	for _, want := range []string{"ca-pem-not-secret", "/etc/camunda/cert.pem"} {
		if !strings.Contains(tlsOut, want) {
			t.Errorf("TLSConfig formatting dropped non-secret %q: %s", want, tlsOut)
		}
	}
}

// TestConfigFormattingDistinguishesUnsetFromRedacted keeps the output honest: an
// unset credential must not read as a configured one. Operators use a config
// dump to find out which credentials are actually in play.
func TestConfigFormattingDistinguishesUnsetFromRedacted(t *testing.T) {
	empty := fmt.Sprintf("%+v", &Config{})
	if strings.Contains(empty, redactedPlaceholder) {
		t.Errorf("empty Config reports redacted secrets it does not hold: %s", empty)
	}
	set := fmt.Sprintf("%+v", &Config{ClientSecret: cfgSentinelClientSecret})
	if !strings.Contains(set, "ClientSecret:"+redactedPlaceholder) {
		t.Errorf("configured ClientSecret not marked as redacted: %s", set)
	}
}

// TestConfigFormattingRendersEveryExportedField is the anti-drift guard for the
// reflective renderer: a field added to Config or TLSConfig later must appear in
// the output rather than being silently dropped, which would turn a redacting
// String() into a lossy one.
func TestConfigFormattingRendersEveryExportedField(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
		out  string
	}{
		{"Config", reflect.TypeOf(Config{}), fmt.Sprintf("%+v", sentinelConfig())},
		{"TLSConfig", reflect.TypeOf(TLSConfig{}), fmt.Sprintf("%+v", sentinelConfig().TLS)},
	} {
		for i := range tc.typ.NumField() {
			f := tc.typ.Field(i)
			if !f.IsExported() {
				continue
			}
			if !strings.Contains(tc.out, f.Name+":") {
				t.Errorf("%s.%s is missing from the formatted output: %s", tc.name, f.Name, tc.out)
			}
		}
	}
}

// TestRedactedFieldsMatchConfigSchema pins the redaction to ConfigSchema, which
// is the SDK's single source of truth for which values are credential material.
// Adding a Secret entry to the schema without teaching the formatter about it
// fails here rather than leaking quietly at runtime.
func TestRedactedFieldsMatchConfigSchema(t *testing.T) {
	wantSecret := map[string]bool{}
	for _, f := range ConfigSchema {
		if f.Secret {
			wantSecret[f.Keys[0]] = true
		}
	}

	gotSecret := map[string]bool{}
	for key := range secretConfigFields {
		gotSecret[key] = true
	}

	for key := range wantSecret {
		if !gotSecret[key] {
			t.Errorf("ConfigSchema marks %s secret but secretConfigFields does not redact it", key)
		}
	}
	for key := range gotSecret {
		if !wantSecret[key] {
			t.Errorf("secretConfigFields redacts %s but ConfigSchema does not mark it secret", key)
		}
	}

	// Every mapped field must actually exist on the struct it names, so a rename
	// cannot leave a dangling redaction rule behind.
	for key, ref := range secretConfigFields {
		var typ reflect.Type
		switch ref.Struct {
		case "Config":
			typ = reflect.TypeOf(Config{})
		case "TLSConfig":
			typ = reflect.TypeOf(TLSConfig{})
		default:
			t.Errorf("%s names unknown struct %q", key, ref.Struct)
			continue
		}
		if _, ok := typ.FieldByName(ref.Field); !ok {
			t.Errorf("%s names %s.%s, which does not exist", key, ref.Struct, ref.Field)
		}
	}
}

// TestRedactedPlaceholderMatchesDiag keeps the two redaction paths reading the
// same, so a support bundle containing both an environment snapshot and a config
// dump does not use two different words for the same thing.
func TestRedactedPlaceholderMatchesDiag(t *testing.T) {
	// Ask internal/diag for its marker rather than restating it here: comparing
	// two copies of the same literal would pass even if diag changed.
	snapshot := diag.EnvironmentSnapshot([]string{"CAMUNDA_CLIENT_SECRET=" + cfgSentinelClientSecret})
	got, ok := snapshot["CAMUNDA_CLIENT_SECRET"]
	if !ok {
		t.Fatal("diag.EnvironmentSnapshot did not report CAMUNDA_CLIENT_SECRET at all")
	}
	if got == cfgSentinelClientSecret {
		t.Fatalf("diag.EnvironmentSnapshot did not redact the secret: %q", got)
	}
	if got != redactedPlaceholder {
		t.Errorf("redactedPlaceholder = %q but internal/diag uses %q; a support bundle would "+
			"use two different words for the same thing", redactedPlaceholder, got)
	}
}

// TestConfigFormattingDoesNotRaceWithInjectedDependencies guards the injected
// Clock and Random fields. A Config can be formatted while the SDK is running,
// and the injected SeededRandom mutates its state on every draw. Dumping that
// struct field by field reads the state while Float64 writes it, so the
// dependencies are rendered by type identity only. The race detector is what
// fails this test, so it is meaningful under `go test -race`.
func TestConfigFormattingDoesNotRaceWithInjectedDependencies(t *testing.T) {
	const rounds = 2000
	rnd := NewSeededRandom(42)
	cfg := sentinelConfig()
	cfg.Random = rnd
	cfg.Clock = LiveClock{}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range rounds {
			rnd.Float64()
		}
	}()
	for range rounds {
		_ = fmt.Sprintf("%v %+v %#v %s", cfg, cfg, cfg, cfg)
		_ = fmt.Sprintf("%v %+v %#v %s", *cfg, *cfg, *cfg, *cfg)
	}
	wg.Wait()
}

// TestConfigFormattingNamesInjectedDependencies pins what replaces the dump: the
// dynamic type of an injected dependency is still visible, without its state.
func TestConfigFormattingNamesInjectedDependencies(t *testing.T) {
	cfg := sentinelConfig()
	cfg.Random = NewSeededRandom(42)
	cfg.Clock = LiveClock{}
	for _, verb := range []string{"%v", "%+v", "%#v"} {
		out := fmt.Sprintf(verb, cfg)
		for _, want := range []string{"*camunda.SeededRandom", "camunda.LiveClock"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s output does not name %s: %s", verb, want, out)
			}
		}
		if strings.Contains(out, "state:") {
			t.Errorf("%s output dumps the injected dependency's state: %s", verb, out)
		}
	}
}
