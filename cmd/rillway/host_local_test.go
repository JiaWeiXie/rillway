package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/i18n"
	"slices"
	"strings"
	"testing"
)

func TestAgentValidateHostLocalSummaryRemainsOffline(t *testing.T) {
	for _, locale := range []i18n.Locale{i18n.English, i18n.TraditionalChinese} {
		for _, mode := range []string{"absent", "open", "strict"} {
			t.Run(string(locale)+"/"+mode, func(t *testing.T) {
				state := filepath.Join(t.TempDir(), "must-not-be-created")
				c := config.Default(state)
				c.Revision = 37
				c.HostLocal = nil
				want := "local-clients-only"
				if mode != "absent" {
					c.HostLocal = &config.HostLocal{Mode: mode}
					want = mode
				}
				input, err := json.Marshal(c)
				if err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				err = agentCommand(i18n.WithLocale(t.Context(), locale), []string{"validate", "--input", "-"}, bytes.NewReader(input), &out)
				if err != nil {
					t.Fatal(err)
				}
				var result struct {
					SchemaVersion int           `json:"schema_version"`
					OK            bool          `json:"ok"`
					Error         *agentFailure `json:"error"`
					Data          struct {
						Valid           bool                    `json:"valid"`
						Revision        uint64                  `json:"revision"`
						RuntimeVerified bool                    `json:"runtime_verified"`
						Host            config.HostLocalSummary `json:"host_local"`
					} `json:"data"`
				}
				decoder := json.NewDecoder(&out)
				if err := decoder.Decode(&result); err != nil || out.Len() != 0 {
					t.Fatal("not one JSON envelope", err)
				}
				if !result.OK || result.Error != nil || result.SchemaVersion != 1 || !result.Data.Valid || result.Data.Revision != 37 || result.Data.RuntimeVerified {
					t.Fatal("validation changed existing contract", result)
				}
				host := result.Data.Host
				if host.EffectiveMode != want || host.RollbackField != "host_local.mode" || host.RollbackValue != "open" || host.ListenerProtection == "" {
					t.Fatal("incomplete doctor substitute", host)
				}
				for _, baseline := range []string{"169.254.0.0/16", "fe80::/10", "fd00:ec2::254", "192.0.0.192", "100.100.100.200"} {
					if !slices.Contains(host.Baseline, baseline) {
						t.Fatal("baseline missing", baseline)
					}
				}
				if !strings.Contains(host.Notice, "open") || locale == i18n.TraditionalChinese && !strings.Contains(host.Notice, "請將 host_local.mode") {
					t.Fatal("missing localized rollback guidance", host.Notice)
				}
				if _, err := os.Stat(state); !os.IsNotExist(err) {
					t.Fatal("offline validation created/read credentials", err)
				}
			})
		}
	}
	result, err := invokeAgent(t, []string{"validate", "--input", "-"}, `{"host_local":{"mode":"invalid-input-must-not-leak"}}`)
	if err == nil || result.Error == nil || result.Error.ExitCode != 2 || result.Error.Code != "invalid_configuration" || strings.Contains(result.Error.Message, "invalid-input-must-not-leak") {
		t.Fatal("validation error/exit contract changed", result, err)
	}
}
