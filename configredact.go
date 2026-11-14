package camunda

import (
	"fmt"
	"reflect"
	"strings"
)

// redactedPlaceholder is substituted for credential material in the formatted
// output of Config and TLSConfig. It is the same marker internal/diag uses for
// secret-like environment variables, so a support bundle that carries both an
// environment snapshot and a config dump reads consistently.
const redactedPlaceholder = "***redacted***"

// secretFieldRef locates a credential-bearing field by the struct that declares
// it and the field's name.
type secretFieldRef struct {
	Struct string
	Field  string
}

// secretConfigFields maps each canonical environment variable that supplies
// credential material to the Config or TLSConfig field it populates.
//
// ConfigSchema's Secret flag stays the SDK's single source of truth for what
// counts as a secret: TestRedactedFieldsMatchConfigSchema fails if an entry is
// marked Secret there without a redaction rule here (or the reverse), and if a
// rule names a field that no longer exists. A new credential therefore cannot
// reach the formatted output unredacted without failing the build first.
var secretConfigFields = map[string]secretFieldRef{
	"CAMUNDA_CLIENT_SECRET":       {Struct: "Config", Field: "ClientSecret"},
	"CAMUNDA_BASIC_AUTH_PASSWORD": {Struct: "Config", Field: "BasicAuthPassword"},
	"CAMUNDA_MTLS_CERT":           {Struct: "TLSConfig", Field: "Cert"},
	"CAMUNDA_MTLS_KEY":            {Struct: "TLSConfig", Field: "Key"},
	"CAMUNDA_MTLS_KEY_PASSPHRASE": {Struct: "TLSConfig", Field: "KeyPassphrase"},
}

// secretFieldsOf collects the field names of structName that hold credentials.
func secretFieldsOf(structName string) map[string]bool {
	out := make(map[string]bool)
	for _, ref := range secretConfigFields {
		if ref.Struct == structName {
			out[ref.Field] = true
		}
	}
	return out
}

var (
	configSecretFields = secretFieldsOf("Config")
	tlsSecretFields    = secretFieldsOf("TLSConfig")
)

// formatRedacted renders v's exported fields, substituting redactedPlaceholder
// for any field named in secrets that actually holds a value.
//
// It walks the struct reflectively rather than listing fields by hand so that a
// field added later is rendered automatically; an omission would quietly turn a
// redacting String into a lossy one, which the anti-drift test guards against.
//
// Fields of interface type, the injected Clock and Random, render as their
// dynamic type name (`<nil>` when unset) and never as their contents.
//
// An unset secret renders as its zero value rather than as the placeholder: an
// operator reads a config dump to find out which credentials are in play, so
// "not configured" and "configured, withheld" must not look the same.
func formatRedacted(v reflect.Value, typeName string, secrets map[string]bool, goSyntax bool) string {
	t := v.Type()
	var b strings.Builder
	if goSyntax {
		b.WriteString("camunda." + typeName + "{")
	} else {
		b.WriteByte('{')
	}
	first := true
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if !first {
			if goSyntax {
				b.WriteString(", ")
			} else {
				b.WriteByte(' ')
			}
		}
		first = false
		b.WriteString(f.Name)
		b.WriteByte(':')

		fv := v.Field(i)
		if f.Type.Kind() == reflect.Interface {
			// Injected dependencies (Clock, Random) are rendered by type identity
			// only. Dumping the value would read their mutable internals, which a
			// running SDK updates concurrently (SeededRandom advances its state on
			// every draw), so formatting a live Config would be a data race.
			fmt.Fprintf(&b, "%T", fv.Interface())
			continue
		}
		if secrets[f.Name] && !fv.IsZero() {
			if goSyntax {
				fmt.Fprintf(&b, "%q", redactedPlaceholder)
			} else {
				b.WriteString(redactedPlaceholder)
			}
			continue
		}
		if goSyntax {
			fmt.Fprintf(&b, "%#v", fv.Interface())
		} else {
			fmt.Fprintf(&b, "%+v", fv.Interface())
		}
	}
	b.WriteByte('}')
	return b.String()
}

// String renders the resolved configuration with every credential replaced by a
// redaction marker, so logging a Config (`log.Printf("%+v", cfg)`) cannot spill
// the client secret, the basic-auth password, the inline mTLS certificate or key
// PEM, or the key passphrase. Non-secret settings are rendered in full.
//
// Implementing String also covers the %v, %+v and %s verbs; GoString covers %#v.
func (c Config) String() string {
	return formatRedacted(reflect.ValueOf(c), "Config", configSecretFields, false)
}

// GoString renders the configuration in Go syntax for the %#v verb, with the
// same credential redaction String applies.
func (c Config) GoString() string {
	return formatRedacted(reflect.ValueOf(c), "Config", configSecretFields, true)
}

// String renders the TLS material with the inline certificate PEM, private key
// PEM and key passphrase replaced by a redaction marker. The CA PEM and the
// *Path file locations are not credentials and are rendered in full.
//
// Implementing String also covers the %v, %+v and %s verbs; GoString covers %#v.
func (t TLSConfig) String() string {
	return formatRedacted(reflect.ValueOf(t), "TLSConfig", tlsSecretFields, false)
}

// GoString renders the TLS material in Go syntax for the %#v verb, with the same
// credential redaction String applies.
func (t TLSConfig) GoString() string {
	return formatRedacted(reflect.ValueOf(t), "TLSConfig", tlsSecretFields, true)
}
