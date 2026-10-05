# Configuration and routing

Use Agent CLI plans for complete JSON configuration updates, or Web UI/TUI for
forms, profile import and secret entry. Keep listener/security settings on the
server; changing them requires a restart. Verify actual applied revision after
an update. A saved/accepted configuration is not proof of end-to-end connectivity.

## Outbounds

- `direct`: immutable built-in public outbound. Never delete/disable/rename it.
- `warp`: official daemon Local Proxy, usual address `127.0.0.1:40000` and CLI
  `warp-cli`. It starts disabled. Enabling is separate from registration, connection
  and verification. There is one official client per host; multiple Rillway IDs
  do not create isolated free/paid WARP instances.
- `tailscale`: embedded private node, dedicated state, optional private auth-key
  file or browser sign-in. It is never a public exit-node candidate. Never point
  its state directory at the host's Tailscale or another active tsnet process.
- `wireguard`: userspace profile with private file reference, or UI paste/import.
  Do not publish the profile. Private keys/auth keys stay in private files. Import
  does not grant arbitrary host shell-hook execution.

Choose a unique outbound ID. UI defaults are actual values, not merely placeholders.
Profile paths refer to the **server**. Prefer existing UI import over making users
manually move key files. WARP+ license is entered through the masked UI, never
stored in Rillway configuration or shared support reports. Account `Unlimited`
and a successful trace showing `warp=plus` establish paid account/path evidence;
neither proves performance improvement for an arbitrary destination.

Deleting an outbound referenced by rules, adaptive candidates or the default
requires an explicit replacement. Do not silently remove references or turn fixed
VPN routes into direct routes. Preserve company destinations on their intended
fixed/private path.

## Rules, DNS and observations

Rules match exact domains, suffix boundaries or client-provided literal IP/CIDR.
Fixed matches precede adaptive selection. DNS runs inside the chosen outbound;
never resolve private names through a public resolver as a troubleshooting fallback.
Domain-based WARP SOCKS requests normally require `family: auto`; a SOCKS hostname
request cannot also force a particular destination IP family. Unknown destination
IP is legitimate, especially through WARP; its listener IP is not the website IP.

Initial GitHub CDN routes use WARP while main/API/codeload routes use direct.
Fresh disabled WARP therefore causes its fixed CDN rules to fail until configured
or deliberately reassigned. Do not promise that every GitHub endpoint is proxied.
HTTP CONNECT observations have hostname/IP and traffic rates, not encrypted URL
paths or response contents. PAC bypass never appears in proxy observations.

## Adaptive routing

Enable only on request. Fixed rules remain authoritative; company/private paths
stay fixed. Candidate sets can use direct, WARP and an explicitly public-capable
WireGuard profile, excluding private Tailscale.

Defaults compare recent connection-success samples, using median connection
latency: at least 3 successful samples in 10 minutes; a candidate must improve
by both 25% and 50 ms, with two fresh qualifying observations and a 10-minute
cooldown. Timeout/refused/unreachable failures can permit early failover after
three consecutive eligible failures if an alternative has a recent successful
measurement. It does not replay the failed application request.

Passive transfer rates are display data, not an automatic switching input.
Background probes are bounded TCP connections for recently used public HTTP(S)
destinations, not downloads: default 12/minute, concurrency 2, timeout 4 seconds.
Changes affect new connections; existing downloads retain their route. Learned
history is in memory and resets when restarting or applying configuration.
Inspect configured thresholds rather than assuming these defaults still apply.
