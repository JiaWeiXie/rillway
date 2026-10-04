# Rillway agent guide

This file is the shared project guidance for coding agents. `CLAUDE.md` imports it; do not duplicate these rules in another agent-specific guide.
The user's current instructions and existing authorization take precedence. Continue authorized work without repeatedly asking for confirmation.

## Start with the current repository

- Read `README.md`, relevant source/tests, and the applicable documents before changing behavior.
- Inspect Git status and the relevant diff. Preserve the user's and other agents' changes; never reset, discard, or overwrite unrelated work.
- Use `rg` / `rg --files` for targeted searches. Coordinate ownership when agents share files, especially `go.mod` and `go.sum`.
- Use RTK for shell commands where installed: `rtk git ...`, or `rtk proxy <command>` for commands without a wrapper. If RTK is absent in another environment, run the equivalent command directly.
- Do not hard-code developer machine paths, model names, SSH identities, credentials, or a past deployment/test result into instructions or implementation.
- Keep commits focused on the current task. Push, publish, deploy, and change live services only within the user's authorized scope; do not infer deployment from an ordinary code edit.

## Structure and contracts

| Location | Responsibility |
| --- | --- |
| `cmd/rillway` | CLI entry point, initialization, TUI, service/client commands, diagnostics |
| `internal/config` | Configuration types, validation, revisions, atomic persistence, safe public errors |
| `internal/app` | Listener lifecycle, runtime configuration application, managed provider retirement |
| `internal/proxy` | HTTP forwarding, CONNECT, SOCKS5 TCP, access control, stream relay |
| `internal/engine` | Routing, adaptive decisions/probes, bounded in-memory observations |
| `internal/outbound` | Direct, official WARP Local Proxy, embedded tsnet, userspace WireGuard |
| `internal/control` | Authenticated `/api/v1` management API and embedded `web/` assets |
| `internal/tui` | Bubble Tea client of the same management API |
| `internal/platform` | systemd/LaunchAgent, TLS credentials, PAC, macOS proxy snapshot/restore |
| `internal/diagnostic` | Explicit GitHub diagnostics and bounded download comparisons |
| `internal/dockerproxy` | Validated Docker daemon/client/env/Compose exports and non-destructive JSON merge |
| `tests/live` | Opt-in tests using existing external VPN configuration |
| `tools/agentcheck`, `scripts/hooks` | Advisory agent checks and Git staged-snapshot validation |
| `scripts`, `.github/workflows` | Shared build, release, acceptance, and CI entry points |

The product is a Go binary with embedded Web UI and TUI. Avoid adding a separate frontend runtime or duplicating backend policy in either UI.
WARP is an external official client; Tailscale and WireGuard run inside the binary. Preserve this separation and check dependency compatibility when upgrading networking libraries.
Product UI, CLI help, TUI text, and known Rillway-owned status/error messages support English (`en`, default) and Traditional Chinese (`zh-Hant`). Keep both catalogs in sync. Preserve user-entered names and unknown upstream diagnostics verbatim; never translate arbitrary substrings of data or errors. Use consistent terms: outbound, routing rule, adaptive routing, and management token. See `docs/i18n.md` for locale selection and font behavior.
Use grapheme-aware display width and truncation for terminal text, including CJK and emoji sequences. Web fonts must be bundled and licensed; do not require CDN access or silently install fonts on the host.
Brand assets and their source prompts are documented in `docs/brand/README.md`. Keep runtime images under the embedded `internal/control/web/brand/` directory; do not add external font or image requests to the management UI.

## Development and checks

`mise.toml` and `mise.lock` define tool versions and shared tasks; do not copy version numbers into additional configuration unnecessarily.
Bootstrap with `mise trust` and `mise install`. Run tools through `mise exec -- ...` or `mise run ...`.

| Task | Purpose |
| --- | --- |
| `mise run fmt` | Apply gofumpt/goimports formatting |
| `mise run lint` | Validate lint configuration and check source/formatting without rewriting |
| `mise run test` | Account-free tests, including local sockets and mocks |
| `mise run check` | Lint plus race detector, randomized order, and coverage |
| `mise run fuzz` | Time-bounded parser fuzzing |
| `mise run build` | Build the local binary |
| `mise run dev` | Start a local foreground daemon using `.local/` configuration |
| `mise run release` | Cross-build release artifacts; this does not deploy them |
| `mise run hooks:install` | Enable the repository-local Git hook, preserving existing hook managers |
| `mise run hooks:check` | Validate the exact staged snapshot without changing the index |
| `mise run changelog:preview` | Preview committed history with git-cliff without rewriting files |
| `mise run changelog` | Regenerate the changelog before a release |
| `mise run changelog:check` | Validate changelog configuration against the current Git history |
| `mise run test:live` | Explicit external-account validation; see below |

- LSP entry point: `mise exec -- gopls`; editor wrapper: `scripts/gopls`.
- Keep race-test CGO requirements separate from release build settings. Caches and generated outputs belong in ignored directories.
- Add meaningful regression tests with behavior changes. Prefer local listeners, fake providers, command runners, and controllable clocks over external accounts or sleeps.
- Run focused checks while editing, then `mise run check` for the completed change. Repeat only when new changes or failures justify it.
- Scope formatting to relevant files when the working tree contains unrelated edits. Do not rewrite the user's work merely to pass a broad formatter.
- Hook tasks and their implementations are defined in `mise.toml` and repository scripts; inspect the current definitions instead of inventing a parallel validation pipeline.
- Codex/Claude post-edit hooks and Git pre-commit hooks share the mise environment, lint configuration, and repository scripts. Hooks must not deploy, alter VPNs, run live acceptance, or download speed-test data.
- Distinguish checks actually run, checks skipped, and checks blocked. Coverage and cross-compilation do not prove target-machine or paid-account behavior.
- New commit messages follow Conventional Commits: `type(scope): description`, with optional scope and `!` for breaking changes. Use the same format for merges and reverts. The `commit-msg` hook uses git-cliff's parser; do not bypass it to submit a malformed message.
- Keep legacy history intact. `cliff.toml` retains non-conventional historical commits. Generate release notes explicitly; hooks do not stage or rewrite them. See `docs/changelog.md` for release preparation and the shared mise tasks.

## Routing, privacy, and lifecycle invariants

- The built-in `direct` outbound must remain enabled, public, and of type `direct`. Never delete, rename, disable, or repurpose it. Deleting another outbound preserves rules and requires an explicit replacement for every reference; do not silently drop fixed VPN rules or use direct.
- Fixed routes take precedence. A failed fixed VPN route must never silently become a direct connection.
- Select the outbound before resolving a hostname. CIDR rules match client-provided literal IPs; do not add host-DNS lookups merely to match CIDRs.
- Keep resolution inside the selected provider. Private-name failures must not fall back to public DNS, and caches must not leak answers across providers.
- Preserve private/company bypass rules. Tailscale has no public exit-node support here; prevent tsnet's host-network fallback for unowned destinations.
- WARP uses official Local Proxy mode. Never turn a rejected proxy-mode request into a whole-device tunnel or an unofficial registration flow.
- Respect requested TCP/IP family or return an explicit unsupported error. Unknown upstream destination IPs remain unknown; a proxy listener address is not a website IP.
- Adaptive decisions affect new connections only. Do not replay application requests, interrupt existing streams, or turn throughput observations into automatic download tests.
- Keep probe budgets, cancellation, cooldowns, sample freshness, and memory-retention limits tested. Idle streams are not evidence of network failure.
- Runtime updates validate and persist before publication. Retire old providers only after their existing streams close; preserve half-close and cancellation behavior through wrappers.
- Listener/security changes and conflicting use of an active Tailscale state directory require the documented restart path, not a second owner of the same state.
- Do not intercept HTTPS or retain payloads, private keys, license keys, cookies, or authorization headers in observations/logs.
- Management API authentication, source restrictions, TLS verification, revision conflicts, and secret masking are part of the contract.
- Only deliberately sanitized `config.PublicError` messages may be shown to management clients. Never mark raw CLI output or credential-bearing errors as public.

## External validation and deployment

- `test:live` requires an explicit `RILLWAY_LIVE_CONFIG`; missing prerequisites must report `NOT VERIFIED` / `SKIP`, not successful validation.
- Consult `docs/providers.md` for optional target/outbound selection, existing tsnet state requirements, WARP+ Unlimited checks, and provider limitations.
- Ordinary tests must not register/license/connect WARP, change host VPN/DNS/proxy settings, install services, or use private company accounts.
- Treat live tests, service acceptance, and download diagnostics as separate, explicitly authorized actions. Do not run them automatically after edits or from hooks.
- Primary deployment targets are Ubuntu 24.04/26.04; macOS supports the documented local client and daemon workflows.
- Read `docs/deployment-target.md` for the intended host and verify changeable facts before deployment. Do not copy machine-specific identities or keys into this guide.
- Follow `docs/deployment.md` for installation and rollback. Preserve existing machine networking and unrelated services within the user's requested scope.
- Update `docs/verification.md` with actual evidence when verification changes; never infer live success from mocks, old records, or a green build.

## Current library documentation

When working on library/framework/SDK/API/CLI/cloud-service syntax, configuration, setup, migration, or library-specific debugging, use the Context7 CLI even for familiar tools.
Do not use it for general programming, business-logic debugging, code review, or unrelated refactoring.

1. Resolve first: `npx ctx7@latest library <official-name> "<specific concept>"`.
2. Select the relevant official/reputable match, then fetch: `npx ctx7@latest docs <library-id> "<specific concept>"`.
3. Use versioned IDs when needed. Maximum three Context7 commands per question; never include secrets in a query.

Run Context7 outside a restrictive sandbox when supported. For DNS/network failures, rerun outside it rather than repeating inside it. On quota errors, report the limitation and suggest Context7 login or `CONTEXT7_API_KEY`; do not silently substitute remembered API behavior.
An explicit `/org/project` ID from the user can skip resolution. Prefer fetched documentation and current official source over assumptions; report limitations when the expected source is unavailable.

## Documentation map

`README.md` is the entry point; `docs/architecture.md` defines routing/API behavior; `docs/providers.md` covers VPN setup and limitations.
`docs/deployment.md` describes operations, `docs/deployment-target.md` records the intended environment, and `docs/verification.md` records dated evidence.
`docs/agent-workflow.md` describes local agent hooks, Git hook installation, and agent-side trust requirements.
`docs/changelog.md` describes commit message conventions and git-cliff release notes.
`docs/docker.md` distinguishes Docker Engine, client/build/container, Desktop, and OrbStack proxy settings. Exports must not mutate Docker settings, leak credentials, or imply container DNS/VPN access was verified.
Keep examples free of real credentials and use `THIRD_PARTY.md` plus release scripts for dependency/license information.
