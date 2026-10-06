package memorylimit

import (
	"rillway/internal/config"
	"testing"
)

func TestUnitsAndPercentageUseHostCapacity(t *testing.T) {
	s := Status{HostBytes: 4 * GiB, MinimumBytes: 256 * MiB, MaximumBytes: Maximum(4 * GiB)}
	for _, tc := range []struct {
		mode, value string
		want        uint64
	}{
		{"percent", "25", GiB}, {"MiB", "512", 512 * MiB}, {"GiB", "0.5", 512 * MiB}, {"GiB", "1.5", GiB + 512*MiB},
	} {
		got, err := Calculate(Request{Mode: tc.mode, Value: tc.value}, s)
		if err != nil || got != tc.want {
			t.Fatal(tc, got, err)
		}
	}
	for _, tc := range []Request{
		{Mode: "MiB", Value: "255"},
		{Mode: "GiB", Value: "4"},
		{Mode: "percent", Value: "100"},
		{Mode: "GiB", Value: "-1"},
		{Mode: "GiB", Value: "NaN"},
		{Mode: "GiB", Value: "1e8"},
		{Mode: "GiB", Value: "0"},
		{Mode: "unknown", Value: "512"},
	} {
		if _, err := Calculate(tc, s); err == nil {
			t.Fatal("unsafe limit accepted", tc)
		}
	}
}

func TestFloorUsesEnabledProvidersAndLiveUsage(t *testing.T) {
	c := config.Config{Outbounds: []config.Outbound{{Type: "warp", Enabled: true}, {Type: "wireguard"}}}
	if floor, _ := SafeMinimum(c, 10*MiB); floor != 256*MiB {
		t.Fatal(floor)
	}
	c.Outbounds[1].Enabled = true
	if floor, _ := SafeMinimum(c, 10*MiB); floor != GiB {
		t.Fatal(floor)
	}
	if floor, _ := SafeMinimum(c, GiB); floor != 1280*MiB {
		t.Fatal(floor)
	}
	if floor, _ := SafeMinimum(config.Config{}, 257*MiB); floor != 322*MiB {
		t.Fatal(floor)
	}
	if GoBudget(512*MiB) >= 512*MiB {
		t.Fatal("Go budget leaves no non-Go headroom")
	}
}
