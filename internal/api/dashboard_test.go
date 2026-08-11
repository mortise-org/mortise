package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/auth"
)

type dashboardTestResponse struct {
	Cluster struct {
		Projects         int            `json:"projects"`
		Apps             int            `json:"apps"`
		AppsByPhase      map[string]int `json:"appsByPhase"`
		BuildsRunning    int            `json:"buildsRunning"`
		BuildsQueued     int            `json:"buildsQueued"`
		MetricsAvailable bool           `json:"metricsAvailable"`
		CPUAllocatable   *float64       `json:"cpuAllocatable"`
	} `json:"cluster"`
	Projects []struct {
		Name      string            `json:"name"`
		AppCount  int               `json:"appCount"`
		EnvHealth map[string]string `json:"envHealth"`
	} `json:"projects"`
	Apps []struct {
		Project string `json:"project"`
		Name    string `json:"name"`
		Phase   string `json:"phase"`
		Envs    []struct {
			Name  string `json:"name"`
			Phase string `json:"phase"`
		} `json:"envs"`
		MetricsAvailable bool `json:"metricsAvailable"`
	} `json:"apps"`
}

func TestDashboardAdminSeesRollup(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	ns := seedProject(t, k8sClient, "dash-a")
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "dash-web", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.27"},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	// A queued BuildRun in the readable project counts toward the strip.
	br := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "dash-br", Namespace: ns},
		Spec: mortisev1alpha1.BuildRunSpec{
			AppName:     "dash-web",
			Environment: "production",
			TargetRef:   mortisev1alpha1.BuildRunTargetRef{Kind: mortisev1alpha1.BuildRunTargetAppEnvironment, Name: "dash-web", Namespace: ns},
		},
	}
	if err := k8sClient.Create(context.Background(), br); err != nil {
		t.Fatalf("create buildrun: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/dashboard", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp dashboardTestResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Cluster.Projects < 1 || resp.Cluster.Apps < 1 {
		t.Fatalf("cluster counts too small: %+v", resp.Cluster)
	}
	if resp.Cluster.BuildsQueued < 1 {
		t.Fatalf("expected the pending BuildRun in buildsQueued, got %+v", resp.Cluster)
	}
	// No adapter configured in envtest — usage degrades, never errors.
	if resp.Cluster.MetricsAvailable {
		t.Fatal("metricsAvailable must be false without an adapter")
	}

	foundProject, foundApp := false, false
	for _, p := range resp.Projects {
		if p.Name == "dash-a" {
			foundProject = true
			if _, ok := p.EnvHealth["production"]; !ok {
				t.Fatalf("project env health missing production: %+v", p)
			}
		}
	}
	for _, a := range resp.Apps {
		if a.Project == "dash-a" && a.Name == "dash-web" {
			foundApp = true
			if len(a.Envs) == 0 || a.Envs[0].Name != "production" {
				t.Fatalf("app row envs = %+v", a.Envs)
			}
		}
	}
	if !foundProject || !foundApp {
		t.Fatalf("rollup missing seeded rows: project=%v app=%v", foundProject, foundApp)
	}
}

func TestDashboardMemberScopedAndNoAdminFields(t *testing.T) {
	k8sClient := setupEnvtest(t)

	seedProject(t, k8sClient, "dash-mine")
	seedProject(t, k8sClient, "dash-other")

	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	seedProjectMember(t, k8sClient, "dash-mine", "member@example.com", mortisev1alpha1.ProjectRoleDeveloper)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/dashboard", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp dashboardTestResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, p := range resp.Projects {
		if p.Name == "dash-other" {
			t.Fatal("member must not see non-member projects in the rollup")
		}
	}
	if resp.Cluster.CPUAllocatable != nil {
		t.Fatal("node capacity is admin-only and must be omitted for members")
	}
}
