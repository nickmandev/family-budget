package deploy

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRunbookDocumentsRequiredOperations is a STATIC presence guard. It proves
// the runbook still covers monitoring and records gate-3 evidence; it cannot
// judge whether the prose is correct or current. Prose quality is reviewed by
// a human.
func TestRunbookDocumentsRequiredOperations(t *testing.T) {
	runbook := readDeployFile(t, filepath.Join("docs", "RUNBOOK.md"))

	required := []struct {
		what string
		text string
	}{
		{"alerting", "Alerting"},
		{"host memory alert", "Host memory pressure"},
		{"low disk alert", "Low disk"},
		{"service health alert", "Service health"},
		{"degraded host incident", "Degraded host / OOM"},
		{"gate-3 evidence", "Quality gate evidence"},
		{"gate-3 PostgreSQL version", "PostgreSQL: 18.6"},
	}
	for _, item := range required {
		if !strings.Contains(strings.ToLower(runbook), strings.ToLower(item.text)) {
			t.Errorf("docs/RUNBOOK.md no longer mentions %s (%q)", item.what, item.text)
		}
	}
}

// TestDeploymentLinksRunbook checks that the deployment doc points operators at
// the runbook for monitoring/incidents and at the integration runner.
func TestDeploymentLinksRunbook(t *testing.T) {
	deployment := readDeployFile(t, filepath.Join("docs", "DEPLOYMENT.md"))

	if !strings.Contains(deployment, "RUNBOOK.md") {
		t.Error("docs/DEPLOYMENT.md does not link to RUNBOOK.md")
	}
	if !strings.Contains(deployment, "scripts/integration-test.sh") {
		t.Error("docs/DEPLOYMENT.md does not point at scripts/integration-test.sh")
	}
}
