# Security policy

[English](SECURITY.md) · [繁體中文](SECURITY.zh-Hant.md)

Rillway is a proxy for a trusted LAN or VPN. Public source does not make its
management interface or proxy listeners safe to expose to the Internet.
No software or audit can guarantee that credentials will never be compromised.

## Reporting vulnerabilities

Use **Security → Report a vulnerability** on the GitHub repository. Maintainers
must enable private vulnerability reporting before making the repository public.
Do not post an exploit, token, private key, license, real host address, screenshot
of production settings, or diagnostic archive in a public issue. If private
reporting is unavailable, wait for the maintainer to enable it; do not disclose
sensitive details publicly. Include the version, impact and a minimal reproduction
using synthetic addresses and credentials. There is no guaranteed response SLA.

Security fixes target the latest release and `main`. Earlier versions may require
an upgrade; there is no long-term support branch yet.

## Deployment boundaries

- The default listeners bind loopback. For LAN use, select a specific IP and the
  smallest source allowlist. Keep all four listeners behind the host firewall.
  Do not forward them from the Internet or use `0.0.0.0/0` / `::/0` as client ACLs.
- HTTP/SOCKS5 share an admission budget of 256 open client sockets, with at most
  64 per source IP. Management/PAC have an independent budget of 64 sockets,
  16 per source IP. ACL and capacity checks run before parsing or TLS handshakes;
  excess sockets are closed without waiting. Half-closed tunnels retain their
  slot until fully closed. These limits do not cap bandwidth or total process RSS.
- The proxy rejects destinations matching its own bound HTTP/SOCKS5 endpoints,
  including known local addresses for wildcard binds and DNS aliases confirmed
  by the selected outbound's connected peer. No extra host-DNS query is made.
  This is not a general detector for loops involving external upstream proxies.
- Requests to the daemon's bound management/PAC endpoints do not enter traffic
  counters or adaptive measurements. Literal endpoints use a local connection;
  confirmed aliases are excluded after provider resolution. Destination ACLs,
  HTTPS and management authentication still apply. Other explicitly proxied
  services on the same host remain observable. PAC automatically returns DIRECT
  for the configured proxy host, even when user bypass entries are disabled.
- Management uses HTTPS, a generated 256-bit random bearer token, same-origin
  checks on changes, CSP, no-store responses, and secret-safe error messages.
  Trust the verified certificate; do not disable TLS verification.
- After 20 failed authentications from a socket peer in one minute, authentication
  is blocked until that window expires, including correct credentials. HTTP and
  SOCKS5 proxy credentials share a limiter; management has its own limiter.
  Source ports, IPv4-mapped IPv6 and forwarded headers cannot change peer identity.
  Each limiter keeps at most 256 peers and rejects new peers when full until entries
  expire. Restarting the daemon clears this in-memory protection. This is not DDoS
  protection and does not replace source ACLs or a firewall.
- HTTP/SOCKS5 proxy authentication is plaintext on the client-to-proxy connection.
  Use a trusted LAN/VPN. Configure a long random proxy password if authentication
  is needed; HTTPS CONNECT encrypts the destination stream, not proxy credentials.
- The installed Linux daemon runs as the dedicated `rillway` user. Config and state
  directories use `0700`, credentials/config use `0600`. A local administrator or
  process with that service identity can read them. Protect the host and backups.
- Web UI tokens remain in browser memory; TUI tokens are hidden unless explicitly
  revealed. Close the session or sign out on shared machines. Browser extensions,
  malicious local processes and screenshots are outside this protection boundary.
- Never expose provider secrets through observation data. Rillway does not decrypt
  HTTPS or retain payloads. An authenticated administrator can see hostnames and
  infrastructure configuration; treat the management token as full admin access.

## Source and supply chain

See [public release preparation](docs/public-release.md). CI uses hosted disposable
runners, read-only permissions for pull requests, immutable action commits, secret
scanning, Go vulnerability checks and bounded fuzzing. Release publishing has a
separate protected job and uses GitHub's short-lived token/OIDC, with no VM, NAS,
WARP, WireGuard or Tailscale credentials. Never use a home-network/self-hosted
runner for public pull requests.

Checksums detect changed bytes. Provenance attestations identify the repository
and workflow; neither proves that the source is vulnerability-free. Review changes
and keep dependencies, the host OS and the official WARP client updated.

If a credential is ever committed or published, revoke/rotate it first. Removing
Git history cannot erase other people's clones, cached logs or downloaded assets.

The Linux memory controller is a socket-activated root process in the same
binary, separate from the unprivileged Web daemon. Its Unix socket checks peer
UID and service cgroup and has bounded requests/deadlines. It only reads service
status and changes fixed MemoryMax/MemoryHigh properties; it cannot execute
submitted commands or modify other units. Root-owned metadata contains no
credentials. The executable and all parent directories must remain root-owned
and not group/world-writable. Authenticated administrators can change resource
limits, which can still terminate the service under future load; conservative
floors and host capacity checks are safeguards, not an availability guarantee.
