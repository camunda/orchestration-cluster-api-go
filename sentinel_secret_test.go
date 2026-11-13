package camunda

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/camunda/orchestration-cluster-api-go/v11/internal/diag"
)

// This file is the SDK's sentinel-secret regression test, required by the shared
// SDK policy ("Secrets must not leak"). It configures recognisable sentinel
// credentials, drives the auth success and auth failure paths end to end through a
// real client, and asserts the sentinels never appear in any sink: the SDK log (at
// trace level), the error returned for every failing call, formatted
// configuration, or the environment snapshot.
//
// Extend it when you add a new place a credential can flow: add the credential to
// sentinelSecrets, a scenario that drives it, or a sink to collectSentinelSinks.

const (
	sentinelOAuthSecret = "CAMUNDA-SENTINEL-DO-NOT-LOG-9f3a"
	sentinelBasicPw     = "CAMUNDA-SENTINEL-BASIC-PW-9f3a"
	sentinelPassphrase  = "CAMUNDA-SENTINEL-PASSPHRASE-9f3a"
	sentinelCertPEM     = "CAMUNDA-SENTINEL-CERT-PEM-9f3a"
	sentinelKeyPEM      = "CAMUNDA-SENTINEL-KEY-PEM-9f3a"
	// sentinelAccessToken is the bearer token the identity provider issues. It is
	// as sensitive as the credentials that obtained it.
	sentinelAccessToken = "CAMUNDA-SENTINEL-ACCESS-TOKEN-9f3a"
	// sentinelIDPBody is arbitrary content an identity provider puts in a response
	// body; the SDK must not assume it is secret-free.
	sentinelIDPBody = "CAMUNDA-SENTINEL-IDP-BODY-9f3a"
)

// sentinelSecrets are the values that must never reach a sink. The base64 form of
// the basic-auth pair is included because that is what travels in the header.
func sentinelSecrets() []string {
	return []string{
		sentinelOAuthSecret, sentinelBasicPw, sentinelPassphrase, sentinelCertPEM,
		sentinelKeyPEM, sentinelAccessToken, sentinelIDPBody,
		base64.StdEncoding.EncodeToString([]byte("sentinel-user:" + sentinelBasicPw)),
	}
}

// sentinelScenario drives one client configuration through a path that touches a
// credential and returns whatever it produced.
type sentinelScenario struct {
	name string
	// run returns the SDK log captured at trace level and every error observed.
	run func(t *testing.T) (logs string, errs []error)
}

// idp builds a token endpoint. handler decides the response.
func sentinelIDP(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// sentinelAPI is a cluster stub that records the Authorization header it saw and
// signals each job-activation request.
type sentinelAPI struct {
	srv         *httptest.Server
	auth        atomic.Value // last Authorization header
	activations chan struct{}
}

func newSentinelAPI(t *testing.T, status int) *sentinelAPI {
	t.Helper()
	a := &sentinelAPI{activations: make(chan struct{}, 64)}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.auth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/jobs/activation") {
			select {
			case a.activations <- struct{}{}:
			default:
			}
			if status == http.StatusOK {
				_, _ = io.WriteString(w, `{"jobs":[]}`)
				return
			}
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *sentinelAPI) lastAuth() string {
	s, _ := a.auth.Load().(string)
	return s
}

// newSentinelClient builds a client from cfg, with trace logging captured.
func newSentinelClient(t *testing.T, cfg *Config) (*CamundaClient, *syncBuffer) {
	t.Helper()
	cfg.Retry = RetryConfig{MaxAttempts: 1}
	client, err := newFromConfig(cfg)
	if err != nil {
		t.Fatalf("newFromConfig: %v", err)
	}
	logs := &syncBuffer{}
	client.logger = diag.New(diag.LevelTrace, logs, client.clock)
	return client, logs
}

func oauthSentinelConfig(t *testing.T, api *sentinelAPI, tokenURL string) *Config {
	t.Helper()
	cfg, err := loadConfig(noEnv, nil,
		WithRestAddress(api.srv.URL),
		WithOAuth("sentinel-client-id", sentinelOAuthSecret, tokenURL),
		WithLogLevel(LogTrace))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	return cfg
}

// callAndPoll makes one REST call and then runs a job worker until it has
// attempted an activation (or logged the failure of one), returning the logs and
// the errors seen. The worker is what logs auth failures on every poll.
func callAndPoll(t *testing.T, client *CamundaClient, logs *syncBuffer, api *sentinelAPI, expectPollFailure bool) (string, []error) {
	t.Helper()
	var errs []error
	if _, err := client.GetTopology(context.Background()); err != nil {
		errs = append(errs, err)
	}

	worker := client.NewJobWorker("demo-task",
		func(context.Context, *Job) (map[string]any, error) { return nil, nil },
		WithRequestTimeout(time.Second), WithPollInterval(5*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = worker.Run(ctx) }()

	reached := func() bool {
		if expectPollFailure {
			return strings.Contains(logs.String(), "activate jobs failed")
		}
		select {
		case <-api.activations:
			return true
		default:
			return false
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for !reached() {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("worker did not reach the expected state; logs:\n%s", logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	return logs.String(), errs
}

func sentinelScenarios() []sentinelScenario {
	tokenJSON := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"expires_in":3600}`, sentinelAccessToken)
	}
	return []sentinelScenario{
		{
			name: "oauth success",
			run: func(t *testing.T) (string, []error) {
				api := newSentinelAPI(t, http.StatusOK)
				idp := sentinelIDP(t, tokenJSON)
				client, logs := newSentinelClient(t, oauthSentinelConfig(t, api, idp.URL))
				out, errs := callAndPoll(t, client, logs, api, false)
				// Guard against a vacuous pass: the credential really flowed.
				if got := api.lastAuth(); got != "Bearer "+sentinelAccessToken {
					t.Fatalf("success path did not authenticate with the issued token; Authorization = %q", got)
				}
				return out, errs
			},
		},
		{
			name: "oauth token endpoint rejects with sentinel body",
			run: func(t *testing.T) (string, []error) {
				api := newSentinelAPI(t, http.StatusOK)
				idp := sentinelIDP(t, func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"`+sentinelIDPBody+`"}`)
				})
				client, logs := newSentinelClient(t, oauthSentinelConfig(t, api, idp.URL))
				out, errs := callAndPoll(t, client, logs, api, true)
				requireFailure(t, errs)
				return out, errs
			},
		},
		{
			name: "oauth token endpoint echoes the submitted form",
			run: func(t *testing.T) (string, []error) {
				api := newSentinelAPI(t, http.StatusOK)
				idp := sentinelIDP(t, func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write(body)
				})
				client, logs := newSentinelClient(t, oauthSentinelConfig(t, api, idp.URL))
				out, errs := callAndPoll(t, client, logs, api, true)
				requireFailure(t, errs)
				return out, errs
			},
		},
		{
			name: "oauth token endpoint returns a malformed body",
			run: func(t *testing.T) (string, []error) {
				api := newSentinelAPI(t, http.StatusOK)
				idp := sentinelIDP(t, func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, "not json "+sentinelIDPBody+" "+sentinelAccessToken)
				})
				client, logs := newSentinelClient(t, oauthSentinelConfig(t, api, idp.URL))
				out, errs := callAndPoll(t, client, logs, api, true)
				requireFailure(t, errs)
				return out, errs
			},
		},
		{
			name: "oauth token endpoint returns an empty access token",
			run: func(t *testing.T) (string, []error) {
				api := newSentinelAPI(t, http.StatusOK)
				idp := sentinelIDP(t, func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, `{"access_token":"","expires_in":3600,"note":"`+sentinelIDPBody+`"}`)
				})
				client, logs := newSentinelClient(t, oauthSentinelConfig(t, api, idp.URL))
				out, errs := callAndPoll(t, client, logs, api, true)
				requireFailure(t, errs)
				return out, errs
			},
		},
		{
			name: "oauth token endpoint unreachable",
			run: func(t *testing.T) (string, []error) {
				api := newSentinelAPI(t, http.StatusOK)
				idp := sentinelIDP(t, tokenJSON)
				url := idp.URL
				idp.Close() // connection refused
				client, logs := newSentinelClient(t, oauthSentinelConfig(t, api, url))
				out, errs := callAndPoll(t, client, logs, api, true)
				requireFailure(t, errs)
				return out, errs
			},
		},
		{
			name: "basic auth success",
			run: func(t *testing.T) (string, []error) {
				api := newSentinelAPI(t, http.StatusOK)
				cfg, err := loadConfig(noEnv, nil, WithRestAddress(api.srv.URL),
					WithBasicAuth("sentinel-user", sentinelBasicPw), WithLogLevel(LogTrace))
				if err != nil {
					t.Fatalf("loadConfig: %v", err)
				}
				client, logs := newSentinelClient(t, cfg)
				out, errs := callAndPoll(t, client, logs, api, false)
				want := "Basic " + base64.StdEncoding.EncodeToString([]byte("sentinel-user:"+sentinelBasicPw))
				if got := api.lastAuth(); got != want {
					t.Fatalf("success path did not send basic credentials; Authorization = %q", got)
				}
				return out, errs
			},
		},
		{
			name: "basic auth rejected",
			run: func(t *testing.T) (string, []error) {
				api := newSentinelAPI(t, http.StatusUnauthorized)
				cfg, err := loadConfig(noEnv, nil, WithRestAddress(api.srv.URL),
					WithBasicAuth("sentinel-user", sentinelBasicPw), WithLogLevel(LogTrace))
				if err != nil {
					t.Fatalf("loadConfig: %v", err)
				}
				client, logs := newSentinelClient(t, cfg)
				out, errs := callAndPoll(t, client, logs, api, true)
				requireFailure(t, errs)
				return out, errs
			},
		},
		{
			name: "mtls key passphrase is rejected at construction",
			run: func(t *testing.T) (string, []error) {
				cfg, err := loadConfig(noEnv, nil, WithNoAuth())
				if err != nil {
					t.Fatalf("loadConfig: %v", err)
				}
				cfg.TLS = TLSConfig{Cert: sentinelCertPEM, Key: sentinelKeyPEM, KeyPassphrase: sentinelPassphrase}
				_, err = newFromConfig(cfg)
				requireFailure(t, []error{err})
				return "", []error{err}
			},
		},
		{
			name: "mtls certificate and key that do not parse",
			run: func(t *testing.T) (string, []error) {
				cfg, err := loadConfig(noEnv, nil, WithNoAuth())
				if err != nil {
					t.Fatalf("loadConfig: %v", err)
				}
				cfg.TLS = TLSConfig{Cert: sentinelCertPEM, Key: sentinelKeyPEM}
				_, err = newFromConfig(cfg)
				requireFailure(t, []error{err})
				return "", []error{err}
			},
		},
	}
}

func requireFailure(t *testing.T, errs []error) {
	t.Helper()
	if len(errs) == 0 || errs[0] == nil {
		t.Fatal("failure scenario produced no error; the test would pass vacuously")
	}
}

// sentinelConfigSinks renders configuration and environment through every
// formatting path an operator might log or attach to a support request.
func sentinelConfigSinks() map[string]string {
	cfg := &Config{
		RestAddress:       "http://localhost:8080",
		AuthStrategy:      AuthOAuth,
		ClientID:          "sentinel-client-id",
		ClientSecret:      sentinelOAuthSecret,
		OAuthURL:          "https://idp.example/oauth/token",
		BasicAuthUsername: "sentinel-user",
		BasicAuthPassword: sentinelBasicPw,
		TLS:               TLSConfig{Cert: sentinelCertPEM, Key: sentinelKeyPEM, KeyPassphrase: sentinelPassphrase},
	}
	sinks := map[string]string{}
	for _, v := range []string{"%v", "%+v", "%#v", "%s"} {
		sinks["Config "+v] = fmt.Sprintf(v, cfg)
		sinks["Config value "+v] = fmt.Sprintf(v, *cfg)
		sinks["TLSConfig "+v] = fmt.Sprintf(v, cfg.TLS)
	}

	// Every variable the schema declares secret, carrying the sentinel secrets.
	var environ []string
	for _, f := range ConfigSchema {
		for _, k := range f.Keys {
			if f.Secret {
				environ = append(environ, k+"="+sentinelOAuthSecret+sentinelCertPEM+sentinelKeyPEM+sentinelPassphrase+sentinelBasicPw)
			}
		}
	}
	sinks["EnvironmentSnapshot"] = fmt.Sprint(environmentSnapshot(environ))
	return sinks
}

// TestSentinelSecretsNeverReachASink is the sentinel-secret regression test.
func TestSentinelSecretsNeverReachASink(t *testing.T) {
	secrets := sentinelSecrets()

	check := func(t *testing.T, sink, text string) {
		t.Helper()
		for _, s := range secrets {
			if strings.Contains(text, s) {
				t.Errorf("sentinel %q leaked into %s:\n%s", s, sink, text)
			}
		}
	}

	for _, sc := range sentinelScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			logs, errs := sc.run(t)
			check(t, "the SDK log", logs)
			for i, err := range errs {
				if err != nil {
					check(t, fmt.Sprintf("error #%d (%T)", i, err), err.Error())
					check(t, fmt.Sprintf("error #%d (%T) %%+v", i, err), fmt.Sprintf("%+v", err))
				}
			}
		})
	}

	t.Run("configuration and environment formatting", func(t *testing.T) {
		for sink, text := range sentinelConfigSinks() {
			check(t, sink, text)
		}
	})
}
