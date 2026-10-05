# Installation, upgrade and rollback

## Identify the target

Distinguish client from server, OS/architecture, first installation from an
existing service, and foreground development from a background service. Inspect
`rillway version`, `rillway service status` and effective configuration before
changes. On Linux, inspect `systemctl cat rillway` if a custom unit/drop-in may
override discovery. Do not execute unit text to discover paths.

One binary embeds the proxy, Web UI/TUI, fonts, tsnet and userspace WireGuard.
WARP/WARP+ additionally needs the separately installed official Cloudflare daemon.
The server does not need Go, Node or mise to run a published binary. Select the
correct Linux/macOS and amd64/arm64 artifact, verify release checksums and, when
available, provenance against the intended repository/workflow/tag. Do not treat
a checksum by itself as proof that the download source is trustworthy.

## New service

`./rillway setup` is an interactive first-install wizard. Agent automation uses
explicit `--yes` options only when first installation is authorized:

```sh
./rillway setup --yes --listen 192.0.2.20 --allow-client 192.0.2.30 \
  --bypass-domains local,ts.net,tailscale.com,corp.example
```

Replace documentation IPs/domains with the operator's chosen values privately.
Choose a specific local address and a small source allowlist; retain firewall
protection and no Internet port forwarding. Loopback is the safe default. One
bridged virtual NIC/default gateway is normally enough; dedicated NIC passthrough
does not repair the ISP's upstream path. VM sizing needs measured concurrency,
CPU and memory; do not present one operator's load results as a general guarantee.

Linux setup uses sudo for installation and runs the daemon as `rillway`:

| Path | Role |
| --- | --- |
| `/usr/local/lib/rillway/rillway` | Installed binary |
| `/usr/local/bin/rillway` | CLI symlink |
| `/etc/rillway/config.json` | New-install configuration |
| `/var/lib/rillway/` | Private credentials/VPN state |
| `/etc/systemd/system/rillway.service` | OS service unit |

Existing legacy `/var/lib/rillway/config.json` is supported. The CLI follows a
Rillway-generated unit's config first, then existing standard locations. Use
`--config` for custom units. macOS uses the current user's LaunchAgent and
`~/Library/Application Support/Rillway/config.json`, without a root daemon.
Use OS config discovery rather than copying any developer's home directory.

Setup rejects retained installations, prints token **path** and TLS fingerprint,
and does not register/license/connect WARP or change Mac/Docker proxy settings.
Verify the fingerprint, then sign in with the private token. Preserve directory
`0700` and config/credential `0600`. `setup --no-install --config FILE` prepares
an isolated foreground configuration; `serve --config FILE` starts it. Do not
reuse running listeners or Tailscale state.

## Existing service upgrade

Do not use `setup`, `init` or `service install` to upgrade an existing deployment.

1. Resolve actual binary, config, unit and state locations. Verify current health,
   new binary architecture/version/checksum, and config/state compatibility.
2. Stage the verified binary on the same filesystem as its destination. Arrange
   interruption, stop the OS service, and back up binary, effective config, unit
   and required state into a private directory. Stop before copying mutable state.
3. Preserve owner/mode, atomically replace the binary, then start the OS service.
   Keep configuration, keys, licenses and VPN identity. Updating a binary does
   not regenerate a unit; handle documented unit changes separately if needed.
4. Verify the running executable/version, service identity/enabled/active state,
   trusted management TLS, unauthenticated rejection, authenticated status, PAC
   fetch and the requested proxy protocols from an allowed client.
5. If startup/acceptance fails, stop and restore the previous binary and any
   compatible config/state required by a format migration. Restart and repeat
   acceptance. Never initialize new credentials as a rollback shortcut.

`rillway agent restart --yes` reloads listeners/providers **inside** the existing
daemon; it does not execute the newly installed binary. Use the OS service restart
for a binary update. `service uninstall` removes service registration but retains
configuration/state; it is not full data deletion. Keep backup location and
deployment receipt private and disclose only a sanitized summary publicly.

Repository acceptance scripts are for disposable first-install VMs, not live
production upgrades. They may uninstall the service on exit. Do not run live VPN
tests against a state directory already owned by the running daemon.
