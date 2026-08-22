package helpers

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"testing"
	"time"
)

// PortForward starts a `kubectl port-forward svc/<svc> :<remotePort>` against the
// given namespace/service and returns the chosen local port. The forwarder is
// killed when the test finishes. kubectl picks a free local port (via "0:remote"
// syntax) so tests don't race over fixed ports.
//
// We shell out to kubectl rather than embed client-go's portforward package
// because the APIs required for upgrade-aware port forwarding have shifted
// across client-go releases and kubectl already handles the churn. The tool
// is a hard dependency of `make test-integration` anyway (used by the chart
// install steps), so assuming it's present is safe.
func PortForward(t *testing.T, namespace, service string, remotePort int) int {
	t.Helper()

	localPort, err := pickFreePort()
	if err != nil {
		t.Fatalf("portforward: pick free port: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	spec := fmt.Sprintf("%d:%d", localPort, remotePort)
	cmd := exec.CommandContext(ctx, "kubectl",
		"-n", namespace,
		"port-forward",
		"svc/"+service,
		spec,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("portforward: kubectl start: %v", err)
	}

	// Wait until the tunnel actually proxies traffic to the pod, not just
	// until kubectl has bound the local port.
	addr := fmt.Sprintf("127.0.0.1:%d", localPort)
	waitForTunnel(t, addr, 30*time.Second)

	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	return localPort
}

// waitForTunnel blocks until addr answers a full HTTP round trip, or fails
// the test once timeout elapses.
//
// kubectl port-forward binds and listens on the local port immediately, then
// separately establishes the tunnel to the pod — the two are not atomic. A
// bare TCP dial succeeds as soon as kubectl has bound the socket, which
// proves kubectl is running but not that anything is reachable through the
// tunnel: the very next read can come back "connection reset by peer" while
// the tunnel is still coming up. Only a completed HTTP response proves the
// path end to end, so probe with a real request instead of a dial.
func waitForTunnel(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		err := probeTunnel(client, addr)
		if err == nil {
			return
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("portforward: tunnel to %s never served a request within %s: %v", addr, timeout, lastErr)
}

// probeTunnel issues a single HTTP request over addr and reports whether a
// full response came back. The request path and status code don't matter —
// even a 404 proves the tunnel round-trips — only whether the round trip
// completed without a transport error.
func probeTunnel(client *http.Client, addr string) error {
	resp, err := client.Get("http://" + addr + "/")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
