package api

import (
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"gopkg.in/yaml.v3"
)

// VarSpec describes how a template variable should be generated.
type VarSpec struct {
	Generate string         `yaml:"generate"`
	Length   int            `yaml:"length,omitempty"`
	Claims   map[string]any `yaml:"claims,omitempty"`
	SignWith string         `yaml:"sign_with,omitempty"`
}

// MortiseExtension is the x-mortise block in a compose file.
type MortiseExtension struct {
	Variables map[string]VarSpec `yaml:"variables"`
}

// parseMortiseExtension extracts the x-mortise block from raw compose YAML
// and returns the extension plus the YAML with x-mortise stripped out.
func parseMortiseExtension(composeYAML string) (*MortiseExtension, string, error) {
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(composeYAML), &raw); err != nil {
		return nil, composeYAML, nil
	}

	xm, ok := raw["x-mortise"]
	if !ok {
		return nil, composeYAML, nil
	}

	// Re-marshal the x-mortise block to decode into our struct.
	xmBytes, err := yaml.Marshal(xm)
	if err != nil {
		return nil, composeYAML, fmt.Errorf("marshal x-mortise: %w", err)
	}
	var ext MortiseExtension
	if err := yaml.Unmarshal(xmBytes, &ext); err != nil {
		return nil, composeYAML, fmt.Errorf("parse x-mortise: %w", err)
	}

	delete(raw, "x-mortise")
	cleaned, err := yaml.Marshal(raw)
	if err != nil {
		return nil, composeYAML, fmt.Errorf("re-marshal compose: %w", err)
	}

	return &ext, string(cleaned), nil
}

// resolveVarSpecs generates values for variables defined in x-mortise.variables,
// respecting dependency order (sign_with). User-provided vars take precedence.
func resolveVarSpecs(ext *MortiseExtension, vars map[string]string) error {
	if ext == nil || len(ext.Variables) == 0 {
		return nil
	}

	order, err := topoSortVars(ext.Variables)
	if err != nil {
		return err
	}

	for _, name := range order {
		if _, provided := vars[name]; provided {
			continue
		}

		spec := ext.Variables[name]
		val, err := generateVar(spec, vars)
		if err != nil {
			return fmt.Errorf("generate %s: %w", name, err)
		}
		vars[name] = val
	}
	return nil
}

// topoSortVars returns variable names in dependency order (dependencies first).
func topoSortVars(specs map[string]VarSpec) ([]string, error) {
	visited := make(map[string]bool)
	onStack := make(map[string]bool)
	var order []string

	var visit func(name string) error
	visit = func(name string) error {
		if onStack[name] {
			return fmt.Errorf("circular dependency on variable %q", name)
		}
		if visited[name] {
			return nil
		}
		onStack[name] = true
		if dep := specs[name].SignWith; dep != "" {
			if _, ok := specs[dep]; ok {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		onStack[name] = false
		visited[name] = true
		order = append(order, name)
		return nil
	}

	for name := range specs {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func generateVar(spec VarSpec, resolved map[string]string) (string, error) {
	switch spec.Generate {
	case "hex", "":
		n := spec.Length
		if n <= 0 {
			n = 32
		}
		return generateHex(n, randReader)

	case "jwt":
		if spec.SignWith == "" {
			return "", fmt.Errorf("jwt generator requires sign_with")
		}
		secret, ok := resolved[spec.SignWith]
		if !ok {
			return "", fmt.Errorf("sign_with references unresolved variable %q", spec.SignWith)
		}
		return generateJWT(spec.Claims, []byte(secret))

	default:
		return "", fmt.Errorf("unknown generator %q", spec.Generate)
	}
}

func generateHex(length int, reader io.Reader) (string, error) {
	nBytes := (length + 1) / 2
	b := make([]byte, nBytes)
	if _, err := io.ReadFull(reader, b); err != nil {
		return "", fmt.Errorf("generating hex: %w", err)
	}
	return hex.EncodeToString(b)[:length], nil
}

func generateJWT(claims map[string]any, secret []byte) (string, error) {
	mc := jwt.MapClaims{
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(10 * 365 * 24 * time.Hour).Unix(),
	}
	for k, v := range claims {
		mc[k] = v
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, mc)
	return token.SignedString(secret)
}
