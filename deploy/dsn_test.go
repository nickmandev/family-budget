package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// removedConnectionEnv names the old user-facing connection-string variables.
// The literals are assembled from fragments so this guard does not itself
// reintroduce the strings it forbids.
var removedConnectionEnv = []string{
	"DATABASE" + "_URL",
	"TEST_POSTGRES" + "_DSN",
}

// connectionComponents is the canonical database configuration family. The db
// container consumes the POSTGRES_* initdb trio; api and migrate consume all
// six.
var connectionComponents = []string{
	"POSTGRES_HOST",
	"POSTGRES_PORT",
	"POSTGRES_USER",
	"POSTGRES_PASSWORD",
	"POSTGRES_DB",
	"POSTGRES_SSLMODE",
}

// readRepoFile reads a repository-root file from the deploy package tests.
func readRepoFile(t *testing.T, name string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("..", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(content)
}

// TestNoUserSuppliedConnectionURLRemains is a grep-style guard over the
// operator-facing files: nobody is asked to hand-build or paste a database URL.
func TestNoUserSuppliedConnectionURLRemains(t *testing.T) {
	files := []string{
		"compose.yaml",
		".env.example",
		"Dockerfile",
		filepath.Join("scripts", "integration-test.sh"),
		filepath.Join(".github", "workflows", "ci.yml"),
	}
	for _, name := range files {
		content := readRepoFile(t, name)
		for _, removed := range removedConnectionEnv {
			if strings.Contains(strings.ToUpper(content), strings.ToUpper(removed)) {
				t.Errorf("%s still mentions the removed user-facing variable %s", name, removed)
			}
		}
	}

	if _, err := os.Stat(filepath.Join("..", "deploy", "migrate-entrypoint.sh")); !os.IsNotExist(err) {
		t.Error("deploy/migrate-entrypoint.sh exists; the Go migrate entrypoint constructs the DSN")
	}
}

// TestEnvExampleDefinesEachConnectionComponentOnce enforces the "one canonical
// variable family, each value defined once" rule.
func TestEnvExampleDefinesEachConnectionComponentOnce(t *testing.T) {
	example := readRepoFile(t, ".env.example")

	for _, name := range connectionComponents {
		pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `=(.*)$`)
		matches := pattern.FindAllStringSubmatch(example, -1)
		if len(matches) != 1 {
			t.Errorf(".env.example defines %s %d times, want exactly once", name, len(matches))
			continue
		}
		if strings.TrimSpace(matches[0][1]) == "" {
			t.Errorf(".env.example leaves %s empty", name)
		}
	}
}

// TestComposeWiresConnectionComponents proves api and migrate receive the
// components and that compose interpolates them from the shared .env values.
func TestComposeWiresConnectionComponents(t *testing.T) {
	doc := loadCompose(t)

	for _, name := range []string{"api", "migrate"} {
		service := requireService(t, doc, name)
		for _, component := range connectionComponents {
			value, ok := service.Environment[component]
			if !ok || value == nil {
				t.Errorf("%s environment has no %s", name, component)
				continue
			}
			if !strings.Contains(*value, "${"+component) {
				t.Errorf("%s %s = %q, want interpolation of the shared %s", name, component, *value, component)
			}
		}
	}
}

// TestMigrateReusesSharedDSNHelper asserts the Go migrate route: the binary
// imports internal/config and calls the same DatabaseConfig.DSN helper as the
// server, so no shell construction or parity test is needed.
func TestMigrateReusesSharedDSNHelper(t *testing.T) {
	main := readRepoFile(t, filepath.Join("cmd", "migrate", "main.go"))

	for _, want := range []string{
		`"family-budget/internal/config"`,
		"cfg.Database.DSN()",
		"config.LoadFrom",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("cmd/migrate/main.go does not contain %q; it must reuse the shared DSN helper", want)
		}
	}

	dockerfile := readRepoFile(t, "Dockerfile")
	if !strings.Contains(dockerfile, "./cmd/migrate") {
		t.Error("Dockerfile does not build ./cmd/migrate")
	}
}
