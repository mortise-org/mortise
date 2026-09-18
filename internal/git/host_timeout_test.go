package git

import (
	"testing"
)

// Every git-host client must carry an HTTP timeout: without one, a hung
// connection blocks the App controller's worker for minutes (CAI-173).
func TestGitHostClientsHaveTimeouts(t *testing.T) {
	gh, err := NewGitHubAPI("", "tok", "sec")
	if err != nil {
		t.Fatal(err)
	}
	if got := gh.client.Client().Timeout; got != HostTimeout {
		t.Errorf("github client timeout = %v, want %v", got, HostTimeout)
	}
	gt, err := NewGiteaAPI("https://gitea.example", "tok", "sec")
	if err != nil {
		t.Fatal(err)
	}
	if gt.hc == nil || gt.hc.Timeout != HostTimeout {
		t.Errorf("gitea client timeout not set")
	}
	gl, err := NewGitLabAPI("", "tok", "sec")
	if err != nil {
		t.Fatal(err)
	}
	if hc := gl.client.HTTPClient(); hc == nil || hc.Timeout != HostTimeout {
		t.Errorf("gitlab client timeout not set")
	}
}
