package main

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// defaultProject is the project name seeded during first-user setup. The CLI
// falls back to this when `current_project` is unset so commands behave
// sensibly for freshly-logged-in users.
const defaultProject = "default"

type Config struct {
	ServerURL      string `yaml:"server_url"`
	Token          string `yaml:"token"`
	CurrentProject string `yaml:"current_project"`
}

// Project returns the effective current project, falling back to the seeded
// "default" project when the config has no value set.
func (c *Config) Project() string {
	if c.CurrentProject == "" {
		return defaultProject
	}
	return c.CurrentProject
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mortise", "config.yaml")
}

func loadConfig() (*Config, error) {
	data, err := os.ReadFile(configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func saveConfig(cfg *Config) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}
