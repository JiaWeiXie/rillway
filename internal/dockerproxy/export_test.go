package dockerproxy

import (
	"bytes"
	"encoding/json"
	"rillway/internal/config"
	"strings"
	"testing"
)

func TestSeparateDockerSettingsAndMerge(t *testing.T) {
	s, err := New("http://proxy.example:17890", "localhost, corp.example,10.0.0.0/8,corp.example")
	if err != nil {
		t.Fatal(err)
	}
	if s.NoProxy != "localhost,corp.example,10.0.0.0/8" {
		t.Fatal(s)
	}
	for _, target := range []string{"daemon", "client"} {
		existing := []byte(`{"auths":{"private.example":{"auth":"credential-to-preserve"}},"limit":9007199254740993,"proxies":{"other-daemon":{"httpProxy":"http://existing:80"},"default":{"ftpProxy":"ftp://existing:21"}},"features":{"buildkit":true}}`)
		data, err := s.Export(target, existing)
		if err != nil {
			t.Fatal(err)
		}
		var root map[string]json.RawMessage
		if err = json.Unmarshal(data, &root); err != nil {
			t.Fatal(err)
		}
		if string(root["limit"]) != "9007199254740993" || !strings.Contains(string(root["auths"]), "credential-to-preserve") || string(root["features"]) == "" {
			t.Fatal("unrelated Docker settings were lost")
		}
		var proxy map[string]json.RawMessage
		if err = json.Unmarshal(root["proxies"], &proxy); err != nil {
			t.Fatal(err)
		}
		if target == "daemon" {
			if string(proxy["http-proxy"]) != `"http://proxy.example:17890"` || string(proxy["https-proxy"]) != string(proxy["http-proxy"]) {
				t.Fatal(string(data))
			}
		} else {
			var defaults map[string]string
			if err = json.Unmarshal(proxy["default"], &defaults); err != nil {
				t.Fatal(err)
			}
			if defaults["httpsProxy"] != s.ProxyURL || defaults["noProxy"] != s.NoProxy || defaults["ftpProxy"] != "ftp://existing:21" || !bytes.Contains(proxy["other-daemon"], []byte("http://existing:80")) {
				t.Fatal(string(data))
			}
		}
		again, err := s.Export(target, data)
		if err != nil || !bytes.Equal(data, again) {
			t.Fatal("merge is not idempotent", err)
		}
	}
}

func TestDockerAddressAndBypassValidation(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:17890", "http://localhost:17890", "http://[::1]:17890", "http://192.168.1.10:17890", "http://[2001:db8::1]:80"} {
		s, err := New(endpoint, "localhost,*.corp.example,.ts.net,127.0.0.0/8,::1,fc00::/7")
		if err != nil {
			t.Fatal(endpoint, err)
		}
		want := strings.Contains(endpoint, "127.0.0.1") || strings.Contains(endpoint, "localhost") || strings.Contains(endpoint, "[::1]")
		if s.Loopback != want {
			t.Fatal(s)
		}
	}
	for _, endpoint := range []string{"", "socks5://proxy:80", "https://proxy:80", "http://proxy", "http://proxy:0", "http://proxy:65536", "http://user@proxy:80", "http://proxy:80/", "http://proxy:80?x=secret", "http://proxy:80#", "http://0.0.0.0:80", "http://[::]:80", "http://a$(echo):80", "http://proxy\n:80"} {
		if _, err := New(endpoint, ""); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe URL accepted or echoed: %q", endpoint)
		}
	}
	for _, bypass := range []string{"corp.example\nBAD=value", "bad entry", "corp.example;command", "$(command)", "10.0.0.0/99", strings.Repeat("a", 8193)} {
		if _, err := New("http://proxy:80", bypass); err == nil {
			t.Fatal("invalid bypass accepted")
		}
	}
}

func TestDockerExportRejectsMalformedInput(t *testing.T) {
	s, err := New("http://proxy:80", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"null", "[]", "false", "{} {}", `{"proxies":[]}`, `{"proxies":{"default":[]}}`, strings.Repeat(" ", 1<<20) + "{}"} {
		if _, err := s.Export("client", []byte(input)); err == nil {
			t.Fatal("malformed Docker settings accepted")
		}
	}
	if _, err := s.Export("unknown", nil); err == nil {
		t.Fatal("unknown target accepted")
	}
	if _, err := s.Export("env", []byte("{}")); err == nil {
		t.Fatal("environment merge accepted")
	}
}

func TestDockerExportsOmitSecretsAndSupportContainerEnv(t *testing.T) {
	c := config.Default("secrets")
	c.PAC.ProxyAddress = "192.168.1.10:17890"
	c.Security.ProxyUsername = "private-user"
	c.Security.ProxyPasswordFile = "private-password-file"
	s, err := FromConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Bundle(true)
	if err != nil {
		t.Fatal(err)
	}
	if !b.AuthRequired || b.Loopback || !strings.Contains(b.NoProxy, "100.64.0.0/10") {
		t.Fatal(b.Settings)
	}
	for target, data := range b.Exports {
		if strings.Contains(data, "private-") || strings.Contains(data, "admin.token") {
			t.Fatal("secret exported", target)
		}
	}
	for _, variable := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
		if !strings.Contains(b.Exports["env"], variable+"=") || !strings.Contains(b.Exports["compose"], variable+":") {
			t.Fatal("missing container variable", variable)
		}
	}
	if strings.Contains(b.Exports["env"], "'") || strings.Contains(b.Exports["env"], "\"") {
		t.Fatal("docker --env-file must not receive quoted values")
	}
	c.Listeners.HTTP = ""
	if _, err = FromConfig(c); err == nil {
		t.Fatal("HTTP listener is required")
	}
}
