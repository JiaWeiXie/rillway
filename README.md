# Rillway

[English](README.md) · [繁體中文](README.zh-Hant.md)

![Rillway — Choose your route.](docs/brand/rillway-cover.png)

Rillway is an observable multi-outbound TCP proxy written in Go. Its name joins
**rill**, a small stream, and **way**, a route: choose a path for each connection
and see how that path performs.

Keep your company's Tailscale on your Mac. Send public traffic through a PAC file
to an Ubuntu VM, where Rillway routes it through direct, WARP, or WireGuard.

## One executable, guided installation

Each release target is **one standalone executable**. It includes the proxy,
HTTPS Web UI, TUI, tsnet, userspace WireGuard, images, offline Chinese/emoji fonts,
and third-party notices. The server does not need Go, Node, mise, or external UI
files. **WARP/WARP+ requires the separately installed official Cloudflare client.**

Choose `dist/rillway-linux-amd64`, `rillway-linux-arm64`, `rillway-darwin-amd64`, or
`rillway-darwin-arm64` from `mise run release`. Verify against `dist/SHA256SUMS`,
copy the executable to the target host as `rillway`, and run:

```sh
chmod +x ./rillway
./rillway setup
# Traditional Chinese prompts:
./rillway --lang zh-Hant setup
```

Use **one** setup invocation. The wizard asks for a specific local IP address,
allowed clients, four ports, and company domains to bypass. It shows a summary and
requires `yes` before creating files. Loopback is the default; for LAN access,
choose the VM's assigned LAN IP and allow your Mac's IP. Linux asks for sudo when
installing; the daemon runs as the dedicated `rillway` user. macOS installs the
current user's LaunchAgent without sudo.

A scripted **first installation** can supply the same settings explicitly:

```sh
./rillway setup --yes --listen 192.0.2.20 \
  --allow-client 192.0.2.30 --bypass-domains local,ts.net,tailscale.com,corp.example
```

The IPs are examples; replace them with your VM and client addresses. Company
bypass domains supplement your Mac's Tailscale workflow. Setup does not register,
license, or connect WARP and does not change your Mac's network settings.

New Linux installations use:

| Location | Purpose |
| --- | --- |
| `/usr/local/lib/rillway/rillway` | Standalone executable |
| `/usr/local/bin/rillway` | PATH symlink |
| `/etc/rillway/config.json` | Effective service configuration |
| `/var/lib/rillway/` | Private credentials and VPN state |
| `/etc/systemd/system/rillway.service` | Enabled and started systemd unit |

Configuration/state directories are `0700`; configuration and credentials are
`0600`. Setup prints the TLS fingerprint and **token path**, never its contents.
Verify the fingerprint, open the displayed HTTPS URL, and read the private token
file locally to sign in. The default certificate is self-signed; trust the verified
certificate or supply your own valid certificate.

Existing installations and retained files are rejected before installation.
Update the binary while preserving configuration and state; do not rerun setup
or install as an upgrade. Legacy `/var/lib/rillway/config.json` deployments remain
supported and are not automatically migrated.

```sh
rillway service status
sudo rillway service restart
sudo rillway tui --config /etc/rillway/config.json
# Configuration only, without sudo or service changes:
./rillway setup --no-install --config .local/config.json
./rillway serve --config .local/config.json
```

See the [complete CLI reference](docs/cli.md) for every command, option, defaults,
service operations, upgrades, Docker exports, and Mac PAC restoration.

## Capabilities and limits

- HTTP forwarding, HTTPS CONNECT, SOCKS5 TCP; source CIDR restrictions and optional proxy authentication.
- Official WARP/WARP+ Local Proxy management, embedded Tailscale tsnet, userspace WireGuard.
- Domain/suffix and literal IP/CIDR rules; failed fixed VPN routes never fall back to direct.
- HTTPS Web UI and Bubble Tea TUI share `/api/v1`, token authentication, revision checks, and atomic configuration updates.
- English and Traditional Chinese (`zh-Hant`) UI/CLI; bundled Web fonts work offline. Terminal glyphs use your terminal's fonts.
- A searchable bilingual **Glossary** explains 37 terms with examples, including the difference between a direct outbound and bypassing the proxy. Open it from the Web UI sidebar.
- Per-second connection, rate, byte, latency, known destination IP, outbound, and rule observations. HTTPS paths/content are not decrypted.
- Opt-in adaptive routing based on connection success/timeouts and latency; only new connections change routes.
- Ubuntu systemd, macOS LaunchAgent, reversible PAC settings per macOS network service.
- Explicit GitHub diagnostics and bounded download comparisons.
- Docker pull/push, Build, and container HTTP/HTTPS exports: daemon, client, environment, Compose; merge existing JSON without modifying the input.

No whole-host TUN, SOCKS5 UDP, HTTPS interception, or transparent proxy. Built-in
`direct` cannot be deleted/disabled. Initial WARP is disabled: its fixed GitHub CDN
rules fail until WARP is configured or you explicitly replace those rules. Company
and private networks stay on fixed routes. Throughput observations alone do not
prove an alternative path is faster.

For browser proxy settings, use the HTTP listener for both HTTP and HTTPS proxies,
or the SOCKS5 listener with proxy-side DNS. LAN addresses replace loopback. Mac PAC
bypass connections never pass through Rillway and do not appear in observations.

## Development

```sh
mise trust
mise install
mise run check
mise run build
mise run dev
# In another terminal:
./bin/rillway tui --config .local/config.json
```

`dev` creates `.local/config.json` and private credentials on its first run; the
management UI is `https://127.0.0.1:17892`. It prints the fingerprint and token file
path. TUI `i`, then Enter installs a new background service; existing services are
protected. Stop any foreground daemon before installing its service. Use `serve` for foreground development.

`mise.toml`/`mise.lock` pin Go, gopls, golangci-lint, and git-cliff. LSP:
`mise exec -- gopls`; VS Code uses `scripts/gopls` with format/imports on save.
Launch the editor where mise is available, for example `mise exec -- code .`.

| Task | Behavior |
| --- | --- |
| `mise run dev` | Foreground local daemon/Web UI |
| `mise run build` | Build `bin/rillway` |
| `mise run fmt` | Apply gofumpt/goimports |
| `mise run lint` | Validate lint configuration, lint, and check formatting |
| `mise run test` | Account-free tests |
| `mise run check` | Lint, race detector, shuffled tests, coverage |
| `mise run fuzz` | Time-bounded configuration/SOCKS5 fuzzing |
| `mise run test:live` | Explicit external-account tests |
| `mise run notices` | Refresh embedded full third-party notices |
| `mise run release` | Four standalone Linux/macOS binaries and checksums |
| `mise run hooks:install` | Enable repository-local Git hooks |
| `mise run hooks:check` | Check the exact staged snapshot |
| `mise run changelog:preview` | Preview git-cliff changelog |
| `mise run changelog` | Regenerate changelog |

Tests retain CGO for the race detector; releases use `CGO_ENABLED=0`. PAC execution
tests use Node only during testing and explicitly skip if absent. Git/agent hooks
invoke the mise tools directly.
Caches and local secrets are ignored by Git. Refresh notices after dependency or
font-license changes; release does this automatically before compiling.

## Documentation

- [CLI: English](docs/cli.md) · [CLI：繁體中文](docs/cli.zh-Hant.md)
- [Ubuntu/LAN deployment, upgrades, Mac PAC](docs/deployment.md)
- [WARP+, Tailscale, WireGuard](docs/providers.md)
- [Routing, adaptive decisions, observations, API](docs/architecture.md)
- [Verification and unverified external environments](docs/verification.md)
- [Docker, Build, Compose, OrbStack](docs/docker.md)
- [Languages, Chinese fonts, emoji](docs/i18n.md)
- [Agent guidance and local hooks](docs/agent-workflow.md)
- [Git hooks and git-cliff](docs/changelog.md)
- [Logo and image assets](docs/brand/README.md)
- [Third-party components and notices](THIRD_PARTY.md)

Operational and architecture documents linked above currently use Traditional
Chinese; the README and complete CLI reference are available in both languages.
