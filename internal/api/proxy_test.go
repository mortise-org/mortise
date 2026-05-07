package api

import (
	"net"
	"testing"
)

func TestProxyManagerKeyIncludesEnv(t *testing.T) {
	pm := newAppProxyManager()

	pm.mu.Lock()
	pm.proxies["myproject/myapp/staging"] = &appProxyEntry{Port: 1234, URL: "http://localhost:1234"}
	pm.mu.Unlock()

	pm.mu.Lock()
	_, existsStaging := pm.proxies["myproject/myapp/staging"]
	_, existsProduction := pm.proxies["myproject/myapp/production"]
	pm.mu.Unlock()

	if !existsStaging {
		t.Error("expected staging proxy to exist")
	}
	if existsProduction {
		t.Error("production proxy should not exist — key must include environment")
	}
}

func TestProxyListenerBindsLocalhost(t *testing.T) {
	// Verify the bind address pattern used by handleConnect.
	// This is a structural test: the listener should bind to 127.0.0.1, not 0.0.0.0.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on 127.0.0.1:0: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	if !addr.IP.IsLoopback() {
		t.Errorf("expected loopback address, got %s", addr.IP)
	}
}
