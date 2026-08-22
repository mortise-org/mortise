package helpers

import (
	"net"
	"testing"
	"time"
)

// startFlakyListener mimics what kubectl port-forward actually does: it binds
// and accepts connections immediately, but for resetFor the tunnel to the pod
// isn't up yet, so every connection gets a hard RST instead of a response.
// After resetFor elapses it starts serving real (if minimal) HTTP responses.
func startFlakyListener(t *testing.T, resetFor time.Duration) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	deadline := time.Now().Add(resetFor)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			if time.Now().Before(deadline) {
				if tc, ok := conn.(*net.TCPConn); ok {
					_ = tc.SetLinger(0) // force RST on close, like a dead tunnel
				}
				_ = conn.Close()
				continue
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				buf := make([]byte, 1024)
				_, _ = c.Read(buf) // drain the request; content doesn't matter
				_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
			}(conn)
		}
	}()

	return ln.Addr().String()
}

// TestWaitForTunnelSurvivesInitialResets reproduces the CAI-173 race: a
// listener that behaves exactly like a not-yet-tunneling kubectl port-forward
// (accepting connections but resetting them) for a fixed window, then
// starting to actually serve. A correct readiness check must not return
// until it has proxied a real request; a dial-only check returns as soon as
// the TCP handshake completes, which happens during the reset window.
func TestWaitForTunnelSurvivesInitialResets(t *testing.T) {
	const resetWindow = 1 * time.Second
	addr := startFlakyListener(t, resetWindow)

	start := time.Now()
	waitForTunnel(t, addr, 5*time.Second)
	elapsed := time.Since(start)

	if elapsed < resetWindow {
		t.Fatalf("waitForTunnel returned after %v, before the reset window (%v) elapsed — "+
			"it accepted a dead tunnel as ready", elapsed, resetWindow)
	}
}
