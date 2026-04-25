package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mortise-org/mortise/internal/templates"
)

func TestParseMortiseExtensionPresent(t *testing.T) {
	yml := `x-mortise:
  variables:
    JWT_SECRET:
      generate: hex
      length: 64
    ANON_KEY:
      generate: jwt
      claims:
        role: anon
      sign_with: JWT_SECRET
services:
  web:
    image: nginx
`
	ext, cleaned, err := parseMortiseExtension(yml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ext == nil {
		t.Fatal("expected extension, got nil")
	}
	if len(ext.Variables) != 2 {
		t.Fatalf("expected 2 variables, got %d", len(ext.Variables))
	}
	if ext.Variables["JWT_SECRET"].Generate != "hex" {
		t.Errorf("JWT_SECRET generate = %q, want hex", ext.Variables["JWT_SECRET"].Generate)
	}
	if ext.Variables["JWT_SECRET"].Length != 64 {
		t.Errorf("JWT_SECRET length = %d, want 64", ext.Variables["JWT_SECRET"].Length)
	}
	if ext.Variables["ANON_KEY"].Generate != "jwt" {
		t.Errorf("ANON_KEY generate = %q, want jwt", ext.Variables["ANON_KEY"].Generate)
	}
	if ext.Variables["ANON_KEY"].SignWith != "JWT_SECRET" {
		t.Errorf("ANON_KEY sign_with = %q, want JWT_SECRET", ext.Variables["ANON_KEY"].SignWith)
	}
	if strings.Contains(cleaned, "x-mortise") {
		t.Errorf("cleaned YAML should not contain x-mortise, got:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "services:") {
		t.Errorf("cleaned YAML should still contain services")
	}
}

func TestParseMortiseExtensionAbsent(t *testing.T) {
	yml := `services:
  web:
    image: nginx
`
	ext, cleaned, err := parseMortiseExtension(yml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ext != nil {
		t.Fatal("expected nil extension for vanilla compose")
	}
	if !strings.Contains(cleaned, "services:") {
		t.Error("cleaned should be original YAML")
	}
}

func TestTopoSortVarsOrdering(t *testing.T) {
	specs := map[string]VarSpec{
		"ANON_KEY":         {Generate: "jwt", SignWith: "JWT_SECRET"},
		"JWT_SECRET":       {Generate: "hex", Length: 64},
		"SERVICE_ROLE_KEY": {Generate: "jwt", SignWith: "JWT_SECRET"},
	}
	order, err := topoSortVars(specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	idx := make(map[string]int, len(order))
	for i, name := range order {
		idx[name] = i
	}
	if idx["JWT_SECRET"] >= idx["ANON_KEY"] {
		t.Errorf("JWT_SECRET should come before ANON_KEY: %v", order)
	}
	if idx["JWT_SECRET"] >= idx["SERVICE_ROLE_KEY"] {
		t.Errorf("JWT_SECRET should come before SERVICE_ROLE_KEY: %v", order)
	}
}

func TestTopoSortVarsCircularDependency(t *testing.T) {
	specs := map[string]VarSpec{
		"A": {Generate: "jwt", SignWith: "B"},
		"B": {Generate: "jwt", SignWith: "A"},
	}
	_, err := topoSortVars(specs)
	if err == nil {
		t.Fatal("expected circular dependency error")
	}
	if !strings.Contains(err.Error(), "circular") {
		t.Errorf("error should mention circular, got: %v", err)
	}
}

func TestGenerateVarHex(t *testing.T) {
	spec := VarSpec{Generate: "hex", Length: 16}
	val, err := generateVar(spec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(val) != 16 {
		t.Errorf("expected 16-char hex, got %d chars: %q", len(val), val)
	}
}

func TestGenerateVarHexDefaultLength(t *testing.T) {
	spec := VarSpec{Generate: "hex"}
	val, err := generateVar(spec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(val) != 32 {
		t.Errorf("expected 32-char default hex, got %d chars: %q", len(val), val)
	}
}

func TestGenerateVarJWT(t *testing.T) {
	secret := "test-secret-key-for-jwt-signing"
	resolved := map[string]string{"JWT_SECRET": secret}
	spec := VarSpec{
		Generate: "jwt",
		Claims:   map[string]any{"role": "anon", "iss": "supabase"},
		SignWith: "JWT_SECRET",
	}
	tokenStr, err := generateVar(spec, resolved)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Parse and validate the token.
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			t.Fatalf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		t.Fatalf("failed to parse generated JWT: %v", err)
	}
	if !token.Valid {
		t.Fatal("generated JWT is not valid")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("could not cast claims to MapClaims")
	}
	if claims["role"] != "anon" {
		t.Errorf("role claim = %v, want anon", claims["role"])
	}
	if claims["iss"] != "supabase" {
		t.Errorf("iss claim = %v, want supabase", claims["iss"])
	}
	if _, ok := claims["exp"]; !ok {
		t.Error("expected exp claim")
	}
	if _, ok := claims["iat"]; !ok {
		t.Error("expected iat claim")
	}
}

func TestGenerateVarJWTMissingSignWith(t *testing.T) {
	spec := VarSpec{Generate: "jwt", Claims: map[string]any{"role": "anon"}}
	_, err := generateVar(spec, nil)
	if err == nil {
		t.Fatal("expected error for jwt without sign_with")
	}
}

func TestGenerateVarJWTUnresolvedDep(t *testing.T) {
	spec := VarSpec{Generate: "jwt", SignWith: "MISSING", Claims: map[string]any{"role": "anon"}}
	_, err := generateVar(spec, map[string]string{})
	if err == nil {
		t.Fatal("expected error for unresolved sign_with")
	}
}

func TestGenerateVarUnknownType(t *testing.T) {
	spec := VarSpec{Generate: "rsa"}
	_, err := generateVar(spec, nil)
	if err == nil {
		t.Fatal("expected error for unknown generator type")
	}
}

func TestResolveVarSpecsUserProvidedTakesPrecedence(t *testing.T) {
	ext := &MortiseExtension{
		Variables: map[string]VarSpec{
			"JWT_SECRET": {Generate: "hex", Length: 64},
			"ANON_KEY":   {Generate: "jwt", Claims: map[string]any{"role": "anon"}, SignWith: "JWT_SECRET"},
		},
	}
	vars := map[string]string{
		"JWT_SECRET": "user-provided-secret",
		"ANON_KEY":   "user-provided-key",
	}
	if err := resolveVarSpecs(ext, vars); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars["JWT_SECRET"] != "user-provided-secret" {
		t.Errorf("JWT_SECRET should not be overwritten, got %q", vars["JWT_SECRET"])
	}
	if vars["ANON_KEY"] != "user-provided-key" {
		t.Errorf("ANON_KEY should not be overwritten, got %q", vars["ANON_KEY"])
	}
}

func TestResolveVarSpecsNilExtension(t *testing.T) {
	vars := map[string]string{}
	if err := resolveVarSpecs(nil, vars); err != nil {
		t.Fatalf("unexpected error for nil ext: %v", err)
	}
}

func TestSubstituteVarsWithMortiseExtension(t *testing.T) {
	yml := `x-mortise:
  variables:
    JWT_SECRET:
      generate: hex
      length: 32
    ANON_KEY:
      generate: jwt
      claims:
        role: anon
        iss: supabase
      sign_with: JWT_SECRET
services:
  web:
    image: nginx
    environment:
      SECRET: ${JWT_SECRET}
      KEY: ${ANON_KEY}
      OTHER: ${RANDOM_VAR}
`
	vars := make(map[string]string)
	result, err := substituteVars(yml, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// JWT_SECRET should be 32-char hex.
	if len(vars["JWT_SECRET"]) != 32 {
		t.Errorf("JWT_SECRET length = %d, want 32", len(vars["JWT_SECRET"]))
	}

	// ANON_KEY should be a valid JWT.
	token, err := jwt.Parse(vars["ANON_KEY"], func(token *jwt.Token) (any, error) {
		return []byte(vars["JWT_SECRET"]), nil
	})
	if err != nil {
		t.Fatalf("ANON_KEY is not a valid JWT: %v", err)
	}
	claims := token.Claims.(jwt.MapClaims)
	if claims["role"] != "anon" {
		t.Errorf("ANON_KEY role = %v, want anon", claims["role"])
	}

	// RANDOM_VAR should be auto-generated hex (no x-mortise spec).
	if vars["RANDOM_VAR"] == "" {
		t.Error("RANDOM_VAR should be auto-generated")
	}

	// Result should not contain any ${} placeholders.
	if strings.Contains(result, "${") {
		t.Errorf("result still contains unresolved placeholders:\n%s", result)
	}

	// Result should not contain x-mortise.
	if strings.Contains(result, "x-mortise") {
		t.Errorf("result should not contain x-mortise block")
	}
}

func TestSupabaseTemplateMortiseIntegration(t *testing.T) {
	tpl, err := templates.Load("supabase")
	if err != nil {
		t.Fatalf("failed to load supabase template: %v", err)
	}

	// Verify x-mortise block is present in raw template.
	if !strings.Contains(tpl.Compose, "x-mortise") {
		t.Fatal("supabase template should contain x-mortise block")
	}

	// Run substituteVars with an empty vars map (all generated).
	vars := make(map[string]string)
	result, err := substituteVars(tpl.Compose, vars)
	if err != nil {
		t.Fatalf("substituteVars failed: %v", err)
	}

	// 1. JWT_SECRET should be a 64-char hex string.
	jwtSecret := vars["JWT_SECRET"]
	if len(jwtSecret) != 64 {
		t.Errorf("JWT_SECRET length = %d, want 64", len(jwtSecret))
	}
	for _, c := range jwtSecret {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("JWT_SECRET contains non-hex char %q", c)
			break
		}
	}

	// 2. ANON_KEY should be a valid JWT signed with JWT_SECRET.
	anonToken, err := jwt.Parse(vars["ANON_KEY"], func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})
	if err != nil {
		t.Fatalf("ANON_KEY is not a valid JWT: %v", err)
	}
	if !anonToken.Valid {
		t.Error("ANON_KEY JWT is not valid")
	}
	anonClaims := anonToken.Claims.(jwt.MapClaims)
	if anonClaims["role"] != "anon" {
		t.Errorf("ANON_KEY role = %v, want anon", anonClaims["role"])
	}
	if anonClaims["iss"] != "supabase" {
		t.Errorf("ANON_KEY iss = %v, want supabase", anonClaims["iss"])
	}
	if _, ok := anonClaims["exp"]; !ok {
		t.Error("ANON_KEY missing exp claim")
	}
	if _, ok := anonClaims["iat"]; !ok {
		t.Error("ANON_KEY missing iat claim")
	}

	// 3. SERVICE_ROLE_KEY should be a valid JWT signed with JWT_SECRET.
	svcToken, err := jwt.Parse(vars["SERVICE_ROLE_KEY"], func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})
	if err != nil {
		t.Fatalf("SERVICE_ROLE_KEY is not a valid JWT: %v", err)
	}
	if !svcToken.Valid {
		t.Error("SERVICE_ROLE_KEY JWT is not valid")
	}
	svcClaims := svcToken.Claims.(jwt.MapClaims)
	if svcClaims["role"] != "service_role" {
		t.Errorf("SERVICE_ROLE_KEY role = %v, want service_role", svcClaims["role"])
	}
	if svcClaims["iss"] != "supabase" {
		t.Errorf("SERVICE_ROLE_KEY iss = %v, want supabase", svcClaims["iss"])
	}

	// 4. PG_PASSWORD and SECRET_KEY_BASE should be auto-generated as 32-char hex.
	for _, name := range []string{"PG_PASSWORD", "SECRET_KEY_BASE"} {
		v := vars[name]
		if v == "" {
			t.Errorf("%s should be auto-generated, got empty string", name)
			continue
		}
		if len(v) != 32 {
			t.Errorf("%s length = %d, want 32", name, len(v))
		}
	}

	// 5. x-mortise should be stripped from output.
	if strings.Contains(result, "x-mortise") {
		t.Error("result should not contain x-mortise block")
	}

	// 6. No unresolved ${} placeholders.
	if strings.Contains(result, "${") {
		t.Errorf("result still contains unresolved placeholders:\n%s", result)
	}

	// 7. The resulting YAML should be parseable by parseCompose.
	cf, err := parseCompose(result)
	if err != nil {
		t.Fatalf("result is not valid compose YAML: %v", err)
	}

	// Sanity check: all expected services are present.
	expectedServices := []string{"postgres", "auth", "rest", "storage", "realtime", "meta", "studio"}
	for _, svc := range expectedServices {
		if _, ok := cf.Services[svc]; !ok {
			t.Errorf("missing expected service %q in parsed compose", svc)
		}
	}

	// 8. Verify the generated values are actually substituted into the YAML.
	if !strings.Contains(result, jwtSecret) {
		t.Error("result YAML does not contain the generated JWT_SECRET value")
	}
	if !strings.Contains(result, vars["PG_PASSWORD"]) {
		t.Error("result YAML does not contain the generated PG_PASSWORD value")
	}
}

func TestSupabaseTemplateMortiseUserOverride(t *testing.T) {
	tpl, err := templates.Load("supabase")
	if err != nil {
		t.Fatalf("failed to load supabase template: %v", err)
	}

	// User provides their own JWT_SECRET; JWTs should be signed with it.
	userSecret := "my-custom-secret-that-is-definitely-not-random-at-all-seriously"
	vars := map[string]string{
		"JWT_SECRET": userSecret,
	}
	result, err := substituteVars(tpl.Compose, vars)
	if err != nil {
		t.Fatalf("substituteVars failed: %v", err)
	}

	// JWT_SECRET should be the user-provided value.
	if vars["JWT_SECRET"] != userSecret {
		t.Errorf("JWT_SECRET = %q, want user-provided value", vars["JWT_SECRET"])
	}

	// ANON_KEY should still be generated and signed with the user's secret.
	anonToken, err := jwt.Parse(vars["ANON_KEY"], func(token *jwt.Token) (any, error) {
		return []byte(userSecret), nil
	})
	if err != nil {
		t.Fatalf("ANON_KEY is not a valid JWT signed with user secret: %v", err)
	}
	if !anonToken.Valid {
		t.Error("ANON_KEY JWT is not valid")
	}

	// SERVICE_ROLE_KEY should be signed with user's secret too.
	svcToken, err := jwt.Parse(vars["SERVICE_ROLE_KEY"], func(token *jwt.Token) (any, error) {
		return []byte(userSecret), nil
	})
	if err != nil {
		t.Fatalf("SERVICE_ROLE_KEY is not a valid JWT signed with user secret: %v", err)
	}
	if !svcToken.Valid {
		t.Error("SERVICE_ROLE_KEY JWT is not valid")
	}

	// Output should still be valid compose.
	if _, err := parseCompose(result); err != nil {
		t.Fatalf("result is not valid compose YAML: %v", err)
	}
}

func TestTopoSortVarsChainABC(t *testing.T) {
	// Test a chain: C depends on B depends on A.
	specs := map[string]VarSpec{
		"C": {Generate: "jwt", SignWith: "B"},
		"B": {Generate: "jwt", SignWith: "A"},
		"A": {Generate: "hex", Length: 32},
	}
	order, err := topoSortVars(specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	idx := make(map[string]int, len(order))
	for i, name := range order {
		idx[name] = i
	}
	if idx["A"] >= idx["B"] {
		t.Errorf("A should come before B: %v", order)
	}
	if idx["B"] >= idx["C"] {
		t.Errorf("B should come before C: %v", order)
	}
}

func TestTopoSortVarsNoDeps(t *testing.T) {
	specs := map[string]VarSpec{
		"X": {Generate: "hex", Length: 16},
		"Y": {Generate: "hex", Length: 32},
		"Z": {Generate: "hex", Length: 64},
	}
	order, err := topoSortVars(specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 3 {
		t.Fatalf("expected 3 items, got %d", len(order))
	}
}

func TestTopoSortVarsSingleItem(t *testing.T) {
	specs := map[string]VarSpec{
		"ONLY": {Generate: "hex", Length: 8},
	}
	order, err := topoSortVars(specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 1 || order[0] != "ONLY" {
		t.Errorf("expected [ONLY], got %v", order)
	}
}

func TestSubstituteVarsVanillaComposeUnchanged(t *testing.T) {
	yml := `services:
  web:
    image: nginx
    environment:
      SECRET: ${MY_SECRET}
`
	vars := make(map[string]string)
	result, err := substituteVars(yml, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "${") {
		t.Errorf("result still contains unresolved placeholders")
	}
	if vars["MY_SECRET"] == "" {
		t.Error("MY_SECRET should be auto-generated")
	}
	if len(vars["MY_SECRET"]) != 32 {
		t.Errorf("auto-generated var should be 32 chars, got %d", len(vars["MY_SECRET"]))
	}
}
