package clickhousedeploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestConfigureV2UsersExecutesCurrentHostScript(t *testing.T) {
	script, err := filepath.Abs("configure-v2-users.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	initDir := filepath.Join(dir, "deploy", "clickhouse", "init")
	if err := os.MkdirAll(initDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(initDir, "010-users.sh"), []byte("printf '%s\\n' current-host-version\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	adapter := `#!/bin/sh
set -eu
[ "$#" -eq 6 ] || exit 2
for expected in compose exec -T clickhouse sh; do
  [ "$1" = "$expected" ] || exit 2
  shift
done
case "$1" in
  /docker-entrypoint-initdb.d/900-users.sh) printf '%s\n' stale-mounted-version ;;
  -s) exec sh -s ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(adapter), 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", script)
	command.Dir = dir
	command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("configure failed: %v %s", err, output)
	}
	if string(output) != "current-host-version\n" {
		t.Fatalf("executed script output=%q, want current-host-version", output)
	}
}
