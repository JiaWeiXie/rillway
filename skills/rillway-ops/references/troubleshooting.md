# Troubleshooting by failure layer

Read effective status/config privately before changing anything. Follow the error
the user actually reported; do not run every probe, reboot services or erase state
as a generic checklist. Use `agent schema` for current command syntax.

## Management connection refused / unusable TUI

`https://127.0.0.1:17892` means this machine, not a remote VM. Distinguish a stopped
local service from a client aimed at the wrong host. Check OS service status and
actual management listener. On remote clients use explicit `--url`, `--token-file`
and trusted `--ca` with `agent status` or `tui`. A valid local config does not prove
that the daemon is running. Agent reads never initialize a missing config.
For access denied, check token source and allowed socket-peer IP; forwarded headers
do not change peer identity. Avoid repeated logins that trigger rate limiting.
For TLS failure, verify hostname/SAN, time and trusted certificate/fingerprint;
do not add insecure TLS flags. Raw logs and auth URLs stay private.

## WARP registration or false health

Separate these checks: official daemon installed/running, CLI available to the
service user, already registered device, supported Local Proxy mode, listener
on the configured address, tunnel connectivity, actual end-to-end trace.
Rillway's Web UI registration checks existing registration instead of deleting it.
Do not run registration-delete/new loops; they can disrupt an existing WARP+
account. A refusal to use proxy mode is not permission to enable whole-host WARP.

`agent outbound --id OUTBOUND_ID --action verify --yes` explicitly generates an
end-to-end request. `connect`/`disconnect` are separate authorized actions.
Account `Unlimited` without a usable listener or successful trace is insufficient.
Use Web UI/TUI for registration and WARP+ license entry; no raw CLI registration
output or license should be copied into a public issue.

## Mac PAC

The field requires a complete URL such as
`http://192.0.2.20:17893/proxy.pac`, not `host:port` alone and not the management
HTTPS URL. PAC's own proxy address is the HTTP listener, normally port 17890.
Fetch PAC from the actual Mac and confirm its rules before applying it. Choose
macOS network **service name** (Wi-Fi/Ethernet), not an `en0` device number.
`client apply` saves a restoration snapshot; do not overwrite an existing snapshot.

Keep company domains and actual subnet routes bypassed so the Mac's existing
Tailscale/DNS handles them. Defaults include local/private/link-local ranges,
Tailscale ranges/MagicDNS and common Docker/Kubernetes local names, but custom
subnets and cluster suffixes require operator input. PAC governs supporting apps,
not all host traffic or VPN firewall rules. An active Tailscale exit node or IP
overlap can still conflict; do not disable it without authorization.

See [Apple proxy settings](https://support.apple.com/guide/mac-help/mchlp25912/mac)
and [Tailscale VPN coexistence](https://tailscale.com/docs/reference/faq/other-vpns).

## Docker pull, build and containers

Determine where the Docker Engine actually runs. A NAS/VM/Desktop/OrbStack engine
does not use the shell's loopback proxy or Mac PAC automatically. Probe from that
engine's host/network first, targeting the server's reachable HTTP proxy address.

| Error/operation | Meaning and next decision |
| --- | --- |
| `no route to host` | Before registry auth: check bridge/VLAN route, VM address, firewall rejection and source ACL; do not repeatedly log in |
| Connection refused | Host reachable but listener/service/bind/port likely wrong; inspect that layer |
| TLS failure | Inspect proxy destination trust and registry certificates; do not disable verification |
| Registry `denied` | If transport now works, check private package visibility, account permission, token expiration and required scopes; not proof of proxy failure |
| Pull/push | Configure Engine proxy, preserving existing daemon JSON; Desktop uses its own proxy UI |
| Build/container | Docker client proxy defaults/build args or explicit container env; existing containers may need recreation |

`rillway docker export --target daemon|client|env|compose` produces output only;
it neither installs settings nor restarts Docker. `--input FILE` merges existing
JSON without modifying it. Exported proxy/no-proxy values need the actual engine's
reachable address. Preserve unrelated Docker settings and limit host changes to
the requested engine. Container DNS/subnet reachability is a separate check.

Do not put registry credentials in a proxy URL. For `docker login --password-stdin`,
the operator supplies the token privately; do not read it into chat/logs. A private
image needs registry permission as well as a working transport. Check token expiry
before modifying a healthy proxy. Never send Docker's credential store to support.

## Memory growth or LAN disruption after broadening the source ACL

`security.allowed_clients` authorizes socket peers; it does not scan the subnet,
create connections or change routes. Broadening it can admit previously blocked
traffic. Do not label memory growth as a confirmed proxy loop without evidence.

If stopping the VM restores the LAN, keep it stopped until isolated recovery is
available. Disconnect its virtual NIC using the hypervisor and use the VM console
to boot, stop Rillway and disable its automatic startup before reconnecting the
NIC. Preserve configuration, VPN state and previous-boot logs privately. If the
console or NIC controls are unavailable, request that access rather than booting
the same configuration directly onto the LAN.

Inspect which process consumed memory, kernel OOM records, accepted source IPs,
connection count/destinations, `proxy_admission_rejections` in `agent stats`
(capacity closures appear to clients as resets), and whether an outbound points
back to Rillway.
Distinguish the NAS/hypervisor VM graph from guest process RSS, service cgroup
accounting and Linux `MemAvailable`. VM RSS can include guest file caches; it is
not the Rillway heap. Historical service memory peaks are useful evidence, but
absence of OOM does not rule out network saturation or host memory pressure.
List recorded boots first: after a recovery boot, `-1` may no longer refer to the
incident. Preserve the incident's actual boot/time range rather than assuming it.
Verify the official WARP mode and VM bridge/IP ownership without changing unrelated
VPNs. Missing persistent journals after a forced power-off mean missing evidence.
Narrow the ACL to individually approved clients; do not replace it with an empty
list or disable authentication. Proxying the host's own outbound traffic back to
Rillway must be corrected before restarting.

Current source bounds HTTP/SOCKS sockets at 256 total/64 per source, with a separate
management/PAC budget of 64 total/16 per source. It also rejects its own proxy
endpoints. Check the installed version before assuming those protections exist.
They do not limit network bandwidth, detect every multi-proxy loop, or provide an
OS memory ceiling. Apply VM/service resource limits appropriate to the machine
before controlled recovery; replacing only the binary does not update a unit.
Resume with one approved client and observe memory and connection counts before
adding more. Do not run bulk traffic tests on the affected LAN.

On a headless server, a failing `warp-taskbar` user unit is the graphical tray,
not `warp-svc`. Inspect its display/session environment and restart count. Do not
stop the official tunnel daemon or change registration to repair a tray failure.
When multiple configuration files exist, inspect the unit's effective `--config`
and use that file's certificate/token references; another config's certificate
can produce repeated `tls: unknown certificate` failures.

## End-to-end and performance evidence

Process active, listening port, VPN connected and real traffic are distinct.
Test the requested protocol from an allowed client; then confirm the observed
outbound/rule and effective revision. Use a controlled destination/test object.
`rillway diagnose --outbound ID` makes explicit DNS/connection probes;
`--download-url HTTPS_URL` additionally downloads at most 4 MiB without redirects.
These are opt-in traffic, not passive inspection. Do not run bulk load tests on a
production proxy under ordinary troubleshooting authorization.

Record checks actually run; do not reuse old deployment/test-account results as
evidence for a different operator's environment. Preserve existing streams where
possible; request an interruption only when the intended fix requires it.
