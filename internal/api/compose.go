package api

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"gopkg.in/yaml.v3"
)

// ComposeFile is the top-level docker-compose.yml structure.
type ComposeFile struct {
	Services map[string]ComposeService `yaml:"services"`
}

// ComposeService captures the subset of docker-compose service fields that
// Mortise maps to App specs.
type ComposeService struct {
	Image       string      `yaml:"image"`
	Ports       []string    `yaml:"ports"`
	Environment interface{} `yaml:"environment"` // map[string]string or []string ("KEY=VAL")
	DependsOn   interface{} `yaml:"depends_on"`  // []string or map[string]{condition}
	Restart     string      `yaml:"restart"`
	Command     interface{} `yaml:"command"` // string or []string
	Volumes     []string    `yaml:"volumes"` // "host:container" or "host:container:ro"
}

// parseCompose parses a docker-compose YAML string.
func parseCompose(yamlStr string) (*ComposeFile, error) {
	var cf ComposeFile
	if err := yaml.Unmarshal([]byte(yamlStr), &cf); err != nil {
		return nil, fmt.Errorf("invalid compose YAML: %w", err)
	}
	if len(cf.Services) == 0 {
		return nil, fmt.Errorf("compose file has no services")
	}
	return &cf, nil
}

// AppSpec wraps the info needed to create an app from a compose service.
type appSpec struct {
	Name    string
	Spec    mortisev1alpha1.AppSpec
	DepsOn  []string // service names this depends on
	IsInit  bool     // restart: "no" — skip creation
	Service string   // original compose service name
}

// composeToAppSpecs converts a parsed ComposeFile into ordered app specs.
// stackPrefix is prepended to service names (e.g. "supabase" -> "supabase-postgres").
// bundledFiles maps host paths from volume mounts to file content (for templates).
func composeToAppSpecs(compose *ComposeFile, stackPrefix string, bundledFiles map[string]string) ([]appSpec, error) {
	specs := make(map[string]*appSpec, len(compose.Services))

	for svcName, svc := range compose.Services {
		appName := svcName
		if stackPrefix != "" {
			appName = stackPrefix + "-" + svcName
		}

		if svc.Image == "" {
			return nil, fmt.Errorf("service %q: image is required", svcName)
		}

		as := &appSpec{
			Name:    appName,
			Service: svcName,
			IsInit:  svc.Restart == "no",
			DepsOn:  parseDependsOn(svc.DependsOn),
		}

		// Build the Mortise AppSpec.
		var port int32 = 8080
		if len(svc.Ports) > 0 {
			p, err := parsePort(svc.Ports[0])
			if err != nil {
				return nil, fmt.Errorf("service %q: invalid port %q: %w", svcName, svc.Ports[0], err)
			}
			port = p
		}

		envVars := parseEnvironment(svc.Environment)

		// Parse volume mounts. If the host path is in bundledFiles, create
		// a ConfigFile mount (ConfigMap). Otherwise treat as a PVC or skip.
		var configFiles []mortisev1alpha1.ConfigFile
		for _, vol := range svc.Volumes {
			parts := strings.SplitN(vol, ":", 2)
			if len(parts) != 2 {
				continue
			}
			hostPath := parts[0]
			containerPath := strings.TrimSuffix(parts[1], ":ro")

			// Check if the host path has bundled content (from a template).
			if content, ok := bundledFiles[hostPath]; ok && content != "" {
				configFiles = append(configFiles, mortisev1alpha1.ConfigFile{
					Path:    containerPath,
					Content: content,
				})
			}
			// Named volumes or unresolved file paths are skipped for now.
			// Future: PVC for named volumes, git repo lookup for file paths.
		}

		as.Spec = mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:  mortisev1alpha1.SourceTypeImage,
				Image: svc.Image,
			},
			Network: mortisev1alpha1.NetworkConfig{
				Public: false,
				Port:   port,
			},
			ConfigFiles: configFiles,
			Environments: []mortisev1alpha1.Environment{{
				Name: "production",
				Env:  envVars,
			}},
		}

		specs[svcName] = as
	}

	// Topological sort by depends_on.
	ordered, err := topoSort(specs)
	if err != nil {
		return nil, err
	}
	return ordered, nil
}

// parseDependsOn normalizes the depends_on field to a list of service names.
func parseDependsOn(v interface{}) []string {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case []interface{}:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case map[string]interface{}:
		out := make([]string, 0, len(val))
		for k := range val {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	return nil
}

// parsePort extracts the container port from a compose port mapping like "5432:5432".
func parsePort(portStr string) (int32, error) {
	// Handle "host:container" or just "container"
	parts := strings.Split(portStr, ":")
	target := parts[len(parts)-1]
	// Strip protocol suffix like "/tcp"
	target = strings.Split(target, "/")[0]
	p, err := strconv.ParseInt(target, 10, 32)
	if err != nil {
		return 0, err
	}
	return int32(p), nil
}

// parseEnvironment normalizes the compose environment field to Mortise EnvVars.
func parseEnvironment(v interface{}) []mortisev1alpha1.EnvVar {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case map[string]interface{}:
		out := make([]mortisev1alpha1.EnvVar, 0, len(val))
		// Sort for deterministic output.
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, mortisev1alpha1.EnvVar{
				Name:  k,
				Value: fmt.Sprintf("%v", val[k]),
			})
		}
		return out
	case []interface{}:
		out := make([]mortisev1alpha1.EnvVar, 0, len(val))
		for _, item := range val {
			s, ok := item.(string)
			if !ok {
				continue
			}
			k, v, _ := strings.Cut(s, "=")
			out = append(out, mortisev1alpha1.EnvVar{Name: k, Value: v})
		}
		return out
	}
	return nil
}

// topoSort returns specs in dependency order (dependencies first).
func topoSort(specs map[string]*appSpec) ([]appSpec, error) {
	// Build adjacency: service name -> list of dependents.
	visited := map[string]int{} // 0=unvisited, 1=in-progress, 2=done
	var result []appSpec

	var visit func(name string) error
	visit = func(name string) error {
		if visited[name] == 2 {
			return nil
		}
		if visited[name] == 1 {
			return fmt.Errorf("circular dependency involving %q", name)
		}
		visited[name] = 1
		spec, ok := specs[name]
		if !ok {
			return fmt.Errorf("unknown service dependency %q", name)
		}
		for _, dep := range spec.DepsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		visited[name] = 2
		result = append(result, *spec)
		return nil
	}

	// Sort service names for deterministic order.
	names := make([]string, 0, len(specs))
	for n := range specs {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return result, nil
}
