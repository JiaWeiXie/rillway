# Agent CLI and operations Skill

[English](agent-cli.md) · [繁體中文](agent-cli.zh-Hant.md)

`rillway agent` provides noninteractive JSON commands in the existing binary,
using the same authenticated management API as Web UI/TUI. It adds no MCP or SDK.

```sh
rillway agent schema
rillway agent status
rillway agent status --url https://192.0.2.20:17892 \
  --token-file /private/path/admin.token --ca /private/path/admin.crt
```

Replace documentation addresses/paths with your own. Local commands discover the
OS config, or accept `--config FILE`. They never initialize missing config or
select a remembered TUI target. Remote management requires an explicit URL and
token file, with normal TLS verification; there is no inline token/insecure flag.

| Command | Effect |
| --- | --- |
| `schema` | Offline discovery: version, commands, flags, output/exit contract |
| `validate --input FILE` | Offline config validation; no VPN/network checks |
| `status` | Effective revision, adaptive switch and outbound states; no auth links/raw details |
| `config` | Complete configuration with file references; contains private infrastructure data |
| `stats` | Existing observations; contains private destination data |
| `plan --input FILE` | Compare candidate/current revision and list changed field names |
| `apply --input FILE --yes` | Apply complete candidate using its current expected revision |
| `outbound --id ID --action connect\|disconnect\|verify --yes` | Explicit provider action; verify generates traffic |
| `restart --yes` | Reload listeners/providers in the daemon; closes active connections |

Input commands also accept `--input -` for one JSON object on stdin, maximum
256 KiB. Extract **`data`** from `agent config` into a private `0600` candidate,
preserving every field and revision; do not submit the full envelope or increment
the revision. Validate, plan, then apply only the authorized change. On conflict,
fetch fresh config and review the intended edit again. Unchanged apply does not
write/increase revision. A plan is not a reservation or provider health check;
it returns `runtime_verified: false`. Listener/security edits remain server-side.
Registration/license/profile-import operations use the existing UI forms.

Network commands accept `--config`, `--url`, `--token-file`, `--ca`, `--timeout`.
Network timeout starts after reading input, defaults to 15s and is at most 2m;
each HTTP request is also capped at 15s. Offline commands reject connection flags.
There are no automatic mutation retries: inspect state after a lost response.
API restart does not execute an updated binary; use the OS service restart for
binary upgrades. `--yes` records command intent, not permission for unrelated work.

Success and failure both emit exactly one JSON object on stdout, without prompts,
colors or progress text. Keys/codes stay stable with `--lang en|zh-Hant`:

```json
{"schema_version":1,"ok":false,"error":{"code":"confirmation_required","message":"This operation requires --yes. Review its effects first.","exit_code":2}}
```

Exit codes: 0 success, 1 rejection/output failure, 2 input/arguments/intent,
3 credentials/access, 4 revision conflict, 5 connection/TLS/deadline failure.
Errors do not echo input values, paths or raw API/provider errors. See the
[detailed contract](../skills/rillway-ops/references/agent-cli.md).

## Install the public Skill

The MIT-licensed [rillway-ops Skill](../skills/rillway-ops/SKILL.md) packages generic
installation/rollback, VPN/routing, PAC/Docker troubleshooting and CLI guidance.
Its English instructions support requests in English or Traditional Chinese;
agents should respond in the requested language. It contains no deployment records,
private account identities or credentials and declares no additional tool dependency.

Copy `skills/rillway-ops` from a reviewed checkout into your agent's Skill discovery
directory. For Codex with this local directory convention:

```sh
mkdir -p "${CODEX_HOME:-$HOME/.codex}/skills"
# Inspect an existing Skill before replacing it.
cp -R skills/rillway-ops "${CODEX_HOME:-$HOME/.codex}/skills/"
```

Alternatively verify release **`rillway-ops.zip`** against `SHA256SUMS`, inspect its
seven allowlisted members and extract into the discovery directory. Use
`$rillway-ops`, e.g. “Check my Docker pull failure; inspect first and plan a targeted
fix.” Your agent's discovery/trust requirements still apply; normal implicit
selection stays enabled. Developers can reproduce the archive with
`mise run skill:package`. Releases checksum/attest the deterministic archive;
symlink/nonregular inputs are rejected. No runtime data or conversation is bundled.

Design references: [CLI Guidelines](https://clig.dev/),
[GitHub CLI JSON output](https://cli.github.com/manual/gh_help_formatting),
[Hugging Face Agent CLI](https://huggingface.co/blog/hf-cli-for-agents),
[Agent Skills format](https://agentskills.io/specification). These inform product
choices; there is no claimed universal “Agent Native” certification.

`config` and `stats` remain private even without secret contents. A management
token grants the same admin access as Web UI/TUI. Keep credentials/logs outside Git
and publish sanitized reports only; scans/provenance cannot guarantee security.
