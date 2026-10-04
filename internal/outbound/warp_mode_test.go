package outbound

import "testing"

func TestWARPModeRecognizesOfficialSettings(t *testing.T) {
	for _, tc := range []struct {
		settings string
		want     string
	}{
		{"(user set)\tMode: WarpProxy on port 40000", "proxy"},
		{"Other setting: enabled\n(user set)\tMode: WarpProxy on port 40000\nProxy: unrelated", "proxy"},
		{"\t(user set)\tMode: WarpProxy on port 40000\r\n", "proxy"},
		{"Mode: WarpProxy on port 1", "proxy"},
		{"Mode: WarpProxy on port 65535", "proxy"},
		{"Mode: warpproxy\ton\tport\t40000", "proxy"},
		{"(user set)\tMode: WarpProxy", "proxy"},
		{"Mode: proxy", "proxy"},
		{"(default)\tMode: Warp", "warp"},
		{"Mode: TunnelOnly", "tunnel_only"},
		{"Mode: tunnel_only", "tunnel_only"},
		{"Mode: WarpDoh", "warp+doh"},
		{"Mode: warp+doh", "warp+doh"},
		{"Mode: Doh", "doh"},
		{"Mode: Dot", "dot"},
		{"Mode: WarpDot", "warp+dot"},
		{"Mode: warp+dot", "warp+dot"},
	} {
		t.Run(tc.settings, func(t *testing.T) {
			if got := warpMode(tc.settings); got != tc.want {
				t.Fatalf("warpMode(%q) = %q, want %q", tc.settings, got, tc.want)
			}
		})
	}
}

func TestWARPModeRejectsAmbiguousOrMalformedSettings(t *testing.T) {
	for _, settings := range []string{
		"",
		"Mode: ",
		"Mode: FutureMode",
		"Mode: WarpProxyDisabled",
		"Mode: NotWarpProxy on port 40000",
		"Mode: WarpProxy on port",
		"Mode: WarpProxy on port 0",
		"Mode: WarpProxy on port 65536",
		"Mode: WarpProxy on port -1",
		"Mode: WarpProxy on port +40000",
		"Mode: WarpProxy on port 40000.0",
		"Mode: WarpProxy on port ４００００",
		"Mode: WarpProxy on port 40000 (connected)",
		"Mode: WarpProxy on port 40000 extra",
		"Mode: WarpProxy on ports 40000",
		"Mode: proxy on port 40000",
		"ProxyMode: WarpProxy on port 40000",
		"Note: Mode: WarpProxy on port 40000",
		"(user set)\tMode: Warp (WarpProxy on port 40000)",
		"Mode: WarpProxy on port\n40000",
		"Mode: FutureMode\nNote: Mode: WarpProxy on port 40000",
	} {
		t.Run(settings, func(t *testing.T) {
			if got := warpMode(settings); got != "unknown" {
				t.Fatalf("warpMode(%q) = %q, want unknown", settings, got)
			}
		})
	}
}
