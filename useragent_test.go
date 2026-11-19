package camunda

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/camunda/orchestration-cluster-api-go/v11/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const sdkUAPrefix = "camunda-orchestration-sdk-go/"

// uaServer records the User-Agent of every request it receives, serving an
// OAuth token on /token and an empty JSON object elsewhere.
func uaServer(t *testing.T) (*httptest.Server, chan [2]string) {
	t.Helper()
	got := make(chan [2]string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- [2]string{r.URL.Path, r.Header.Get("User-Agent")}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			_, _ = io.WriteString(w, `{"access_token":"t","expires_in":3600}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func drainUA(got chan [2]string) map[string]string {
	out := map[string]string{}
	for {
		select {
		case e := <-got:
			if _, ok := out[e[0]]; !ok {
				out[e[0]] = e[1]
			}
		default:
			return out
		}
	}
}

// TestUserAgentOnRESTAndOAuth: the REST call and the OAuth token request both
// lead with the SDK identifier.
func TestUserAgentOnRESTAndOAuth(t *testing.T) {
	srv, got := uaServer(t)
	client, err := New(WithRestAddress(srv.URL), WithOAuth("id", "secret", srv.URL+"/token"),
		WithFalcon(false), WithLogLevel(LogOff), WithOAuthCacheDir(t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, _ = client.GetTopology(context.Background())
	uas := drainUA(got)
	tok, ok := uas["/token"]
	if !ok {
		t.Fatalf("token endpoint not called; saw %v", uas)
	}
	if !strings.HasPrefix(tok, sdkUAPrefix) {
		t.Errorf("OAuth User-Agent = %q, want prefix %q", tok, sdkUAPrefix)
	}
	rest, ok := uas["/v2/topology"]
	if !ok {
		t.Fatalf("REST endpoint not called; saw %v", uas)
	}
	if !strings.HasPrefix(rest, sdkUAPrefix) {
		t.Errorf("REST User-Agent = %q, want prefix %q", rest, sdkUAPrefix)
	}
}

// TestUserAgentAppendsApplicationValue: an application-supplied value (option or
// env) is appended after the SDK identifier, never replacing it.
func TestUserAgentAppendsApplicationValue(t *testing.T) {
	srv, got := uaServer(t)
	client, err := New(WithRestAddress(srv.URL), WithOAuth("id", "secret", srv.URL+"/token"),
		WithFalcon(false), WithLogLevel(LogOff), WithOAuthCacheDir(t.TempDir()),
		WithUserAgent("my-app/1.2"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, _ = client.GetTopology(context.Background())
	for path, ua := range drainUA(got) {
		if !strings.HasPrefix(ua, sdkUAPrefix) || !strings.HasSuffix(ua, " my-app/1.2") {
			t.Errorf("%s User-Agent = %q, want %q... my-app/1.2", path, ua, sdkUAPrefix)
		}
	}

	cfg, err := resolveFromEnv(func(k string) string {
		if k == "CAMUNDA_USER_AGENT" {
			return "env-app/3"
		}
		return ""
	}, nil)
	if err != nil {
		t.Fatalf("resolveFromEnv: %v", err)
	}
	if ua := userAgent(cfg); !strings.HasPrefix(ua, sdkUAPrefix) || !strings.HasSuffix(ua, " env-app/3") {
		t.Errorf("env User-Agent = %q", ua)
	}
}

type uaGateway struct {
	pb.UnimplementedGatewayServer
}

func (uaGateway) StreamActivatedJobs(_ *pb.StreamActivatedJobsRequest, stream grpc.ServerStreamingServer[pb.ActivatedJob]) error {
	<-stream.Context().Done()
	return nil
}

// TestUserAgentOnGRPCStream: the gRPC dial puts the SDK identifier first in the
// user-agent metadata seen by the gateway.
func TestUserAgentOnGRPCStream(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	uas := make(chan string, 4)
	srv := grpc.NewServer(grpc.StreamInterceptor(func(s any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		md, _ := metadata.FromIncomingContext(ss.Context())
		uas <- strings.Join(md.Get("user-agent"), ",")
		return h(s, ss)
	}))
	pb.RegisterGatewayServer(srv, uaGateway{})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithNoAuth(), WithGrpcAddress(lis.Addr().String()), WithUserAgent("my-app/1.2"), WithLogLevel(LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conn, err := client.grpcConn()
	if err != nil {
		t.Fatalf("grpcConn: %v", err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := pb.NewGatewayClient(conn).StreamActivatedJobs(ctx, &pb.StreamActivatedJobsRequest{Type: "x"}); err != nil {
		t.Fatalf("stream: %v", err)
	}
	ua := waitFor(t, uas)
	if !strings.HasPrefix(ua, sdkUAPrefix) || !strings.Contains(ua, " my-app/1.2") || !strings.Contains(ua, "grpc-go/") {
		t.Errorf("gRPC user-agent = %q, want %q... my-app/1.2 grpc-go/...", ua, sdkUAPrefix)
	}
}

func TestSDKVersionFromBuildInfo(t *testing.T) {
	if v := sdkVersion(); v == "" {
		t.Error("sdkVersion is empty")
	}
}

func TestVersionFromBuildInfo(t *testing.T) {
	const mod = "example.com/sdk/v11"
	cases := []struct {
		name string
		bi   debug.BuildInfo
		want string
	}{
		{"dep", debug.BuildInfo{Deps: []*debug.Module{{Path: mod, Version: "v11.0.3"}}}, "11.0.3"},
		{"replaced", debug.BuildInfo{Deps: []*debug.Module{{Path: mod, Version: "v11.0.0", Replace: &debug.Module{Version: "v11.0.9"}}}}, "11.0.9"},
		{"main devel", debug.BuildInfo{Main: debug.Module{Path: mod, Version: "(devel)"}}, "development"},
		{"absent", debug.BuildInfo{}, "development"},
	}
	for _, c := range cases {
		if got := versionFromBuildInfo(&c.bi, mod); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

type uaCapture struct{ ua string }

func (c *uaCapture) RoundTrip(r *http.Request) (*http.Response, error) {
	c.ua = r.Header.Get("User-Agent")
	return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
}

// TestUserAgentOnFalconDialer: the FALCON detect/upgrade HTTP client also leads
// with the SDK identifier.
func TestUserAgentOnFalconDialer(t *testing.T) {
	capture := &uaCapture{}
	client, err := New(WithNoAuth(), WithTransport(capture), WithLogLevel(LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d, err := client.buildFalconDialer()
	if err != nil {
		t.Fatalf("buildFalconDialer: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	if !strings.HasPrefix(capture.ua, sdkUAPrefix) {
		t.Errorf("FALCON User-Agent = %q, want prefix %q", capture.ua, sdkUAPrefix)
	}
}
