// Package templates provides embedded compose-based stack templates.
//
// Each subdirectory is a template containing a docker-compose.yml, an optional
// template.yaml metadata file, and an optional files/ directory for
// volume-mounted content (e.g. init SQL).
package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed all:supabase
var embedded embed.FS

// templateMeta is the optional template.yaml metadata file.
type templateMeta struct {
	Name             string   `yaml:"name"`
	Description      string   `yaml:"description"`
	RequiredServices []string `yaml:"required_services"`
}

// Template holds a resolved template's compose YAML and bundled files.
type Template struct {
	Name        string
	Description string
	Compose     string            // raw docker-compose.yml content
	Files       map[string]string // host path (from volume mounts) -> file content
	Required    []string          // service names that are required
}

// List returns all available template names.
func List() ([]string, error) {
	entries, err := embedded.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("read templates dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Must contain a docker-compose.yml to be a valid template.
		if _, err := embedded.ReadFile(path.Join(e.Name(), "docker-compose.yml")); err == nil {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// Load reads a template by name from the embedded filesystem.
func Load(name string) (*Template, error) {
	composeBytes, err := embedded.ReadFile(path.Join(name, "docker-compose.yml"))
	if err != nil {
		return nil, fmt.Errorf("unknown template %q", name)
	}

	t := &Template{
		Name:    name,
		Compose: string(composeBytes),
		Files:   make(map[string]string),
	}

	// Read optional metadata.
	if metaBytes, err := embedded.ReadFile(path.Join(name, "template.yaml")); err == nil {
		var meta templateMeta
		if err := yaml.Unmarshal(metaBytes, &meta); err == nil {
			if meta.Name != "" {
				t.Name = meta.Name
			}
			t.Description = meta.Description
			t.Required = meta.RequiredServices
		}
	}

	// Read bundled files from the files/ subdirectory.
	filesDir := path.Join(name, "files")
	_ = fs.WalkDir(embedded, filesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		content, err := embedded.ReadFile(p)
		if err != nil {
			return err
		}
		// Map the embedded path to the relative path used in volume mounts.
		// e.g. "supabase/files/init.sql" -> "./files/init.sql"
		relPath := "./" + strings.TrimPrefix(p, name+"/")
		t.Files[relPath] = string(content)
		return nil
	})

	return t, nil
}
