package deploy

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposeApplicationImagesUseReleaseVersion(t *testing.T) {
	doc := loadCompose(t)
	if got, want := requireService(t, doc, "api").Image, "${IMAGE_REGISTRY:-ghcr.io}/${IMAGE_OWNER:-nickmandev}/family-budget-api:${APP_VERSION:-1.0.0}"; got != want {
		t.Errorf("api image = %q, want %q", got, want)
	}
	if got, want := requireService(t, doc, "migrate").Image, "${IMAGE_REGISTRY:-ghcr.io}/${IMAGE_OWNER:-nickmandev}/family-budget-migrate:${APP_VERSION:-1.0.0}"; got != want {
		t.Errorf("migrate image = %q, want %q", got, want)
	}
}

func TestReleaseWorkflowPublishesTagImagesOnlyAfterVerification(t *testing.T) {
	workflow := readDeployFile(t, ".github/workflows/release-images.yml")
	for _, want := range []string{
		"tags: [\"v*\"]",
		`TAG="$GITHUB_REF_NAME"`,
		"uses: ./.github/workflows/test.yml",
		"needs: test",
		"packages: write",
		"docker/login-action@v3",
		"docker/build-push-action@v6",
		"family-budget-api:",
		"family-budget-migrate:",
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("release workflow does not contain %q", want)
		}
	}
	if strings.Contains(workflow, "workflow_call:") {
		t.Error("release image workflow must only publish from pushed tags")
	}
	testWorkflow := readDeployFile(t, ".github/workflows/test.yml")
	for _, want := range []string{"workflow_call:", "  test:"} {
		if !strings.Contains(testWorkflow, want) {
			t.Errorf("verification workflow does not contain %q", want)
		}
	}
	for _, forbidden := range []string{"HETZNER_", "scp ", "ssh ", "deploy-release.sh"} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("release publication workflow contains direct VPS deployment detail %q", forbidden)
		}
	}
}

func TestReleaseDrafterNeverPublishes(t *testing.T) {
	workflow := readDeployFile(t, ".github/workflows/release-drafter.yml")
	for _, want := range []string{
		"push:",
		"branches: [main]",
		"workflow_dispatch:",
		"scripts/next-version.sh",
		"gh release create \"$TAG\" --draft",
		"contents: write",
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("release drafter does not contain %q", want)
		}
	}
	for _, forbidden := range []string{
		"git push",
		"--draft=false",
		"gh release publish",
		"release-images",
		"HETZNER_",
		"scp ",
		"ssh ",
		"deploy-release.sh",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("release drafter contains forbidden publication/deployment detail %q", forbidden)
		}
	}
}

func TestVPSReleaseScriptsParseAndKeepSafetySteps(t *testing.T) {
	for _, path := range []string{"scripts/check-release.sh", "scripts/deploy-release.sh", "scripts/next-version.sh"} {
		if output, err := exec.Command("bash", "-n", filepath.Join("..", path)).CombinedOutput(); err != nil {
			t.Fatalf("bash -n %s: %v\n%s", path, err, output)
		}
	}
	poller := readDeployFile(t, "scripts/check-release.sh")
	for _, want := range []string{
		"git fetch",
		"git tag --merged origin/main",
		"sort -V",
		"flock",
		"docker compose pull api migrate",
		"seq 1 12",
		"deferring %s",
		"deploy-release.sh",
	} {
		if !strings.Contains(poller, want) {
			t.Errorf("release poller does not contain %q", want)
		}
	}
	deployer := readDeployFile(t, "scripts/deploy-release.sh")
	for _, want := range []string{
		"docker compose pull api migrate",
		"docker compose run --rm migrate",
		"docker compose up -d --no-deps --no-build --force-recreate api",
		".State.Health.Status",
		"restoring the previous app version/configuration",
	} {
		if !strings.Contains(deployer, want) {
			t.Errorf("release deployment script does not contain %q", want)
		}
	}
	service := readDeployFile(t, "deploy/systemd/family-budget-release-check@.service")
	if !strings.Contains(service, "check-release.sh") {
		t.Error("release poller service does not invoke check-release.sh")
	}
	timer := readDeployFile(t, "deploy/systemd/family-budget-release-check@.timer")
	if !strings.Contains(timer, "Unit=family-budget-release-check@%i.service") || !strings.Contains(timer, "OnUnitActiveSec=5min") {
		t.Error("release timer must activate the poller service every five minutes")
	}
}
