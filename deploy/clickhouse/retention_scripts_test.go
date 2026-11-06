package clickhousedeploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetentionScriptsRejectUnsafeIdentifiers(t *testing.T) {
	for _, script := range []string{"activate-retention.sh", "backup-retention.sh"} {
		for _, id := range []string{"", "../outside", "a'unsafe", "line\nbreak", strings.Repeat("a", 65)} {
			err := exec.Command("sh", script, id).Run()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
				t.Fatalf("%s accepted unsafe identifier: %v", script, err)
			}
		}
	}
}

func TestRetentionActivationPreservesEnvironmentAndBackup(t *testing.T) {
	script, err := filepath.Abs("activate-retention.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	original := "# retained comment\nPRIVATE_PASSWORD=test-placeholder\nIMAGE_TAG=original\nRETENTION_V2_COMMON=\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		command := exec.Command("sh", script, "test-release")
		command.Dir = dir
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("activation failed: %v %s", err, output)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "# retained comment\nPRIVATE_PASSWORD=test-placeholder\nIMAGE_TAG=original\n") {
		t.Fatal("unrelated settings changed")
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		key, value, _ := strings.Cut(line, "=")
		if strings.HasPrefix(key, "RETENTION_V2_") {
			values[key] = value
		}
	}
	if len(values) != 9 {
		t.Fatalf("retention fields=%d, want 9", len(values))
	}
	for key, value := range values {
		want := "4320h"
		if key == "RETENTION_V2_PRIVACY" || key == "RETENTION_V2_DELETION_PROGRESS" {
			want = "26280h"
		}
		if value != want {
			t.Errorf("%s=%s, want %s", key, value, want)
		}
	}
	backup := filepath.Join(dir, ".env.before-retention-test-release")
	saved, err := os.ReadFile(backup)
	if err != nil || string(saved) != original {
		t.Fatal("original backup changed on repeat")
	}
	for _, file := range []string{path, backup} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("environment permissions changed")
		}
	}
}

func TestPreflightTracksMigrationFiles(t *testing.T) {
	script, err := filepath.Abs("preflight.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "migrations"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_database.sql", "002_tables.sql", "017_v2_retention.sql"} {
		if err := os.WriteFile(filepath.Join(dir, "migrations", name), []byte("SELECT 1;"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	adapter := "#!/bin/sh\nfor argument do last=$argument; done\nprintf '%s' \"$last\" > \"$CAPTURED_MANIFEST\"\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(adapter), 0700); err != nil {
		t.Fatal(err)
	}
	captured := filepath.Join(dir, "captured")
	for _, want := range []string{"1\n2\n17", "1\n2\n17\n99"} {
		command := exec.Command("sh", script)
		command.Dir = dir
		command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "CAPTURED_MANIFEST="+captured)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("preflight failed: %v %s", err, output)
		}
		got, err := os.ReadFile(captured)
		if err != nil || string(got) != want {
			t.Fatalf("migration manifest=%q, want %q: %v", got, want, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "migrations", "099_future.sql"), []byte("SELECT 1;"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
