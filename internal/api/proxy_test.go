package api

import (
	"net"
	"testing"
)

func TestProxyKeyIncludesEnv(t *testing.T) {
	k1 := proxyKey("myproject", "myapp", "staging")
	k2 := proxyKey("myproject", "myapp", "production")

	if k1 == k2 {
		t.Errorf("keys for different environments must differ: staging=%q production=%q", k1, k2)
	}

	pm := newAppProxyManager()
	pm.proxies[k1] = &appProxyEntry{Port: 1234, URL: "http://localhost:1234"}

	if _, ok := pm.proxies[k1]; !ok {
		t.Error("expected staging proxy to exist")
	}
	if _, ok := pm.proxies[k2]; ok {
		t.Error("production proxy should not exist")
	}
}

func TestProxyBindAddressIsLoopback(t *testing.T) {
	ip := net.ParseIP(proxyBindAddress)
	if ip == nil {
		t.Fatalf("proxyBindAddress %q is not a valid IP", proxyBindAddress)
	}
	if !ip.IsLoopback() {
		t.Errorf("proxyBindAddress must be loopback, got %s", proxyBindAddress)
	}
}
