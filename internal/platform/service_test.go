package platform

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceTemplates(t *testing.T) {
	u, err := SystemdUnit("/opt/my app/rillway", "/var/lib/rillway/config.json")
	if err != nil || !strings.Contains(u, "User=rillway") || !strings.Contains(u, `"/opt/my app/rillway"`) {
		t.Fatal(u, err)
	}
	if !strings.Contains(u, "StateDirectory=rillway\nStateDirectoryMode=0700\n") {
		t.Fatal("service state directory must remain private", u)
	}
	if _, err = SystemdUnit("/tmp/x\nEvil=y", "/tmp/c"); err == nil {
		t.Fatal("newline injection")
	}
	p, err := LaunchAgent("/x/rillway", "/x/a&b.json", "/x")
	if err != nil || !strings.Contains(p, "a&amp;b.json") {
		t.Fatal(p, err)
	}
}

func TestPACRollbackAndRestore(t *testing.T) {
	var calls [][]string
	failed := false
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if strings.HasPrefix(args[0], "-get") {
			if args[0] == "-getautoproxyurl" {
				return []byte("URL: http://old/pac\nEnabled: Yes\n"), nil
			}
			return []byte("Enabled: Yes\nServer: old\n"), nil
		}
		if args[0] == "-setsecurewebproxystate" && args[2] == "off" && !failed {
			failed = true
			return nil, errors.New("mock failure")
		}
		return nil, nil
	}
	backup := filepath.Join(t.TempDir(), "backup.json")
	if err := ApplyPAC(context.Background(), run, "Wi-Fi", "http://new/pac", backup); err == nil {
		t.Fatal("expected failure")
	}
	restored := false
	for _, c := range calls {
		if c[0] == "-setautoproxyurl" && c[2] == "http://old/pac" {
			restored = true
		}
	}
	if !restored {
		t.Fatal("not restored", calls)
	}
	if err := ApplyPAC(context.Background(), run, "Wi-Fi", "http://new/pac", backup); err != nil {
		t.Fatal(err)
	}
	if err := RestoreProxy(context.Background(), run, backup); err != nil {
		t.Fatal(err)
	}
}

func TestPACRollbackUsesIndependentContextAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	restored := false
	run := func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if strings.HasPrefix(args[0], "-get") {
			if args[0] == "-getautoproxyurl" {
				return []byte("URL: http://old/pac\nEnabled: Yes\n"), nil
			}
			return []byte("Enabled: Yes\n"), nil
		}
		if args[0] == "-setautoproxyurl" && args[2] == "http://new/pac" {
			cancel()
			return nil, nil
		}
		if args[0] == "-setautoproxyurl" && args[2] == "http://old/pac" {
			restored = true
		}
		return nil, nil
	}
	backup := filepath.Join(t.TempDir(), "backup.json")
	if err := ApplyPAC(ctx, run, "Wi-Fi", "http://new/pac", backup); err == nil {
		t.Fatal("expected cancellation")
	}
	if !restored {
		t.Fatal("canceled operation prevented PAC rollback")
	}
}
