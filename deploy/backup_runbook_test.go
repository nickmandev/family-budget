package deploy

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRunbookDocumentsRequiredOperations is a STATIC presence guard. It proves
// the runbook still covers the topics the operations contract requires; it
// cannot judge whether the prose is correct or current. Content quality and
// the drill itself are reviewed by a human and recorded in the drill log.
func TestRunbookDocumentsRequiredOperations(t *testing.T) {
	runbook := readDeployFile(t, filepath.Join("docs", "RUNBOOK.md"))

	required := []struct {
		what string
		text string
	}{
		{"backup policy", "Backup policy"},
		{"database-aware dump", "pg_dump"},
		{"off-host target", "Hetzner Storage Box"},
		{"encryption tool", "age"},
		{"key custody", "Key custody"},
		{"RPO", "RPO"},
		{"RTO", "RTO"},
		{"retention", "Retention"},
		{"restore drill", "Restore drill"},
		{"drill log template", "Drill log template"},
		{"drill cadence", "quarterly"},
		{"alerting", "Alerting"},
		{"stale backup alert", "Stale backup age"},
		{"low disk alert", "Low disk"},
		{"service health alert", "Service health"},
		{"clean restore", "fresh database"},
		{"migration order", "goose up"},
	}
	for _, item := range required {
		if !strings.Contains(strings.ToLower(runbook), strings.ToLower(item.text)) {
			t.Errorf("docs/RUNBOOK.md no longer mentions %s (%q)", item.what, item.text)
		}
	}
}

// TestDeploymentLinksRunbook checks that the deployment doc points operators at
// the runbook instead of leaving the old TODO stubs in place.
func TestDeploymentLinksRunbook(t *testing.T) {
	deployment := readDeployFile(t, filepath.Join("docs", "DEPLOYMENT.md"))

	if !strings.Contains(deployment, "RUNBOOK.md") {
		t.Error("docs/DEPLOYMENT.md does not link to RUNBOOK.md")
	}
	if strings.Contains(deployment, "TODO — backups and recovery") {
		t.Error("docs/DEPLOYMENT.md still has the old backups TODO stub")
	}
	if strings.Contains(deployment, "TODO — monitoring") {
		t.Error("docs/DEPLOYMENT.md still has the old monitoring TODO stub")
	}
}
