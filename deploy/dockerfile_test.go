// Package deploy contains static deployment guards.
//
// These tests do not build or run containers. They parse the Dockerfile (and,
// in compose_test.go and nginx_test.go, the Compose/nginx files) and fail when a property the
// operations review depends on is lost: pinned images, a multi-stage build, a
// static server binary, a non-root runtime user and secret-free build context.
// Container build/run validation must happen in CI or on the deployment host.
package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"family-budget/internal/config"
)

var fromLine = regexp.MustCompile(`(?i)^FROM\s+(\S+)(?:\s+AS\s+(\S+))?\s*$`)

// stage is one Dockerfile build stage and the instructions that follow it.
type stage struct {
	base         string
	name         string
	instructions []string
}

func (s stage) text() string { return strings.Join(s.instructions, "\n") }

// readDeployFile reads a repository-root file from the deploy package tests.
func readDeployFile(t *testing.T, name string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("..", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(content)
}

// parseStages splits a Dockerfile into stages. Comment and blank lines are
// ignored; every other instruction belongs to the most recent FROM.
func parseStages(t *testing.T, dockerfile string) []stage {
	t.Helper()

	var stages []stage
	for _, raw := range strings.Split(dockerfile, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if match := fromLine.FindStringSubmatch(line); match != nil {
			stages = append(stages, stage{base: match[1], name: match[2]})
			continue
		}
		if len(stages) == 0 {
			t.Fatalf("Dockerfile instruction before first FROM: %q", line)
		}
		stages[len(stages)-1].instructions = append(stages[len(stages)-1].instructions, line)
	}
	if len(stages) == 0 {
		t.Fatal("Dockerfile has no FROM instruction")
	}
	return stages
}

// isPinned reports whether an image reference uses an explicit tag or digest
// rather than the implicit/floating `latest`.
func isPinned(image string) bool {
	if strings.Contains(strings.ToLower(image), ":latest") {
		return false
	}
	if strings.Contains(image, "@sha256:") {
		return true
	}
	last := image
	if slash := strings.LastIndex(image, "/"); slash >= 0 {
		last = image[slash+1:]
	}
	return strings.Contains(last, ":")
}

// isStageReference reports whether image names a previously declared stage
// rather than a registry image.
func isStageReference(image string, prior []stage) bool {
	for _, candidate := range prior {
		if candidate.name != "" && strings.EqualFold(candidate.name, image) {
			return true
		}
	}
	return false
}

func directive(stage stage, keyword string) string {
	prefix := strings.ToUpper(keyword) + " "
	for _, instruction := range stage.instructions {
		upper := strings.ToUpper(instruction)
		if strings.HasPrefix(upper, prefix) {
			return strings.TrimSpace(instruction[len(prefix):])
		}
	}
	return ""
}

func isRootUser(user string) bool {
	switch strings.ToLower(strings.TrimSpace(user)) {
	case "root", "0", "0:0", "root:root", "0:root", "root:0":
		return true
	}
	return false
}

func TestDockerfileIsMultiStageWithPinnedImages(t *testing.T) {
	stages := parseStages(t, readDeployFile(t, "Dockerfile"))
	if len(stages) < 2 {
		t.Fatalf("Dockerfile has %d stage(s), want at least 2 (builder + runtime)", len(stages))
	}

	for i, current := range stages {
		if isStageReference(current.base, stages[:i]) {
			continue
		}
		if !isPinned(current.base) {
			t.Errorf("stage %q uses unpinned base image %q; pin an explicit version tag or digest", current.name, current.base)
		}
		if strings.Contains(strings.ToLower(current.base), ":latest") {
			t.Errorf("stage %q uses the floating latest tag: %q", current.name, current.base)
		}
	}
}

func TestDockerfileRuntimeIsNonRootAndStatic(t *testing.T) {
	stages := parseStages(t, readDeployFile(t, "Dockerfile"))
	final := stages[len(stages)-1]

	if final.name != "runtime" {
		t.Errorf("final stage is %q, want the API runtime stage", final.name)
	}

	user := directive(final, "USER")
	if user == "" {
		t.Fatal("final stage has no USER directive; the API must run as non-root")
	}
	if isRootUser(user) {
		t.Errorf("final stage runs as root (%q); use a numeric non-root UID", user)
	}

	if entrypoint := directive(final, "ENTRYPOINT"); entrypoint == "" {
		t.Error("final stage has no ENTRYPOINT")
	}

	port := strconv.Itoa(config.Default().HTTP.Port)
	if expose := directive(final, "EXPOSE"); !strings.Contains(expose, port) {
		t.Errorf("final stage EXPOSE %q does not include the configured API port %s", expose, port)
	}

	all := strings.Join(stageTexts(stages), "\n")
	if !strings.Contains(all, "go mod download") {
		t.Error("Dockerfile does not cache dependencies with `go mod download` before copying source")
	}
	if strings.Index(all, "go mod download") > strings.Index(all, "COPY . .") {
		t.Error("`go mod download` must run before `COPY . .` for layer caching")
	}
	if !strings.Contains(all, "CGO_ENABLED=0") {
		t.Error("Dockerfile does not build with CGO_ENABLED=0")
	}
	if !strings.Contains(all, "go build") || !strings.Contains(all, "./cmd/server") {
		t.Error("Dockerfile does not run `go build ... ./cmd/server`")
	}
	if !strings.Contains(all, `-ldflags="-s -w"`) || !strings.Contains(all, "-trimpath") {
		t.Error("Dockerfile build is missing -trimpath and -ldflags=\"-s -w\"")
	}
}

func TestDockerfileMigrateStageUsesPinnedGoose(t *testing.T) {
	stages := parseStages(t, readDeployFile(t, "Dockerfile"))

	var migrate *stage
	for i := range stages {
		if stages[i].name == "migrate" {
			migrate = &stages[i]
		}
	}
	if migrate == nil {
		t.Fatal("Dockerfile has no migrate stage; the schema must be applied out-of-band")
	}

	all := strings.Join(stageTexts(stages), "\n")
	goMod := readDeployFile(t, "go.mod")
	if !strings.Contains(goMod, "github.com/pressly/goose/v3 v3.") {
		t.Error("go.mod does not pin a goose module version")
	}
	if strings.Contains(goMod, "github.com/pressly/goose/v3 v3.0.0-") {
		t.Error("goose is pinned to a floating pseudo-version")
	}
	if !strings.Contains(all, "./cmd/migrate") {
		t.Error("Dockerfile does not build the migrate entrypoint from ./cmd/migrate")
	}
	if !strings.Contains(migrate.text(), "migrations") {
		t.Error("migrate stage does not copy the migrations directory")
	}
	if entrypoint := directive(*migrate, "ENTRYPOINT"); !strings.Contains(entrypoint, "/migrate") {
		t.Errorf("migrate stage entrypoint = %q, want the compiled migrate binary", entrypoint)
	}

	user := directive(*migrate, "USER")
	if user == "" || isRootUser(user) {
		t.Errorf("migrate stage must run as a non-root user, got USER %q", user)
	}
}

func TestDockerignoreKeepsBuildInputsAndExcludesSecrets(t *testing.T) {
	ignores := readDeployFile(t, ".dockerignore")
	lines := map[string]bool{}
	for _, raw := range strings.Split(ignores, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines[line] = true
	}

	for _, required := range []string{".git", ".env", ".env.*"} {
		if !lines[required] {
			t.Errorf(".dockerignore does not exclude %q", required)
		}
	}
	if !lines["!.env.example"] {
		t.Error(".dockerignore should re-include .env.example documentation")
	}
	for _, needed := range []string{"cmd", "internal", "migrations"} {
		for _, variant := range []string{needed, needed + "/", "/" + needed, "/" + needed + "/"} {
			if lines[variant] {
				t.Errorf(".dockerignore excludes build input %q", variant)
			}
		}
	}
}

func stageTexts(stages []stage) []string {
	texts := make([]string, len(stages))
	for i, current := range stages {
		texts[i] = current.text()
	}
	return texts
}
