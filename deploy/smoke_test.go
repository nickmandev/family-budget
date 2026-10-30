package deploy

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

const smokeTestScriptPath = "scripts/smoke-test.sh"

func TestSmokeTestScriptShape(t *testing.T) {
	script, err := os.ReadFile("../" + smokeTestScriptPath)
	if err != nil {
		t.Fatalf("read %s: %v", smokeTestScriptPath, err)
	}
	content := string(script)
	for _, want := range []string{
		"set -euo pipefail",
		"Idempotency-Key",
		"healthz",
		"readyz",
		"IDEMPOTENCY_KEY_PAYLOAD_MISMATCH",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("%s does not contain %q", smokeTestScriptPath, want)
		}
	}

	// The API origin and credentials are supplied/generated at runtime; do not
	// bake the production endpoint, deploy credentials, or a database URL here.
	for _, forbidden := range []string{
		"budget-api.nickmandev.com",
		"nickmandev.com",
		"PRIVATE KEY",
		"password=password",
		"curl -u ",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("%s contains %q; endpoints and credentials must not be hardcoded", smokeTestScriptPath, forbidden)
		}
	}
	if strings.Contains(content, "DATABASE"+"_URL") {
		t.Errorf("%s must not refer to a database URL", smokeTestScriptPath)
	}
}

func TestSmokeTestHelpDoesNotMakeRequests(t *testing.T) {
	cmd := exec.Command("bash", smokeTestScriptPath, "--help")
	cmd.Dir = ".."
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash %s --help failed: %v\n%s", smokeTestScriptPath, err, output)
	}
	if !strings.Contains(string(output), "Usage:") ||
		!strings.Contains(string(output), "https://<your-production-host>") {
		t.Errorf("--help output is missing usage or the production example: %s", output)
	}
}
