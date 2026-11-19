package camunda

import (
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
)

// sdkUserAgentName is the product token every SDK connection (REST, OAuth token
// request, gRPC) leads its User-Agent with, so gateway usage reports can
// attribute traffic to this SDK. It matches the sibling SDKs'
// camunda-orchestration-sdk-<lang>/<version> convention.
const sdkUserAgentName = "camunda-orchestration-sdk-go"

// sdkVersionFallback is reported when the module version cannot be read from the
// build info (e.g. a build of this repository itself, or tests).
const sdkVersionFallback = "development"

var sdkVersion = sync.OnceValue(func() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return sdkVersionFallback
	}
	return versionFromBuildInfo(bi, reflect.TypeFor[Config]().PkgPath())
})

// versionFromBuildInfo returns the version of module modPath recorded in bi:
// the dependency entry in a consumer's build (honouring replace directives), or
// the main module when this module is itself being built.
func versionFromBuildInfo(bi *debug.BuildInfo, modPath string) string {
	clean := func(v string) string {
		v = strings.TrimPrefix(v, "v")
		if v == "" || v == "(devel)" {
			return sdkVersionFallback
		}
		return v
	}
	for _, d := range bi.Deps {
		if d.Path == modPath {
			// A replacement is authoritative: an unversioned (local
			// directory) replacement is a development build, not the
			// originally required release.
			if d.Replace != nil {
				return clean(d.Replace.Version)
			}
			return clean(d.Version)
		}
	}
	if bi.Main.Path == modPath {
		return clean(bi.Main.Version)
	}
	return sdkVersionFallback
}

// userAgent returns the User-Agent value for cfg: the SDK identifier first,
// followed by any application-supplied value (WithUserAgent /
// CAMUNDA_USER_AGENT). The application value is appended, never substituted, so
// traffic always stays attributable to the SDK.
func userAgent(cfg *Config) string {
	ua := sdkUserAgentName + "/" + sdkVersion()
	if cfg != nil {
		if app := strings.TrimSpace(cfg.UserAgent); app != "" {
			ua += " " + app
		}
	}
	return ua
}
