package tests

// Real CLI tests: build the binary once per run and exec it. The previous
// "TestInitProjectScaffolding" was vacuous — it hand-created the files it then
// claimed to verify.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

var (
	cliOnce sync.Once
	cliBin  string
	cliErr  error
)

// buildCLI compiles the spine binary once per test run.
func buildCLI(t *testing.T) string {
	t.Helper()
	cliOnce.Do(func() {
		dir, err := os.MkdirTemp("", "spine-cli-test-*")
		if err != nil {
			cliErr = err
			return
		}
		cliBin = filepath.Join(dir, "spine")
		// `go test` runs with the package dir (tests/) as cwd — the repo
		// root is one level up.
		cmd := exec.Command("go", "build", "-tags", "sqlite_fts5", "-o", cliBin, "./cmd/spine")
		cmd.Dir = ".."
		out, err := cmd.CombinedOutput()
		if err != nil {
			cliErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if cliErr != nil {
		t.Fatalf("build CLI: %v", cliErr)
	}
	return cliBin
}

func runCLI(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(buildCLI(t), args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("run %v: %v", args, err)
		}
	}
	return stdout.String(), stderr.String(), code
}

// TestCLIInitScaffoldsWithGeneratedSecrets: `spine init` must produce a
// working scaffold with FRESH secrets — never the old hardcoded defaults.
func TestCLIInitScaffoldsWithGeneratedSecrets(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "proj")
	if _, stderr, code := runCLI(t, dir, "init", target); code != 0 {
		t.Fatalf("spine init failed (%d): %s", code, stderr)
	}

	manifestContent, err := os.ReadFile(filepath.Join(target, "app.spine"))
	if err != nil {
		t.Fatalf("app.spine missing after init: %v", err)
	}
	envContent, err := os.ReadFile(filepath.Join(target, ".env.example"))
	if err != nil {
		t.Fatalf(".env.example missing after init: %v", err)
	}

	// The manifest references the secrets via $env — never inline defaults.
	if !strings.Contains(string(manifestContent), "$ADMIN_SECRET") ||
		!strings.Contains(string(manifestContent), "$PUBLIC_SECRET") {
		t.Errorf("manifest must reference env-backed secrets:\n%s", manifestContent)
	}
	// The .env.example must carry freshly generated keys.
	if !strings.Contains(string(envContent), "ADMIN_SECRET=sk_admin_") ||
		!strings.Contains(string(envContent), "PUBLIC_SECRET=sk_public_") {
		t.Errorf(".env.example must contain generated keys:\n%s", envContent)
	}
	if strings.Contains(string(envContent), "sk_admin_secret_12345") {
		t.Errorf("hardcoded default secret leaked into scaffold:\n%s", envContent)
	}
}

// TestCLIVersionMatchesReleaseFormat: `spine version` prints the release
// format the CI version-consistency check parses.
func TestCLIVersionMatchesReleaseFormat(t *testing.T) {
	stdout, _, code := runCLI(t, t.TempDir(), "version")
	if code != 0 {
		t.Fatalf("spine version failed with %d", code)
	}
	if !regexp.MustCompile(`^spine v[0-9]+\.[0-9]+\.[0-9]+`).MatchString(strings.TrimSpace(stdout)) {
		t.Errorf("unexpected version output: %q", stdout)
	}
}

// TestCLIServeRefusesWithoutAuth: the fail-closed guard (Batch E) must refuse
// to start with no API key and no access rules, exiting non-zero.
func TestCLIServeRefusesWithoutAuth(t *testing.T) {
	dir := t.TempDir()
	manifest := `spine_version: 1
database:
  tables:
    - data
routes:
  - on: EVT
    steps:
      - action: log.write
`
	if err := os.WriteFile(filepath.Join(dir, "app.spine"), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	_, stderr, code := runCLI(t, dir, "serve", "app.spine")
	if code == 0 {
		t.Fatal("spine serve must refuse to start without authentication")
	}
	if !strings.Contains(stderr, "Refusing to start") {
		t.Errorf("expected fail-closed refusal message, got: %s", stderr)
	}
}

// TestCLIDeployRenderAndParse: documented deploy targets work and the example
// manifest parses.
func TestCLIDeployRenderAndParse(t *testing.T) {
	dir := t.TempDir()
	if _, stderr, code := runCLI(t, dir, "deploy", "render"); code != 0 {
		t.Fatalf("spine deploy render failed (%d): %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err != nil {
		t.Fatalf("Dockerfile not generated: %v", err)
	}

	repo := ".."
	stdout, stderr, code := runCLI(t, repo, "parse", "examples/app.spine")
	if code != 0 {
		t.Fatalf("spine parse failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "parsed successfully") {
		t.Errorf("expected parse success, got: %s", stdout)
	}
}
