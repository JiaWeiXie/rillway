package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseVersionAndArtifactAllowlist(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "fake-bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"release.sh", "release-notices.go"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(filepath.Join(root, "scripts"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, "scripts", name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	mock := `#!/bin/sh
printf '%s\n' "$*" >> "$RILLWAY_TEST_LOG"
if [ "$1" = build ]; then
 previous=
 for arg do
  if [ "$previous" = -o ]; then output=$arg; fi
  previous=$arg
 done
 printf 'synthetic binary\n' > "$output"
elif [ "$3" = --hashes-only ]; then
 exec "$RILLWAY_TEST_REAL_GO" "$@"
fi
`
	if err = os.WriteFile(filepath.Join(bin, "go"), []byte(mock), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "dist"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "dist", "rillway-private-backup"), []byte("not publishable"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(version string) ([]byte, error) {
		c := exec.Command("sh", "scripts/release.sh")
		c.Dir = root
		c.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "RILLWAY_RELEASE_VERSION="+version, "RILLWAY_TEST_LOG="+filepath.Join(root, "log"), "RILLWAY_TEST_REAL_GO="+realGo)
		return c.CombinedOutput()
	}
	if out, err := run("v1.2.3-rc.1"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	hashes, err := os.ReadFile(filepath.Join(root, "dist", "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(hashes), "private") || strings.Count(string(hashes), "\n") != 4 {
		t.Fatalf("unlisted artifact included: %s", hashes)
	}
	log, err := os.ReadFile(filepath.Join(root, "log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "-X main.version=v1.2.3-rc.1") != 4 {
		t.Fatal("version not embedded in every target")
	}
	if out, err := run("v1.2.3 -X main.injected=value"); err == nil || !strings.Contains(string(out), "Invalid release version") {
		t.Fatal("linker argument injection accepted")
	}
}
