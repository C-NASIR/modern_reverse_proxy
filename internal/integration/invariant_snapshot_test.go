package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestSnapshotImmutability(t *testing.T) {
	upstreamA := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "A")
	})
	addrA, closeA := testutil.StartUpstream(t, upstreamA)
	defer closeA()

	upstreamB := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "B")
	})
	addrB, closeB := testutil.StartUpstream(t, upstreamB)
	defer closeB()

	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)
	defer reg.Close()
	defer trafficReg.Close()

	initialConfig := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1"}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, addrA)
	cfg, err := config.ParseJSON([]byte(initialConfig))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}

	initialSnap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store := runtime.NewStore(initialSnap)
	oldSnap := store.Get()
	if oldSnap == nil {
		t.Fatalf("expected snapshot")
	}

	updatedConfig := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r2", "host": "example.local", "path_prefix": "/", "pool": "p2"}],
"pools": {"p2": {"endpoints": ["%s"]}}
}`, addrB)
	newCfg, err := config.ParseJSON([]byte(updatedConfig))
	if err != nil {
		t.Fatalf("parse updated config: %v", err)
	}
	newSnap, err := runtime.BuildSnapshot(newCfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build updated snapshot: %v", err)
	}
	if err := store.Swap(newSnap); err != nil {
		t.Fatalf("swap snapshot: %v", err)
	}
	current := store.Get()
	if current == nil {
		t.Fatalf("expected current snapshot")
	}
	if current == oldSnap {
		t.Fatalf("expected new snapshot pointer")
	}
	if _, ok := oldSnap.Pools["p1"]; !ok {
		t.Fatalf("expected old snapshot pool p1")
	}
	if _, ok := oldSnap.Pools["p2"]; ok {
		t.Fatalf("expected old snapshot to remain unchanged")
	}
	oldReq, _ := http.NewRequest(http.MethodGet, "http://example.local/", nil)
	oldReq.Host = "example.local"
	if route, ok := oldSnap.Router.Match(oldReq); !ok || route.ID != "r1" {
		t.Fatalf("expected old snapshot to match r1")
	}
	newReq, _ := http.NewRequest(http.MethodGet, "http://example.local/", nil)
	newReq.Host = "example.local"
	if route, ok := current.Router.Match(newReq); !ok || route.ID != "r2" {
		t.Fatalf("expected new snapshot to match r2")
	}
}

type snapshotPhaseRecorder struct {
	mu     sync.Mutex
	phases map[string]uint64
}

func (r *snapshotPhaseRecorder) ObserveSnapshot(phase string, snapshot *runtime.Snapshot) {
	if snapshot == nil || phase == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phases == nil {
		r.phases = make(map[string]uint64)
	}
	r.phases[phase] = snapshot.ID
}

func TestSnapshotSingleObservation(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)
	defer reg.Close()
	defer trafficReg.Close()

	cfg := &config.Config{
		Routes: []config.Route{{ID: "r1", Host: "example.local", PathPrefix: "/", Pool: "p1"}},
		Pools:  map[string]config.Pool{"p1": {Endpoints: []string{addr}}},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store := runtime.NewStore(snap)
	recorder := &snapshotPhaseRecorder{}
	proxyHandler := &proxy.Handler{
		Store:            store,
		Registry:         reg,
		Engine:           proxy.NewEngine(reg, nil, nil, nil, nil),
		SnapshotObserver: recorder,
	}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.phases) == 0 {
		t.Fatalf("expected snapshot observations")
	}
	routeID, ok := recorder.phases[proxy.SnapshotPhaseRouteMatch]
	if !ok {
		t.Fatalf("missing route match observation")
	}
	upstreamID, ok := recorder.phases[proxy.SnapshotPhaseUpstreamPick]
	if !ok {
		t.Fatalf("missing upstream pick observation")
	}
	responseID, ok := recorder.phases[proxy.SnapshotPhaseResponseWrite]
	if !ok {
		t.Fatalf("missing response write observation")
	}
	if routeID != upstreamID || routeID != responseID {
		t.Fatalf("expected consistent snapshot IDs, got %d %d %d", routeID, upstreamID, responseID)
	}
}

func TestRetiredSnapshotLifecycle(t *testing.T) {
	var upstreamCount atomic.Int32
	startedFirst := make(chan struct{})
	startedSecond := make(chan struct{})
	blockFirst := make(chan struct{})
	blockSecond := make(chan struct{})

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := upstreamCount.Add(1)
		switch count {
		case 1:
			close(startedFirst)
			<-blockFirst
		case 2:
			close(startedSecond)
			<-blockSecond
		}
		w.WriteHeader(http.StatusOK)
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)
	defer reg.Close()
	defer trafficReg.Close()

	baseConfig := func(routeID string) *config.Config {
		return &config.Config{
			Routes: []config.Route{{ID: routeID, Host: "example.local", PathPrefix: "/", Pool: "p1"}},
			Pools:  map[string]config.Pool{"p1": {Endpoints: []string{addr}}},
		}
	}

	firstSnap, err := runtime.BuildSnapshot(baseConfig("r1"), reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store := runtime.NewStore(firstSnap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil, nil, nil)}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 4 * time.Second}
	respErr := make(chan error, 2)

	go func() {
		resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		if resp.StatusCode != http.StatusOK {
			respErr <- fmt.Errorf("first request status %d", resp.StatusCode)
			return
		}
		respErr <- nil
	}()

	<-startedFirst
	secondSnap, err := runtime.BuildSnapshot(baseConfig("r2"), reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build second snapshot: %v", err)
	}
	if err := store.Swap(secondSnap); err != nil {
		t.Fatalf("swap snapshot: %v", err)
	}

	go func() {
		resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		if resp.StatusCode != http.StatusOK {
			respErr <- fmt.Errorf("second request status %d", resp.StatusCode)
			return
		}
		respErr <- nil
	}()

	<-startedSecond
	thirdSnap, err := runtime.BuildSnapshot(baseConfig("r3"), reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build third snapshot: %v", err)
	}
	if err := store.Swap(thirdSnap); err != nil {
		t.Fatalf("swap snapshot: %v", err)
	}

	retired := store.RetiredSnapshots()
	if len(retired) < 2 {
		t.Fatalf("expected retired snapshots to accumulate")
	}
	for _, info := range retired {
		if info.RefCount == 0 {
			t.Fatalf("expected retired snapshot refcount to be > 0")
		}
	}

	close(blockFirst)
	close(blockSecond)
	for i := 0; i < 2; i++ {
		if err := <-respErr; err != nil {
			t.Fatalf("request error: %v", err)
		}
	}

	testutil.Eventually(t, 2*time.Second, 20*time.Millisecond, func() error {
		if count := store.RetiredCount(); count != 0 {
			return fmt.Errorf("expected retired count 0, got %d", count)
		}
		if len(store.RetiredSnapshots()) != 0 {
			return fmt.Errorf("expected no retained snapshots")
		}
		return nil
	})
}
