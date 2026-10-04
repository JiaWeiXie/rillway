package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerCLIExportsWithoutChangingSource(t *testing.T) {
	t.Setenv("RILLWAY_LANG", "en")
	path := filepath.Join(t.TempDir(), "docker.json")
	previous := []byte(`{"auths":{"corp":{"auth":"preserve-me"}},"credsStore":"existing"}`)
	if err := os.WriteFile(path, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(t.Context(), []string{"docker", "export", "--target", "client", "--proxy-url", "http://proxy:17890", "--input", path, "--no-proxy", "corp.example"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"httpsProxy": "http://proxy:17890"`) || !strings.Contains(out.String(), "preserve-me") {
		t.Fatal(out.String())
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(previous, unchanged) {
		t.Fatal("source was modified", err)
	}
	out.Reset()
	if err := run(t.Context(), []string{"docker", "export", "--target", "env", "--proxy-url", "http://proxy:17890", "--no-proxy", ""}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "NO_PROXY=\n") {
		t.Fatal("explicit empty bypass ignored")
	}
	if err := run(t.Context(), []string{"docker", "export", "--proxy-url", "socks5://proxy:80", "--lang", "zh-Hant"}, &out); err == nil || !strings.Contains(err.Error(), "Docker Proxy 網址") {
		t.Fatal(err)
	}
}
