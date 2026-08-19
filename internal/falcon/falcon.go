// Package falcon implements detection of the FALCON (nanobpmn command-stream)
// transport upgrade.
//
// A nanobpmn gateway (https://github.com/jwulf/nano-bpm) is an API/behaviour
// superset of the Camunda 8 Orchestration Cluster. It advertises a persistent
// command-stream WebSocket by including a "nano" object in its GET /v2/topology
// response. Stock Camunda has no such field, in which case the SDK stays on its
// byte-identical REST path.
//
// This package performs only the one-time capability probe and builds the
// command-stream failover directory (ws:// or wss:// endpoints, derived from the
// cluster's scheme). The WebSocket transport, credit-gated create producer, and
// pushed job worker are layered on top separately.
package falcon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// DefaultCommandStreamPath is the command-stream path assumed when the gateway's
// "nano" advertisement omits "falconPath".
const DefaultCommandStreamPath = "/falcon"

// Caps describes detected nanobpmn command-stream capabilities.
type Caps struct {
	// Endpoints is the command-stream WebSocket failover directory: one ws:// or
	// wss:// URL per cluster node, de-duplicated, with the configured address
	// always included. A single-node gateway yields a one-element directory.
	Endpoints []string
}

type topology struct {
	Nano *struct {
		FalconPath string `json:"falconPath"`
	} `json:"nano"`
	Brokers []struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"brokers"`
}

// Detect probes v2BaseURL's /topology for the nanobpmn "nano" advertisement.
//
// v2BaseURL is the same /v2 base address the REST client uses (e.g.
// "http://localhost:8080/v2"); Detect appends "/topology". httpClient should be
// the SDK's configured client so auth and TLS apply.
//
// It returns (caps, nil) when the gateway is a nanobpmn gateway; (nil, nil) when
// the gateway was reached but is stock Camunda (no "nano" field, or an
// unrecognised body); and (nil, err) for a transient failure the caller may
// retry — a transport error, a canceled/expired context, or a non-2xx status.
// In every case other than (caps, nil) the caller falls back to REST, so
// detection never fails a request.
func Detect(ctx context.Context, v2BaseURL string, httpClient *http.Client) (*Caps, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	base := strings.TrimRight(strings.TrimSpace(v2BaseURL), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/topology", nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("camunda: falcon topology probe returned status %d", resp.StatusCode)
	}

	var body topology
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		// Reached a 2xx endpoint but the body isn't a topology: treat as stock
		// (definitive) rather than a transient failure, so we don't re-probe forever.
		return nil, nil
	}
	if body.Nano == nil {
		return nil, nil
	}

	path := body.Nano.FalconPath
	if path == "" {
		path = DefaultCommandStreamPath
	}
	eps := endpointsFromTopology(base, path, body)
	if len(eps) == 0 {
		return nil, nil
	}
	return &Caps{Endpoints: eps}, nil
}

// endpointsFromTopology builds the command-stream failover directory from a
// /v2/topology body. Every brokers[].host:port becomes a "<ws|wss>://host:port<path>"
// endpoint. A broker reporting the unspecified/self placeholder (0.0.0.0 / ::) has
// its host replaced with the host actually used to reach the gateway, so every
// entry is dialable from the client. The configured address is always included,
// and the result is de-duplicated.
func endpointsFromTopology(v2BaseURL, path string, body topology) []string {
	u, err := url.Parse(v2BaseURL)
	if err != nil || u.Host == "" {
		return nil
	}
	scheme := wsScheme(u.Scheme)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	var out []string
	seen := map[string]bool{}
	add := func(ep string) {
		if !seen[ep] {
			seen[ep] = true
			out = append(out, ep)
		}
	}

	// The configured address is always a valid entry.
	add(scheme + "://" + u.Host + path)

	for _, b := range body.Brokers {
		if b.Port == 0 {
			continue
		}
		host := b.Host
		if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
			host = u.Hostname()
		}
		add(fmt.Sprintf("%s://%s:%d%s", scheme, bracketHost(host), b.Port, path))
	}
	return out
}

// bracketHost wraps an unbracketed IPv6 literal in square brackets so it is a
// valid URL authority ("2001:db8::1" -> "[2001:db8::1]"). Names and IPv4 literals
// (and already-bracketed hosts) are returned unchanged.
func bracketHost(h string) string {
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		return "[" + h + "]"
	}
	return h
}

// wsScheme maps an HTTP scheme to the corresponding WebSocket scheme: https → wss
// (TLS), anything else → ws (plaintext).
func wsScheme(httpScheme string) string {
	if strings.EqualFold(httpScheme, "https") {
		return "wss"
	}
	return "ws"
}
