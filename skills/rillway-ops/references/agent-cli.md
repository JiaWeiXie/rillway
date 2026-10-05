# Agent CLI contract

All `rillway agent` commands are noninteractive, with one JSON object on stdout,
including failures. The envelope has `schema_version: 1`, `ok`, and either `data`
or `error` (`code`, `message`, `exit_code`). Keys/codes are stable; `--lang en` or
`--lang zh-Hant` changes error prose. There are no animations, colors or prompts.
Read `rillway agent schema` for the installed binary's actual command contract.

## Connection and observations

```sh
rillway agent schema
rillway agent status
# Supply operator-owned values; this is a documentation address.
rillway agent status --url https://192.0.2.20:17892 \
  --token-file /private/path/admin.token --ca /private/path/admin.crt --timeout 15s
```

On the server, `--config FILE` overrides OS discovery. `--url` deliberately selects
a remote target; no remembered TUI profile is used. Remote management requires
HTTPS. Verify the certificate fingerprint through a trusted channel before using
`--ca`; never disable certificate verification. Token files should be `0600`.
No inline token flag exists. Network operations have a default 15-second deadline
(maximum 2 minutes); each HTTP request is also limited to 15 seconds. Read complete
stdin before expecting the network deadline to begin.

`status` returns effective revision, adaptive enablement and provider states;
interactive auth links and provider detail text are omitted. `config` returns
configuration with secret file references; `stats` returns connection observations.
**Both can contain private infrastructure/destination data.** Neither is a public
support bundle, and neither reads secret file contents for output.

## Candidate configuration

1. Fetch `rillway agent config` privately; extract the **`data` object**, not the
   full envelope, into a `0600` candidate file. Preserve every field and revision.
2. Edit only the requested values. Do not increment or guess `revision`.
3. Validate locally, then compare against the intended running server:

```sh
rillway agent validate --input /private/path/candidate.json
rillway agent plan --input /private/path/candidate.json --config /private/path/config.json
# Only after the user has authorized this specific change:
rillway agent apply --input /private/path/candidate.json --config /private/path/config.json --yes
```

All three accept `--input -` for a single JSON object on stdin, maximum 256 KiB.
`validate` is offline and checks structure/configuration only. `plan` reads the
server and lists changed top-level field names, without values. It neither writes
nor starts providers, checks file permissions/profile contents, reserves a revision,
or proves reachability. `runtime_verified` is therefore `false`. The server may
still reject `apply`; configuration remains unchanged on validation/persistence
failure. Listener/security changes are rejected here; edit them on the server.

Apply is guarded by the candidate's current revision. On conflict, fetch fresh
configuration, reapply the intended edit and review a new plan. Never replace
the revision alone to force an old snapshot over somebody else's changes.
An unchanged candidate succeeds without writing or increasing the revision.
There are no automatic mutation retries. After an ambiguous failure, re-read
effective state before taking further action.

## Explicit actions and exit codes

```sh
rillway agent outbound --id warp --action verify --yes
rillway agent outbound --id warp --action connect --yes
rillway agent outbound --id warp --action disconnect --yes
rillway agent restart --yes
```

`verify` performs end-to-end network traffic; `connect` may change the official
WARP client's mode and port. The CLI's `--yes` means the agent supplies explicit
intent, not that the user has authorized unrelated actions. Registration, WARP+
license entry, Tailscale browser sign-in and inline WireGuard import remain in
the existing Web UI/TUI. Do not invent agent flags for them. Restart acknowledgement
is not proof of restored availability: reconnect, fetch status and test listeners.

| Exit | Meaning | Next decision |
| --- | --- | --- |
| 0 | Success | Inspect `data`; distinguish acceptance from health |
| 1 | Server rejection/output error | Inspect relevant settings locally/UI |
| 2 | Bad input/options/missing intent | Fix the request; discover schema |
| 3 | Missing credential/access denied | Check private token and source ACL |
| 4 | Revision conflict | Fetch and review again |
| 5 | Connection/TLS/deadline failure | Read state before retrying a write |

Schema is product-specific command discovery, not a claim of compliance with a
universal Agent CLI standard. Existing human CLI commands retain their formats.
