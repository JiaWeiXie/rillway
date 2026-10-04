# Rillway CLI reference

[English](cli.md) · [繁體中文](cli.zh-Hant.md) · [README](../README.md)

Examples use `rillway` after Linux installation. Before installation, use
`./rillway`; development uses `./bin/rillway`. Flags come after the command (and
subcommand), except the global `--lang`, which can appear before or after it.
Run `rillway help` for the command list or `rillway COMMAND --help` for options.
For subcommands use `rillway service install --help` or `rillway docker export --help`.
No arguments opens the TUI; it does **not** start the setup wizard.

## Language, paths, output, exit status

```sh
rillway --lang zh-Hant help
RILLWAY_LANG=zh-Hant rillway setup
rillway tui --lang en --config .local/config.json
```

`--lang en|zh-Hant` overrides `RILLWAY_LANG`; English is the default. Web UI language
is selected independently. TUI `L`/`Ctrl+L` changes language; use `Ctrl+L` while
entering text. User data and unknown upstream diagnostics are preserved.
Web fonts are embedded; install/select a CJK/emoji-capable terminal font yourself
if terminal glyphs are missing. Rillway does not install host fonts.

On Linux, the default configuration first uses an existing `/etc/rillway/config.json`, then the legacy `/var/lib/rillway/config.json`. Otherwise it uses `$XDG_CONFIG_HOME/rillway/config.json`, usually `~/.config/rillway/config.json`. macOS uses `~/Library/Application Support/rillway/config.json`. An explicit `--config FILE` overrides discovery; relative paths resolve from the working directory. Installed Linux configuration is private, so local administration requires the appropriate permissions, usually `sudo`. Do not start a second daemon sharing listeners or Tailscale state. Remote TUI connections have a separate client profile.

Success/help is exit `0`; invalid options, setup EOF, validation failures and
failed operations return `1`. Cancelling a wizard with an answer other than `yes`
returns `0` without writes. Signals cancel long-running work. `serve`/TUI run until
stopped. Normal output goes to stdout; final CLI errors go to stderr.
`pac`, `docker export`, `diagnose`, and `licenses` produce redirectable output.
Credentials are read from files; setup/init output only token paths.

## `setup`: guided first installation

```sh
./rillway setup
./rillway setup --yes --listen 192.0.2.20 --allow-client 192.0.2.30 \
  --bypass-domains local,ts.net,tailscale.com,corp.example
./rillway setup --no-install --config .local/config.json
```

| Option | Default | Meaning |
| --- | --- | --- |
| `--config FILE` | User config path | New staging configuration, retained after installation |
| `--listen IP` | `127.0.0.1` | Specific local IPv4/IPv6 address for all four listeners |
| `--allow-client CSV` | Selected listener IP | Allowed source IPs/CIDRs, comma-separated |
| `--http-port PORT` | `17890` | HTTP forwarding and HTTPS CONNECT |
| `--socks-port PORT` | `17891` | SOCKS5 TCP |
| `--admin-port PORT` | `17892` | HTTPS management |
| `--pac-port PORT` | `17893` | HTTP PAC endpoint |
| `--bypass-domains CSV` | `local,ts.net,tailscale.com` | Company/domain suffixes bypassing the Mac PAC proxy |
| `--yes` | `false` | Accept explicit/default settings without input; first installation only |
| `--no-install` | `false` | Create configuration/credentials without service registration or sudo |

The wizard asks for the IP, client list, four ports, bypass domains, then final
confirmation (`yes`). Press Enter to keep a shown default. The selected IP must
belong to the target host; wildcards (`0.0.0.0`, `::`), multicast and zone-qualified
addresses are rejected. Ports must be distinct, between `1024` and `65535`, and
available on the host. A CIDR is accepted for clients; a bare IP becomes `/32` or
`/128`. Loopback and the listener's own address are also allowed. Invalid input
fails before files/service changes; correct it and rerun. Empty stdin is not consent.

Setup validates, writes private staging files, generates a management token and
TLS certificate including the selected admin IP, then installs after confirmation.
`--no-install` stops after generation; use `serve --config FILE` to run it.
Linux requests sudo for registration if needed; `--yes` does not bypass sudo.
macOS uses a logged-in user's LaunchAgent and must not be installed with sudo.

New Linux installation creates the dedicated service account, copies the executable
to `/usr/local/lib/rillway/rillway`, creates `/usr/local/bin/rillway`, writes the
unit, runs daemon-reload, enables boot startup and starts the service. Effective
configuration is `/etc/rillway/config.json`, private state `/var/lib/rillway`.
The service has no capabilities and uses filesystem restrictions; only its config
and state are writable. State/config directories are `0700`; credentials/config
are `0600`. The initial per-user staging file is not the installed service's source.

Existing units, binaries, PATH links, config/state directories (including retained
files) cause rejection, even with `--yes`. The privileged installer checks again
before changes. An installation error may leave partial files for diagnosis;
inspect `service status` and the journal, back up retained files, and do not blindly
rerun setup. Setup is not an upgrade/reset operation.

WARP stays disabled and fixed GitHub CDN routes will fail until explicitly
configured or changed. Setup does not change WARP registration/license, host VPN,
Mac proxy, firewall, Docker, or routing. Configure providers in Web UI/TUI afterward.

After installation, verify the displayed certificate fingerprint, open the
HTTPS URL, then read the token locally. For a Linux service:

```sh
sudo cat /var/lib/rillway/admin.token
rillway service status
sudo journalctl -u rillway --no-pager -n 50
```

Keep the token private. A self-signed certificate is not automatically trusted;
verify/trust it or use your own valid certificate. Do not place the token in
command arguments, Git, tickets, or logs.

## `init` and `serve`: local configuration and foreground daemon

```sh
rillway init --config .local/config.json
rillway serve --config .local/config.json
```

Both accept `--config FILE`. `init` creates defaults and private credentials in a
`state/` directory beside the configuration; it refuses an existing file.
`serve` loads the configuration, or initializes it if missing, then starts the four
listeners. Stop with Ctrl+C. They do not install a service or modify WARP accounts.
Use `setup --no-install` to choose LAN settings before credentials are generated.
If editing listeners after init, supply a matching TLS certificate; the daemon
never replaces an existing certificate automatically.

## `tui`: local or remote management

```sh
sudo rillway tui --config /etc/rillway/config.json
rillway tui --url https://192.0.2.20:17892 \
  --token-file ./private/admin.token --ca ./private/admin.crt
# After a successful connection, reuse the remembered service:
rillway tui
```

| Option | Purpose |
| --- | --- |
| `--config FILE` | Explicit local configuration; initialized if absent |
| `--url URL` | Remote HTTPS management URL |
| `--token-file FILE` | Local management token file |
| `--ca PEM` | Trusted certificate/CA; otherwise use system trust |
| `--client-config FILE` | Override the remembered TUI connection file |

A verified connection is remembered in `rillway/client.json` under the OS user configuration directory, with mode `0600`. It contains only the URL and token/certificate file paths, never token contents. Explicit `--url` or `--config` overrides the remembered service. Use `--config FILE` for local installation/start controls. Securely copy the token and public certificate to a remote client, protect the token file, and verify the certificate fingerprint. TLS verification cannot be disabled.

The TUI manages a running service. If it cannot connect, it explains the failure and offers `o` to edit the current URL and file paths. It does not show revision zero or pretend an empty connection list was loaded. Press `?` for instructions; actual HTTP proxy and PAC addresses appear after connecting.

| Key | Action |
| --- | --- |
| `o` | Edit the service connection; Enter connects |
| `?` | How to use Rillway |
| `Tab`/Right, Shift+Tab/Left | Switch Connections, Outbounds & VPNs, Service settings |
| `j`/Down, `k`/Up | Select a row |
| `r` | Refresh, including retrying an unavailable service |
| `a` | Toggle adaptive routing |
| Connections Enter | Create a rule, preselecting the current outbound; `f` changes IP family |
| Outbounds `+` | Add an outbound with suggested values; WARP is the initial type |
| Forms Tab/Up/Down | Select a field; Left/Right changes type or switches; Ctrl+U clears text |
| Forms Enter/Esc | Save or connect / cancel; failures preserve input |
| Outbounds `n`, `c`, `d`, `v` | Register, connect, disconnect, verify |
| Outbounds lowercase `l` | Enter a masked WARP+ license |
| `i`, then Enter | Install a new local service after confirmation |
| `s`, then Enter | Start an installed local service after confirmation |
| `L`/Ctrl+L | Change language; use Ctrl+L while editing text |
| Escape | Cancel a form, or quit outside a form |
| `q`/Ctrl+C | Quit; Ctrl+C also works while editing |

Web UI and TUI share actual defaults: WARP `127.0.0.1:40000` and `warp-cli`; a WireGuard file suggestion; and a Tailscale node name with a dedicated state directory. VPN paths belong to the server. WireGuard and Tailscale start disabled until you provide a configuration or enable and sign in; credentials are not generated. Tailscale stays private. New rules suggest `github.com` and the current default outbound, which you can replace. Switching types or languages preserves edits. Web UI also provides rule/profile editing and safe deletion; built-in `direct` cannot be deleted, and referenced outbounds require an explicit replacement.

## `service`: systemd / LaunchAgent

```sh
sudo rillway service install --config /absolute/new/config.json
rillway service status
sudo rillway service stop
sudo rillway service start
sudo rillway service restart
sudo rillway service uninstall
```

`install`, `start`, `stop`, `restart`, `status`, `uninstall` accept `--config FILE`;
only `install` uses it. `setup` is the recommended first-install workflow.
Linux install/uninstall require root; systemctl may also require privileges for
start/stop/restart. `status` uses systemctl without a pager. Manual stop stays
stopped despite restart-on-failure. Enablement is not proof of a reboot test.

Uninstall disables/stops the service and removes its unit. It **retains** account,
binary/PATH link, configuration, token, TLS and VPN state; no destructive purge
command is provided. These retained paths prevent accidental reinstall.

macOS: omit sudo for all service commands. Label is `io.rillway.daemon`; executable
is `~/Library/Application Support/Rillway/rillway`, plist
`~/Library/LaunchAgents/io.rillway.daemon.plist`. It uses the installation source
config path and GUI login session. `stop` unloads the agent; `start` loads it;
`restart` kickstarts it. Logs are `daemon.log`/`daemon-error.log` in the application
support directory. Uninstall removes only the LaunchAgent registration/plist.

## Existing Linux installation: update and rollback

Do not rerun `setup`/`service install`. Inspect `systemctl cat rillway`, back up the
current binary/effective config/credentials, verify release checksums and architecture.
For a consistent VPN-state backup, stop the service before copying state. The
following updates only the executable; choose an unused backup filename:

```sh
sudo cp -p /usr/local/lib/rillway/rillway /usr/local/lib/rillway/rillway.before-update
sudo install -m 0755 ./rillway-linux-amd64 /usr/local/lib/rillway/rillway.next
sudo mv /usr/local/lib/rillway/rillway.next /usr/local/lib/rillway/rillway
sudo rillway service restart
rillway service status
```

Confirm trusted HTTPS management and actual proxy requests afterward. Configuration,
TLS, token, WARP registration, company VPN and existing unit are preserved. The
same-directory rename avoids partially replacing a running executable. Old config
locations remain valid; a binary update does not migrate paths or revise units.
Rollback by stopping the service, atomically restoring the saved compatible binary,
and restarting with the retained compatible configuration/state. See
[deployment operations](deployment.md) for unit changes and acceptance boundaries.

## `pac` and macOS `client`

```sh
rillway pac --config .local/config.json > proxy.pac
rillway client list
sudo rillway client apply --service "Wi-Fi" \
  --pac-url http://192.0.2.20:17893/proxy.pac --backup "$HOME/rillway-proxy-backup.json"
sudo rillway client restore --backup "$HOME/rillway-proxy-backup.json"
```

`pac --config FILE` exports the configured PAC to stdout without changing system
settings. The daemon also serves it at `/proxy.pac`. Company domains/CIDRs bypass
on the Mac; public traffic uses Ubuntu. PAC has no automatic public DIRECT fallback.
On macOS, open Network, choose the service, open Details and Proxies, then enable
Automatic Proxy Configuration and enter the PAC URL. See [Apple's Mac proxy
settings guide](https://support.apple.com/zh-tw/guide/mac-help/mchlp25912/mac) for
the current interface steps.

`client` is macOS-only. `list` lists network **service names**, e.g. Wi-Fi or USB
Ethernet, not `en0`. `apply` options: `--service` (default Wi-Fi), required
`--pac-url`, `--backup` (default `rillway-proxy-backup.json` in current directory).
Before changes it saves PAC URL/state and manual proxy enable states, then sets
PAC and disables manual proxies. It retains manual servers/ports/credentials.
Existing backup files are refused; failed application attempts restoration.
`restore --backup FILE` restores the snapshot and removes it only on success.
Only this explicit command changes the Mac's selected network service.

## `docker export`: Engine, Build and containers

```sh
rillway docker export --target daemon --proxy-url http://192.0.2.20:17890 > daemon.proxy.json
rillway docker export --target client --proxy-url http://192.0.2.20:17890 \
  --input "$HOME/.docker/config.json" > docker-client.merged.json
rillway docker export --target env --proxy-url http://192.0.2.20:17890 > proxy.env
rillway docker export --target compose --proxy-url http://192.0.2.20:17890 > compose.proxy.yaml
```

| Option | Default / meaning |
| --- | --- |
| `--target daemon|client|env|compose` | `client` |
| `--config FILE` | Derive HTTP URL and default bypass from config |
| `--proxy-url URL` | Explicit Docker-reachable HTTP proxy; can work without config |
| `--no-proxy CSV` | Override domain/IP/CIDR bypass; empty explicitly removes bypass |
| `--input FILE` | Merge existing JSON for daemon/client; source is not modified |

The daemon format covers image pulls/pushes; client config supplies Build arguments
and new container defaults. Environment/Compose formats support container HTTP/HTTPS
clients. Export writes stdout only and does not install settings, restart Docker,
change OrbStack/Desktop, or test reachability. Use a LAN/container-reachable address;
container loopback refers to the container, not the host. Keep company/private
bypass entries. PAC DNS-aware logic is not equivalent to Docker `NO_PROXY`.

Review the output before applying it. **Never redirect onto the same file passed
as `--input`**: the shell truncates it before export reads it. Merges preserve
unrelated settings, including registry authentication; keep these files private.
Inline proxy credentials are rejected. Proxy password setups require a separate
compatible Docker authentication arrangement. Docker Desktop/OrbStack settings
and daemon-driver differences are described in [Docker operations](docker.md).

## `diagnose`: explicit connection/download diagnostics

```sh
sudo rillway diagnose --config /etc/rillway/config.json --outbound direct --family ipv4
sudo rillway diagnose --config /etc/rillway/config.json --outbound warp \
  --download-url https://YOUR_HOST/YOUR_TEST_FILE
```

`--config FILE`; `--outbound ID` defaults to direct; `--family auto|ipv4|ipv6`
defaults to auto. The report is JSON with DNS, address family, connection quality
and observed CDN information. Per-target failures appear in each JSON `error` field; a completed report can exit
`0` even when a target fails. Inspect the report as well as exit status. WARP must
already be configured/connected; avoid concurrently owning the same tsnet state.
`--download-url HTTPS_URL` explicitly fetches at most **4 MiB**, without redirects.
Select a suitable authorized test file. No automatic download test runs on startup.
CDN headers reflect that response, not proof of physical node location; connect
latency is not download throughput.

## `licenses`, `version`, `help`

```sh
rillway licenses > THIRD-PARTY-NOTICES.txt
rillway version
rillway help
rillway setup --help
```

`licenses` prints the full embedded third-party/module/Go/font notices and needs
no config or companion files. `version`/`--version` prints the build version;
development builds currently identify as `0.1.0-dev`. No version command implies
release publication. `help`/`--help`/`-h` shows commands, and command-local help lists
flags without installing services or generating credentials.
