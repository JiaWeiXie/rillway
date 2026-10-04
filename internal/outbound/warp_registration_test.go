package outbound

import (
	"context"
	"errors"
	"reflect"
	"rillway/internal/config"
	"strings"
	"testing"
	"time"
)

func TestWARPRegistrationPreservesExistingDevice(t *testing.T) {
	for _, account := range []string{"Free", "Unlimited"} {
		t.Run(account, func(t *testing.T) {
			w := newWARP(config.Outbound{ID: "warp"})
			verified := time.Now()
			w.verified, w.manualStop = verified, true
			calls := 0
			w.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				calls++
				if !reflect.DeepEqual(args, []string{"--accept-tos", "registration", "show"}) {
					t.Fatalf("existing registration must not be modified: %v", args)
				}
				return []byte("Account type: " + account + "\nLicense: private-license\nDevice ID: private-device"), nil
			}
			for range 2 {
				if err := w.Action(t.Context(), "register", ""); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 2 || w.verified != verified || !w.manualStop {
				t.Fatal("repeat registration changed device lifecycle or verification")
			}
		})
	}
}

func TestWARPRegistrationHandlesConcurrentRegistration(t *testing.T) {
	w := newWARP(config.Outbound{ID: "warp"})
	var commands []string
	w.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		commands = append(commands, strings.Join(args[1:], " "))
		if len(commands) == 3 {
			return []byte("Account type: Unlimited\nLicense: private-license"), nil
		}
		return []byte("private-device"), errors.New("registration changed meanwhile")
	}
	if err := w.Action(t.Context(), "register", ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(commands, []string{"registration show", "registration new", "registration show"}) {
		t.Fatalf("unexpected commands: %v", commands)
	}
}

func TestWARPRegistrationFailureRemainsSafe(t *testing.T) {
	w := newWARP(config.Outbound{ID: "warp"})
	calls := 0
	w.run = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		calls++
		return []byte("private-license"), errors.New("failure with private-license")
	}
	err := w.Action(t.Context(), "register", "")
	var public interface{ PublicMessage() string }
	if calls != 3 || !errors.As(err, &public) || !strings.Contains(public.PublicMessage(), "registration failed") || strings.Contains(err.Error(), "private-license") {
		t.Fatalf("unexpected registration failure: %v", err)
	}
}
