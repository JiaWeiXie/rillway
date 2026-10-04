# Third-party components

Rillway links the modules pinned in `go.mod` and `go.sum`. Release distributors
must retain the applicable upstream license and notice files.

| Component | Purpose | Upstream license |
|---|---|---|
| Go standard library | Runtime, HTTP, TLS | BSD-3-Clause |
| github.com/charmbracelet/bubbletea | Terminal UI | MIT |
| github.com/rivo/uniseg | Terminal grapheme segmentation and display width | MIT |
| tailscale.com | Embedded tsnet | BSD-3-Clause |
| golang.zx2c4.com/wireguard | Userspace WireGuard | MIT |
| gvisor.dev/gvisor | Userspace network stack | Apache-2.0 |

Cloudflare WARP is an external, separately installed official client. Its binary,
account state and paid license are not included in Rillway releases. Rillway does
not replace the terms associated with that software or service.

Inspect the exact dependency graph with `mise exec -- go list -m all`.
The release task includes module license/notice files alongside binaries.

## Embedded fonts

The Web UI includes unmodified Noto Sans TC and Noto Color Emoji font binaries,
both under the SIL Open Font License 1.1. Their license files are embedded next to
the fonts and copied to `dist/_licenses/fonts/` by the release task. Exact sources,
revisions, and SHA-256 checksums are recorded in [the font inventory](docs/brand/fonts.md).
No font CDN or host font installation is required for the Web UI.
