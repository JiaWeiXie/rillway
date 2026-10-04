# Third-party components

Rillway links the modules pinned in `go.mod` and `go.sum`. Release distributors
must retain the applicable upstream license and notice files.

| Component | Purpose | Upstream license |
|---|---|---|
| Go standard library | Runtime, HTTP, TLS | BSD-3-Clause |
| github.com/charmbracelet/bubbletea | Terminal UI | MIT |
| tailscale.com | Embedded tsnet | BSD-3-Clause |
| golang.zx2c4.com/wireguard | Userspace WireGuard | MIT |
| gvisor.dev/gvisor | Userspace network stack | Apache-2.0 |

Cloudflare WARP is an external, separately installed official client. Its binary,
account state and paid license are not included in Rillway releases. Rillway does
not replace the terms associated with that software or service.

Inspect the exact dependency graph with `mise exec -- go list -m all`.
The release task includes module license/notice files alongside binaries.
