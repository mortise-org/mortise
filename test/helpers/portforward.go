package helpers

import (
	"context"
	"fmt"
	"net"
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

	// Wait until the local port is accepting connections.
	addr := fmt.Sprintf("127.0.0.1:%d", localPort)
	RequireEventually(t, 30*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	})

	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	return localPort
}

func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
