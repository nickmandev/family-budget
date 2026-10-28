package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The backup/restore scripts cannot be executed end-to-end in this repository's
// test environment: they need a Docker daemon, a running Compose stack, age and
// an off-host target. These guards are STATIC. They assert the properties the
// operations review depends on (database-aware dump, encryption before upload,
// restore ordering, no committed secrets). Syntax is checked with `bash -n`
// when bash is available. The actual backup and restore drill runs on the
// deployment host per docs/RUNBOOK.md.

var dangerPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"private key block", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)},
	{"age secret key", regexp.MustCompile(`AGE-SECRET-KEY-1[0-9A-Z]{10,}`)},
	{"AWS access key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"inline SSH private key", regexp.MustCompile(`ssh-rsa[[:space:]]+AAAA[0-9A-Za-z+/]{20,}`)},
	{"hardcoded password assignment", regexp.MustCompile(`(?i)(^|[^A-Z_])PASSWORD[[:space:]]*=[[:space:]]*["']?[^"'\s$]`)},
}

func readScript(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "scripts", name))
	if err != nil {
		t.Fatalf("read scripts/%s: %v", name, err)
	}
	return string(content)
}

func TestBackupScriptsExistExecutableAndStrict(t *testing.T) {
	for _, name := range []string{"backup.sh", "restore.sh", "verify-restore.sh"} {
		path := filepath.Join("..", "scripts", name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat scripts/%s: %v", name, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("scripts/%s is not executable; chmod +x it or document `bash scripts/%s`", name, name)
		}

		content := readScript(t, name)
		if !strings.Contains(content, "set -euo pipefail") {
			t.Errorf("scripts/%s does not set `set -euo pipefail`", name)
		}
		if !strings.Contains(content, "--help") {
			t.Errorf("scripts/%s has no --help handling", name)
		}
	}
}

func TestBackupScriptsContainNoSecrets(t *testing.T) {
	for _, name := range []string{"backup.sh", "restore.sh", "verify-restore.sh"} {
		content := readScript(t, name)
		for _, pattern := range dangerPatterns {
			if pattern.re.MatchString(content) {
				t.Errorf("scripts/%s looks like it contains a hardcoded secret (%s); secrets belong in .env or the operator's key store", name, pattern.name)
			}
		}
	}
}

func TestBackupScriptIsDatabaseAwareEncryptedAndUploaded(t *testing.T) {
	content := readScript(t, "backup.sh")

	for _, want := range []string{
		"pg_dump",           // database-aware dump inside the db container
		"--format=custom",   // -Fc custom format for flexible restore
		"age --encrypt",     // encryption before any transfer
		"sha256sum",         // checksum recorded alongside
		"rsync",             // off-host transfer
		"--no-upload",       // local-only mode
		"--keep-local",      // retention control
		"--dry-run",         // safe rehearsal
		"backup_summary",    // machine-readable summary line
		"BACKUP_SSH_TARGET", // explicit off-host target
		"family-budget_",    // timestamped naming scheme
		"set -euo pipefail",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("scripts/backup.sh is missing %q", want)
		}
	}

	// The primary artifact must never be a raw PGDATA archive copy.
	if rawCopy := regexp.MustCompile(`(?m)^\s*(sudo\s+)?tar\s`).FindString(content); rawCopy != "" {
		t.Errorf("scripts/backup.sh appears to tar data directly (%q); use pg_dump", strings.TrimSpace(rawCopy))
	}
}

func TestRestoreScriptVerifiesDecryptsAndRestoresInOrder(t *testing.T) {
	content := readScript(t, "restore.sh")

	for _, want := range []string{
		"age --decrypt",
		"sha256sum -c",
		"pg_restore",
		"DROP DATABASE",
		"run --rm migrate up",
		"verify-restore.sh",
		"--fetch-latest",
		"--force",
		"require-data",
		"set -euo pipefail",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("scripts/restore.sh is missing %q", want)
		}
	}

	// Documented order: restore dump -> goose up -> start api. Use the last
	// occurrence of each token so usage text earlier in the file does not skew
	// the comparison.
	dump := strings.LastIndex(content, "pg_restore")
	migrate := strings.LastIndex(content, "run --rm migrate up")
	start := strings.LastIndex(content, `up -d "$BACKUP_API_SERVICE"`)
	if dump < 0 || migrate < 0 || start < 0 {
		t.Fatalf("could not locate restore ordering tokens (dump=%d migrate=%d start=%d)", dump, migrate, start)
	}
	if !(dump < migrate && migrate < start) {
		t.Errorf("restore order must be pg_restore (%d) -> migrate up (%d) -> start api (%d)", dump, migrate, start)
	}
}

func TestVerifyRestoreScriptChecksHealthSchemaAndConsistency(t *testing.T) {
	content := readScript(t, "verify-restore.sh")

	for _, want := range []string{
		"pg_isready", // database reachable
		"SELECT 1",   // database answers
		"households", // row-count spot checks
		"journal_operations",
		"account_postings",
		"envelope_postings",
		"idempotency_records",
		"ledger_version", // journal/projection consistency spot check
		"/readyz",        // API readiness
		"--require-data", // real-data gate
		"set -euo pipefail",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("scripts/verify-restore.sh is missing %q", want)
		}
	}
}

func TestBackupEnvExampleDocumentsVariables(t *testing.T) {
	example := readDeployFile(t, ".env.example")

	for _, name := range []string{
		"BACKUP_ENCRYPTION_PUBLIC_KEY",
		"BACKUP_SSH_TARGET",
		"BACKUP_SSH_PORT",
		"BACKUP_SSH_KEY",
		"BACKUP_REMOTE_PATH",
		"BACKUP_KEEP_LOCAL",
		"BACKUP_KEEP_REMOTE",
	} {
		if !strings.Contains(example, name+"=") {
			t.Errorf(".env.example does not document backup variable %s", name)
		}
	}

	// The age identity is operator-side only and must stay commented out.
	if regexp.MustCompile(`(?m)^BACKUP_AGE_IDENTITY_FILE=`).MatchString(example) {
		t.Error(".env.example must not set BACKUP_AGE_IDENTITY_FILE; the age private identity never lives with the deployment")
	}
}

func TestBackupScriptsPassBashSyntaxCheck(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available; static content guards still run")
	}
	for _, name := range []string{"backup.sh", "restore.sh", "verify-restore.sh"} {
		cmd := exec.Command(bash, "-n", filepath.Join("..", "scripts", name))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("bash -n scripts/%s failed: %v\n%s", name, err, out)
		}
	}
}
