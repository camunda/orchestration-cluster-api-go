package camunda

import (
	"context"
	"net/http"
	"sync"
	"testing"
)

func newPoolTestClient(t *testing.T, env map[string]string, opts ...Option) *CamundaClient {
	t.Helper()
	cfg, err := loadConfig(envFrom(env), nil, opts...)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	c, err := newFromConfig(cfg)
	if err != nil {
		t.Fatalf("newFromConfig: %v", err)
	}
	return c
}

func noopHandler(context.Context, *Job) (map[string]any, error) { return nil, nil }

type poolTestRT struct{}

func (poolTestRT) RoundTrip(*http.Request) (*http.Response, error) { return nil, http.ErrNotSupported }

// TestPoolGrowsWithWorkerCapacity pins #196: workers totalling more than the
// default pool get an idle pool at least as large as their in-flight capacity.
func TestPoolGrowsWithWorkerCapacity(t *testing.T) {
	c := newPoolTestClient(t, map[string]string{})
	if c.pool == nil {
		t.Fatal("SDK-owned transport must be sizable")
	}
	for i := 0; i < 50; i++ {
		c.NewJobWorker("t", noopHandler, WithMaxConcurrentJobs(32))
	}
	tr := c.pool.transport()
	if tr.MaxIdleConnsPerHost < 50*32 || tr.MaxIdleConns < 50*32 {
		t.Errorf("pool = %d/%d, want >= %d", tr.MaxIdleConns, tr.MaxIdleConnsPerHost, 50*32)
	}
	c.NewStreamJobWorker("s", noopHandler, WithStreamMaxConcurrentJobs(400))
	if got := c.pool.transport().MaxIdleConnsPerHost; got < 50*32+400 {
		t.Errorf("per-host = %d after stream worker, want >= %d", got, 50*32+400)
	}
}

func TestPoolKeepsDefaultFloor(t *testing.T) {
	c := newPoolTestClient(t, map[string]string{})
	c.NewJobWorker("t", noopHandler, WithMaxConcurrentJobs(4))
	tr := c.pool.transport()
	if tr.MaxIdleConns != defaultMaxIdleConns || tr.MaxIdleConnsPerHost != defaultMaxIdleConnsPerHost {
		t.Errorf("pool = %d/%d, want default floor", tr.MaxIdleConns, tr.MaxIdleConnsPerHost)
	}
}

func TestPoolExplicitSettingsWin(t *testing.T) {
	cases := map[string]*CamundaClient{
		"options": newPoolTestClient(t, map[string]string{}, WithMaxIdleConns(64), WithMaxIdleConnsPerHost(128)),
		"env": newPoolTestClient(t, map[string]string{
			"CAMUNDA_SDK_HTTP_MAX_IDLE_CONNS":          "64",
			"CAMUNDA_SDK_HTTP_MAX_IDLE_CONNS_PER_HOST": "128",
		}),
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 20; i++ {
				c.NewJobWorker("t", noopHandler, WithMaxConcurrentJobs(32))
			}
			tr := c.pool.transport()
			if tr.MaxIdleConns != 64 || tr.MaxIdleConnsPerHost != 128 {
				t.Errorf("pool = %d/%d, want 64/128", tr.MaxIdleConns, tr.MaxIdleConnsPerHost)
			}
		})
	}
}

// An explicit CAMUNDA_SDK_HTTP_MAX_IDLE_CONNS=0 means "no global idle limit" (as
// documented), not the 256 default: it is pinned at 0 while the per-host pool
// still grows to cover the workers.
func TestPoolEnvZeroGlobalMeansUnlimited(t *testing.T) {
	c := newPoolTestClient(t, map[string]string{"CAMUNDA_SDK_HTTP_MAX_IDLE_CONNS": "0"})
	for i := 0; i < 50; i++ {
		c.NewJobWorker("t", noopHandler, WithMaxConcurrentJobs(32))
	}
	tr := c.pool.transport()
	if tr.MaxIdleConns != 0 {
		t.Errorf("global = %d, want 0 (unlimited)", tr.MaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost < 50*32 {
		t.Errorf("per-host = %d, want grown to >= %d", tr.MaxIdleConnsPerHost, 50*32)
	}
}

func TestPoolOneDimensionExplicit(t *testing.T) {
	c := newPoolTestClient(t, map[string]string{}, WithMaxIdleConnsPerHost(100))
	for i := 0; i < 20; i++ {
		c.NewJobWorker("t", noopHandler, WithMaxConcurrentJobs(32))
	}
	tr := c.pool.transport()
	if tr.MaxIdleConnsPerHost != 100 {
		t.Errorf("per-host = %d, want pinned 100", tr.MaxIdleConnsPerHost)
	}
	if tr.MaxIdleConns < 20*32 {
		t.Errorf("global = %d, want grown to >= %d", tr.MaxIdleConns, 20*32)
	}
}

func TestPoolUserTransportUntouched(t *testing.T) {
	c := newPoolTestClient(t, map[string]string{}, WithTransport(poolTestRT{}))
	if c.pool != nil {
		t.Fatal("user-supplied transport must not be wrapped")
	}
	c.NewJobWorker("t", noopHandler, WithMaxConcurrentJobs(1000)) // must not panic
}

func TestPoolConcurrentGrowthRaceFree(t *testing.T) {
	c := newPoolTestClient(t, map[string]string{})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.NewJobWorker("t", noopHandler, WithMaxConcurrentJobs(16))
			_ = c.pool.transport().MaxIdleConnsPerHost
		}()
	}
	wg.Wait()
	if got := c.pool.transport().MaxIdleConnsPerHost; got < 32*17 {
		t.Errorf("per-host = %d, want >= %d", got, 32*17)
	}
}
