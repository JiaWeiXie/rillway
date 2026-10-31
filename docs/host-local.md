# Host-local access

[English](host-local.md) · [繁體中文](host-local.zh-Hant.md)

Host-local destinations include loopback, unspecified and link-local addresses,
Rillway's startup snapshot of host interface addresses, `localhost` and its
subdomains, and the designated metadata addresses below. The client's source is
its accepted socket peer; forwarded headers never grant access. A local client
means a **loopback peer**, including normalized IPv4-mapped loopback. A host
interface, Docker bridge or overlay source is external for this policy.

| Mode | Ordinary host-local services |
| --- | --- |
| `open` | Allowed, subject to the baseline and own-listener protection |
| `local-clients-only` | Loopback peers allowed; others need an exception |
| `strict` | Every client needs an exception |

New setup writes `local-clients-only`. An omitted or empty mode also means
`local-clients-only`, including immediately after upgrading an old configuration.
Loading a file does not rewrite it or reset unrelated settings. The startup
notice reports the effective mode; it does not claim to detect an upgrade.

## Exceptions and baseline

Merge this object into the full configuration, preserving other settings and the
current revision. The example permits one documentation-only client to reach one
service on the proxy host:

```json
"host_local": {
  "mode": "local-clients-only",
  "allow": [
    { "to": "127.0.0.1:3000", "from": ["192.0.2.30/32"] },
    { "to": "[::1]:8080-8082", "from": ["2001:db8::30/128"] }
  ]
}
```

`to` must be a literal IP and a port or inclusive port range; no hostnames,
subnet destinations or wildcards. IPv4-mapped addresses and IPv6 zones are
normalized for matching. `from` uses source CIDRs; omit it to apply the exception
to all clients. An explicit empty `from` list is invalid. Overlapping exceptions
are a union. There are at most 256 rules and 1,024 source CIDRs in total.
Validation warns about unscoped sources, port ranges and baseline exceptions.
An exception never grants admission through the separate source ACL.

In **every mode**, these destinations require an applicable exception:

- IPv4 link-local `169.254.0.0/16` and IPv6 link-local `fe80::/10`;
- link-local multicast `224.0.0.0/24` and `ff02::/16`;
- metadata endpoints `fd00:ec2::254`, `192.0.0.192` and `100.100.100.200`.

Rillway's own HTTP/SOCKS5, management, PAC and live UDP relay listeners are
always forbidden through the proxy, even with an exception. They retain their
own direct-access authentication and ACLs. Use browser proxy exceptions to reach
the management UI or PAC directly.

## DNS and provider limits

The selected provider still owns DNS; route selection and `Explain` do not
resolve names. Direct connections check every actual numeric socket candidate
**before connect**, including newly resolved aliases and adaptive probes.
Cached direct UDP targets retain their verified numeric destination and check
current policy before every datagram write.

An IP exception does not assume that `localhost` means `127.0.0.1`: IPv4 and IPv6
candidates must each match their own exception. When a remote-resolving provider
cannot verify the IP exception needed for a `localhost` spelling, it fails closed
with a fixed explanation that a verified destination IP is required. Mode/source
permission for ordinary host-local services still applies, but a provider's
stricter private-route restriction always wins; no fallback bypasses it.

For **ordinary remotely resolved names, coverage is literal-only**. Their unknown
resolved address is not inferred from the provider's transport peer. In
particular, WARP's local proxy socket is not the website's destination IP.
`Explain` labels conditional direct names as `pending_direct_resolution`; this
is not a promise of access, successful DNS or connectivity.

## Apply, rollback and upgrade

Use the normal revision-checked configuration apply workflow, or edit the
service's actual configuration file and restart. Editing a file alone does not
hot-apply it. Linux normally uses `/etc/rillway/config.json`; older installations
may use `/var/lib/rillway/config.json`. Check the service's configured path.

The one configuration change back to the previous ordinary host-local behavior
is `"host_local": {"mode": "open"}`; preserve any wanted `allow` rules and all
unrelated settings, then apply normally. The baseline and own-listener
protections remain. To **downgrade to v0.9.1**, remove the entire `host_local`
object first: selecting `open` does not make a new field understood by an old
binary. Keep a private backup of the configuration and state before upgrading.
No upgrade or downgrade procedure should regenerate credentials or provider state.

Policy publication has this contract:

> later checks use the new snapshot, and already-authorized sends may finish.
> Apply is not a synchronous barrier.

Apply does not cut established TCP streams. Subsequent UDP sends are checked
under current policy; a newly forbidden cached target is closed. A send already
authorized before publication can finish afterward. Restart closes active streams
and resets in-memory counters.

An old installation with no mode receives immediate protection after upgrade;
external proxy clients reaching host-local services may be refused. Add a scoped
exception for intended traffic, or make the single mode change to `open` above.
This describes the upgrade behavior, not proof that a particular installation
has been upgraded or verified.

## Offline validation and observations

```sh
rillway agent validate --input candidate.json
```

The existing JSON envelope, validation errors and exit meanings are retained.
`data.host_local` reports `effective_mode`, the protected `baseline`, own-listener
protection and `rollback_field: "host_local.mode"` / `rollback_value: "open"`.
`runtime_verified` stays `false`: validation does not contact providers, read
credentials, inspect live interfaces or report live traffic. Phase 1 adds no
`doctor`, setup flag, mode editor, Web UI card or TUI control.

Authenticated `rillway agent stats` / `GET /api/v1/stats` includes `host_local`:

- `actual_denials`: requests/datagrams actually refused by policy;
- `hypothetical_local_clients_only_denials`: observed decisions that
  `local-clients-only` would refuse **with the same configured exceptions**;
- `external_host_local_by_port`: external-to-host-local breakdown, bounded to
  128 port bins; further distinct ports aggregate into `port_overflow`;
- `recent`: the latest eight actual or hypothetical policy denials, with numeric
  identities when verified. These are operator observations, not public errors.

Actual and hypothetical metrics are independent and can both count one event;
**do not sum them**. In `open`, a hypothetical denial is not an actual refusal.
A matching exception prevents the hypothetical denial too. Each ingress request
or accepted datagram is counted at most once, not once per DNS candidate, byte,
preview or probe. Successful mixed-DNS attempts use the final verified target;
ordinary DNS/transport failures are not actual refusals just because another
candidate was blocked. Unknown remote DNS identities remain unknown. Counters
are bounded, memory-only and reset on restart; existing management/UDP metrics
retain their separate meanings.

HTTP can show a fixed policy error without source/internal destination details.
SOCKS5 may provide only a protocol error; dropped UDP can appear as a timeout.
Do not rely on every client displaying the same human-readable message.

## Residual limits

A forwarder on this host can make an outside client appear as a loopback peer;
use `strict` and scoped exceptions where appropriate. `strict` is not a shared-VM
security boundary: a local process can connect directly without using Rillway.
This policy does not block every container/overlay subnet. The host-interface
destination set is a startup snapshot; restart after relevant address changes.
PAC bypass is a routing convenience, not authorization.
