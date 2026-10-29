package deploy

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const shipImageScriptPath = "scripts/ship-image.sh"

func TestShipImageScriptShape(t *testing.T) {
	script := readDeployFile(t, shipImageScriptPath)

	for _, want := range []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		"docker compose build",
		"docker save",
		"scp ",
		"docker load",
		"--host",
		"--remote-dir",
		"--load-only",
		"--keep-tarball",
		"--dry-run",
		"ssh ",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("%s does not contain %q", shipImageScriptPath, want)
		}
	}

	// The host must come from --host, never be baked into the script, and no
	// credentials may appear in it.
	for _, forbidden := range []string{
		"sshpass",
		"BEGIN OPENSSH",
		"PRIVATE KEY",
		"password=",
		"@",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("%s contains %q; hosts and credentials must stay out of the script", shipImageScriptPath, forbidden)
		}
	}
	if ipv4.MatchString(script) {
		t.Errorf("%s contains a hardcoded IPv4 address", shipImageScriptPath)
	}
}

func TestShipImageHelpDoesNotInvokeDockerOrSSH(t *testing.T) {
	// Put failing docker/ssh/scp shims first in PATH; --help must not touch
	// them and must exit 0.
	shimDir := t.TempDir()
	sentinel := filepath.Join(shimDir, "invoked")
	for _, command := range []string{"docker", "ssh", "scp"} {
		shim := "#!/bin/sh\ntouch " + sentinel + "\nexit 99\n"
		path := filepath.Join(shimDir, command)
		if err := os.WriteFile(path, []byte(shim), 0o755); err != nil {
			t.Fatalf("write %s shim: %v", command, err)
		}
	}

	cmd := exec.Command("bash", shipImageScriptPath, "--help")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash %s --help failed: %v\n%s", shipImageScriptPath, err, output)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("--help invoked docker/ssh/scp; it must only print usage")
	}
	if !strings.Contains(string(output), "Usage:") {
		t.Errorf("--help output does not contain usage: %s", output)
	}
}

func TestNoDatabaseURLVariableAnywhere(t *testing.T) {
	// The connection string is constructed internally from POSTGRES_*
	// components. Build the needle so this guard itself does not contain it.
	needle := "DATABASE" + "_URL"

	err := filepath.WalkDir(filepath.Join(".."), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if strings.Contains(string(content), needle) {
			t.Errorf("%s contains %q; the DSN is built from POSTGRES_* components", path, needle)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
}

var ipv4 = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
