package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// nextVersionScript is the repository-root path of the version derivation
// script under test.
const nextVersionScript = "scripts/next-version.sh"

// versionRepo is a throwaway git repository used to exercise
// scripts/next-version.sh without touching the real checkout.
type versionRepo struct {
	t          *testing.T
	dir        string
	scriptPath string
	env        []string
}

func newVersionRepo(t *testing.T) *versionRepo {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	scriptPath, err := filepath.Abs(filepath.Join("..", nextVersionScript))
	if err != nil {
		t.Fatalf("resolve %s: %v", nextVersionScript, err)
	}
	dir := t.TempDir()

	repo := &versionRepo{
		t:          t,
		dir:        dir,
		scriptPath: scriptPath,
		// Isolate from the developer's global/system git configuration so
		// identity, signing and default branch settings cannot leak in.
		env: append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_CONFIG_NOSYSTEM=1",
			"HOME="+dir,
			"XDG_CONFIG_HOME="+dir,
		),
	}
	repo.git("init", "-q")
	repo.git("config", "user.name", "Release Plan Tester")
	repo.git("config", "user.email", "version-test@example.com")
	repo.git("config", "commit.gpgsign", "false")
	return repo
}

func (r *versionRepo) git(args ...string) {
	r.t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = r.env
	if output, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func (r *versionRepo) gitOutput(args ...string) string {
	r.t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = r.env
	output, err := cmd.Output()
	if err != nil {
		r.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}

// commit records an empty commit with the given subject and optional body
// paragraphs, mirroring "git commit -m subject -m paragraph...".
func (r *versionRepo) commit(subject string, body ...string) {
	r.t.Helper()

	args := []string{"commit", "-q", "--allow-empty", "-m", subject}
	for _, paragraph := range body {
		args = append(args, "-m", paragraph)
	}
	r.git(args...)
}

func (r *versionRepo) tag(name string) {
	r.t.Helper()

	r.git("tag", name)
}

// parseAssignments reads key=value lines, ignoring blank lines and any
// surrounding log output.
func parseAssignments(text string) map[string]string {
	assignments := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "=") {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		assignments[key] = value
	}
	return assignments
}

// run executes scripts/next-version.sh with GITHUB_OUTPUT pointed at a temp
// file and returns the parsed outputs, asserting stdout and the file agree on
// should_release, version and tag.
func (r *versionRepo) run() map[string]string {
	r.t.Helper()

	outputFile := filepath.Join(r.dir, "github-output.txt")
	cmd := exec.Command("bash", r.scriptPath)
	cmd.Dir = r.dir
	cmd.Env = append(r.env, "GITHUB_OUTPUT="+outputFile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("next-version.sh failed: %v\n%s", err, output)
	}

	stdout := parseAssignments(string(output))
	fileBytes, err := os.ReadFile(outputFile)
	if err != nil {
		r.t.Fatalf("read GITHUB_OUTPUT file: %v", err)
	}
	fromFile := parseAssignments(string(fileBytes))

	for _, key := range []string{"should_release", "version", "tag"} {
		if _, ok := stdout[key]; !ok {
			r.t.Errorf("stdout is missing %s; got:\n%s", key, output)
		}
		if stdout[key] != fromFile[key] {
			r.t.Errorf("%s: stdout = %q, GITHUB_OUTPUT file = %q", key, stdout[key], fromFile[key])
		}
	}
	return stdout
}

// runExpectFailure executes the script and requires a non-zero exit whose
// combined output contains wantText.
func (r *versionRepo) runExpectFailure(wantText string) {
	r.t.Helper()

	outputFile := filepath.Join(r.dir, "github-output.txt")
	cmd := exec.Command("bash", r.scriptPath)
	cmd.Dir = r.dir
	cmd.Env = append(r.env, "GITHUB_OUTPUT="+outputFile)
	output, err := cmd.CombinedOutput()
	if err == nil {
		r.t.Fatalf("next-version.sh unexpectedly succeeded:\n%s", output)
	}
	if !strings.Contains(string(output), wantText) {
		r.t.Errorf("failure output does not contain %q:\n%s", wantText, output)
	}
}

func TestNextVersionScriptDerivesVersionsFromConventionalCommits(t *testing.T) {
	cases := []struct {
		name    string
		build   func(r *versionRepo)
		release string
		version string
		tag     string
	}{
		{
			name: "feature without any tag starts at 0.1.0",
			build: func(r *versionRepo) {
				r.commit("chore: bootstrap")
				r.commit("feat: x")
			},
			release: "true", version: "0.1.0", tag: "v0.1.0",
		},
		{
			name: "fix bumps the patch",
			build: func(r *versionRepo) {
				r.commit("chore: bootstrap")
				r.tag("v1.2.3")
				r.commit("fix: y")
			},
			release: "true", version: "1.2.4", tag: "v1.2.4",
		},
		{
			name: "feat bumps the minor and resets the patch",
			build: func(r *versionRepo) {
				r.commit("chore: bootstrap")
				r.tag("v1.2.3")
				r.commit("feat: z")
			},
			release: "true", version: "1.3.0", tag: "v1.3.0",
		},
		{
			name: "breaking exclamation mark bumps the major",
			build: func(r *versionRepo) {
				r.commit("chore: bootstrap")
				r.tag("v1.2.3")
				r.commit("feat!: w")
			},
			release: "true", version: "2.0.0", tag: "v2.0.0",
		},
		{
			name: "breaking change footer bumps the major",
			build: func(r *versionRepo) {
				r.commit("chore: bootstrap")
				r.tag("v1.2.3")
				r.commit("fix: api", "BREAKING CHANGE: drop api")
			},
			release: "true", version: "2.0.0", tag: "v2.0.0",
		},
		{
			name: "non-releasable types do not bump",
			build: func(r *versionRepo) {
				r.commit("chore: bootstrap")
				r.tag("v1.2.3")
				r.commit("docs: a")
				r.commit("chore: b")
				r.commit("ci: c")
				r.commit("refactor: d")
			},
			release: "false", version: "", tag: "",
		},
		{
			name: "merge commits are ignored even when bodies list features",
			build: func(r *versionRepo) {
				r.commit("chore: bootstrap")
				r.commit("Merge pull request #1 from nickmandev/feat/x", "feat: inner change")
			},
			release: "false", version: "", tag: "",
		},
		{
			name: "minor wins over patch",
			build: func(r *versionRepo) {
				r.commit("chore: bootstrap")
				r.tag("v1.2.3")
				r.commit("fix: y")
				r.commit("feat: z")
			},
			release: "true", version: "1.3.0", tag: "v1.3.0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newVersionRepo(t)
			tc.build(repo)
			got := repo.run()
			if got["should_release"] != tc.release {
				t.Errorf("should_release = %q, want %q", got["should_release"], tc.release)
			}
			if got["version"] != tc.version {
				t.Errorf("version = %q, want %q", got["version"], tc.version)
			}
			if got["tag"] != tc.tag {
				t.Errorf("tag = %q, want %q", got["tag"], tc.tag)
			}
		})
	}
}

func TestNextVersionScriptRefusesExistingComputedTag(t *testing.T) {
	repo := newVersionRepo(t)
	repo.commit("chore: bootstrap")
	repo.tag("v1.2.3")
	repo.commit("feat: x")

	// Consume v1.3.0 on a side branch so the tag exists but is not an
	// ancestor of HEAD; the script must fail instead of reusing it.
	head := repo.gitOutput("symbolic-ref", "--short", "HEAD")
	repo.git("checkout", "-q", "-b", "side")
	repo.commit("chore: future release")
	repo.tag("v1.3.0")
	repo.git("checkout", "-q", head)

	repo.runExpectFailure("already exists")
}
