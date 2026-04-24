package api

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

// TestParsePortRejectsMalformed verifies that malformed port specs are
// surfaced as errors rather than silently falling back to a default.
func TestParsePortRejectsMalformed(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"non-numeric", "foo:bar"},
		{"trailing garbage", "5432:abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parsePort(tc.input); err == nil {
				t.Errorf("expected error for %q, got nil", tc.input)
			}
		})
	}
}

// TestComposeToAppSpecsRejectsMalformedPort verifies that parseCompose +
// composeToAppSpecs bubbles up a parse-port failure instead of silently
// using the default 8080.
func TestComposeToAppSpecsRejectsMalformedPort(t *testing.T) {
	yaml := `services:
  web:
    image: nginx:1.25
    ports: ["bogus:bogus"]
`
	cf, err := parseCompose(yaml)
	if err != nil {
		t.Fatalf("parseCompose should accept the YAML, got: %v", err)
	}
	if _, err := composeToAppSpecs(cf, "stack", nil); err == nil {
		t.Fatal("expected composeToAppSpecs to reject malformed port, got nil")
	}
}

func TestIsNamedVolume(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"db_data", true},
		{"redis-cache", true},
		{"./data", false},
		{"../data", false},
		{"/var/lib/data", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			if got := isNamedVolume(tc.input); got != tc.want {
				t.Errorf("isNamedVolume(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestComposeNamedVolumesCreateStorage(t *testing.T) {
	yml := `services:
  db:
    image: postgres:16
    ports: ["5432:5432"]
    volumes:
      - db_data:/var/lib/postgresql/data
`
	cf, err := parseCompose(yml)
	if err != nil {
		t.Fatalf("parseCompose: %v", err)
	}
	specs, err := composeToAppSpecs(cf, "mystack", nil)
	if err != nil {
		t.Fatalf("composeToAppSpecs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	storage := specs[0].Spec.Storage
	if len(storage) != 1 {
		t.Fatalf("expected 1 storage entry, got %d", len(storage))
	}
	if storage[0].Name != "db_data" {
		t.Errorf("storage name = %q, want %q", storage[0].Name, "db_data")
	}
	if storage[0].MountPath != "/var/lib/postgresql/data" {
		t.Errorf("mountPath = %q, want %q", storage[0].MountPath, "/var/lib/postgresql/data")
	}
	expectedSize := resource.MustParse("1Gi")
	if storage[0].Size.Cmp(expectedSize) != 0 {
		t.Errorf("size = %s, want %s", storage[0].Size.String(), expectedSize.String())
	}
}

func TestComposeBundledFileNotDuplicatedAsVolume(t *testing.T) {
	yml := `services:
  db:
    image: postgres:16
    ports: ["5432:5432"]
    volumes:
      - ./files/init.sql:/docker-entrypoint-initdb.d/init.sql
      - db_data:/var/lib/postgresql/data
`
	bundled := map[string]string{
		"./files/init.sql": "CREATE TABLE t();",
	}
	cf, err := parseCompose(yml)
	if err != nil {
		t.Fatalf("parseCompose: %v", err)
	}
	specs, err := composeToAppSpecs(cf, "stack", bundled)
	if err != nil {
		t.Fatalf("composeToAppSpecs: %v", err)
	}
	spec := specs[0].Spec
	if len(spec.ConfigFiles) != 1 {
		t.Fatalf("expected 1 configFile, got %d", len(spec.ConfigFiles))
	}
	if len(spec.Storage) != 1 {
		t.Fatalf("expected 1 storage, got %d", len(spec.Storage))
	}
	if spec.Storage[0].Name != "db_data" {
		t.Errorf("storage name = %q, want %q", spec.Storage[0].Name, "db_data")
	}
}

func TestComposeMultipleNamedVolumes(t *testing.T) {
	yml := `services:
  app:
    image: myapp:latest
    ports: ["8080:8080"]
    volumes:
      - app_data:/data
      - app_cache:/cache
`
	cf, err := parseCompose(yml)
	if err != nil {
		t.Fatalf("parseCompose: %v", err)
	}
	specs, err := composeToAppSpecs(cf, "stack", nil)
	if err != nil {
		t.Fatalf("composeToAppSpecs: %v", err)
	}
	storage := specs[0].Spec.Storage
	if len(storage) != 2 {
		t.Fatalf("expected 2 storage entries, got %d", len(storage))
	}
	names := map[string]string{}
	for _, s := range storage {
		names[s.Name] = s.MountPath
	}
	if names["app_data"] != "/data" {
		t.Errorf("app_data mountPath = %q, want %q", names["app_data"], "/data")
	}
	if names["app_cache"] != "/cache" {
		t.Errorf("app_cache mountPath = %q, want %q", names["app_cache"], "/cache")
	}
}

func TestComposeBindMountPathsSkipped(t *testing.T) {
	yml := `services:
  web:
    image: nginx:1.25
    ports: ["80:80"]
    volumes:
      - /host/path:/container/path
      - ../relative:/other
`
	cf, err := parseCompose(yml)
	if err != nil {
		t.Fatalf("parseCompose: %v", err)
	}
	specs, err := composeToAppSpecs(cf, "stack", nil)
	if err != nil {
		t.Fatalf("composeToAppSpecs: %v", err)
	}
	if len(specs[0].Spec.Storage) != 0 {
		t.Errorf("expected no storage for bind mounts, got %d", len(specs[0].Spec.Storage))
	}
}

func TestConvertComposeCPU(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"1.0", "1000m"},
		{"0.5", "500m"},
		{"2", "2000m"},
		{"0.25", "250m"},
		{"", ""},
		{"bogus", ""},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			if got := convertComposeCPU(tc.input); got != tc.want {
				t.Errorf("convertComposeCPU(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestConvertComposeMemory(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"512M", "512Mi"},
		{"2G", "2Gi"},
		{"1024K", "1024Ki"},
		{"512Mi", "512Mi"},
		{"2Gi", "2Gi"},
		{"256m", "256Mi"},
		{"1g", "1Gi"},
		{"1024", "1024"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			if got := convertComposeMemory(tc.input); got != tc.want {
				t.Errorf("convertComposeMemory(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestComposeDeployResources(t *testing.T) {
	yml := `services:
  web:
    image: nginx:1.25
    ports: ["80:80"]
    deploy:
      resources:
        limits:
          cpus: "0.5"
          memory: "512M"
`
	cf, err := parseCompose(yml)
	if err != nil {
		t.Fatalf("parseCompose: %v", err)
	}
	specs, err := composeToAppSpecs(cf, "stack", nil)
	if err != nil {
		t.Fatalf("composeToAppSpecs: %v", err)
	}
	res := specs[0].Spec.Environments[0].Resources
	if res.CPU != "500m" {
		t.Errorf("CPU = %q, want %q", res.CPU, "500m")
	}
	if res.Memory != "512Mi" {
		t.Errorf("Memory = %q, want %q", res.Memory, "512Mi")
	}
}

func TestComposeDependsOnCreatesBindings(t *testing.T) {
	yml := `services:
  db:
    image: postgres:16
    ports: ["5432:5432"]
  cache:
    image: redis:7
    ports: ["6379:6379"]
  web:
    image: myapp:latest
    ports: ["8080:8080"]
    depends_on:
      - db
      - cache
`
	cf, err := parseCompose(yml)
	if err != nil {
		t.Fatalf("parseCompose: %v", err)
	}
	specs, err := composeToAppSpecs(cf, "mystack", nil)
	if err != nil {
		t.Fatalf("composeToAppSpecs: %v", err)
	}

	var webSpec *appSpec
	for i := range specs {
		if specs[i].Service == "web" {
			webSpec = &specs[i]
			break
		}
	}
	if webSpec == nil {
		t.Fatal("web spec not found")
	}

	bindings := webSpec.Spec.Environments[0].Bindings
	if len(bindings) != 2 {
		t.Fatalf("expected 2 bindings, got %d", len(bindings))
	}

	refs := map[string]bool{}
	for _, b := range bindings {
		refs[b.Ref] = true
	}
	if !refs["mystack-db"] {
		t.Error("expected binding ref mystack-db")
	}
	if !refs["mystack-cache"] {
		t.Error("expected binding ref mystack-cache")
	}

	// Services without depends_on should have no bindings.
	for i := range specs {
		if specs[i].Service == "db" || specs[i].Service == "cache" {
			if len(specs[i].Spec.Environments[0].Bindings) != 0 {
				t.Errorf("service %q should have no bindings", specs[i].Service)
			}
		}
	}
}

func TestComposeDependsOnNoPrefix(t *testing.T) {
	yml := `services:
  db:
    image: postgres:16
    ports: ["5432:5432"]
  web:
    image: myapp:latest
    ports: ["8080:8080"]
    depends_on:
      - db
`
	cf, err := parseCompose(yml)
	if err != nil {
		t.Fatalf("parseCompose: %v", err)
	}
	specs, err := composeToAppSpecs(cf, "", nil)
	if err != nil {
		t.Fatalf("composeToAppSpecs: %v", err)
	}

	var webSpec *appSpec
	for i := range specs {
		if specs[i].Service == "web" {
			webSpec = &specs[i]
			break
		}
	}
	if webSpec == nil {
		t.Fatal("web spec not found")
	}

	bindings := webSpec.Spec.Environments[0].Bindings
	if len(bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(bindings))
	}
	if bindings[0].Ref != "db" {
		t.Errorf("binding ref = %q, want %q", bindings[0].Ref, "db")
	}
}

func TestComposeDeployReservationsFallback(t *testing.T) {
	yml := `services:
  web:
    image: nginx:1.25
    ports: ["80:80"]
    deploy:
      resources:
        reservations:
          cpus: "1.0"
          memory: "1G"
`
	cf, err := parseCompose(yml)
	if err != nil {
		t.Fatalf("parseCompose: %v", err)
	}
	specs, err := composeToAppSpecs(cf, "stack", nil)
	if err != nil {
		t.Fatalf("composeToAppSpecs: %v", err)
	}
	res := specs[0].Spec.Environments[0].Resources
	if res.CPU != "1000m" {
		t.Errorf("CPU = %q, want %q", res.CPU, "1000m")
	}
	if res.Memory != "1Gi" {
		t.Errorf("Memory = %q, want %q", res.Memory, "1Gi")
	}
}
