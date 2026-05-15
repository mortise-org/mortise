package constants

import "testing"

func TestCanonicalRepoKey(t *testing.T) {
	tests := []struct {
		name string
		repo string
		want string
	}{
		{name: "https url", repo: "https://github.com/org/repo.git", want: "org/repo"},
		{name: "host path", repo: "github.com/org/repo", want: "org/repo"},
		{name: "owner repo", repo: "org/repo", want: "org/repo"},
		{name: "ssh style", repo: "git@github.com:org/repo.git", want: "org/repo"},
		{name: "case normalized", repo: "https://GitHub.com/Org/Repo/", want: "org/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanonicalRepoKey(tt.repo); got != tt.want {
				t.Fatalf("CanonicalRepoKey(%q) = %q, want %q", tt.repo, got, tt.want)
			}
		})
	}
}

func TestPreviewEnvironmentName_CanonicalizesRepoVariants(t *testing.T) {
	urlName := PreviewEnvironmentName("https://github.com/org/foo.bar", 42, true)
	shortName := PreviewEnvironmentName("org/foo.bar", 42, true)
	sshName := PreviewEnvironmentName("git@github.com:org/foo.bar.git", 42, true)

	if urlName != shortName || shortName != sshName {
		t.Fatalf("expected equivalent repo variants to share preview name, got %q %q %q", urlName, shortName, sshName)
	}
}
