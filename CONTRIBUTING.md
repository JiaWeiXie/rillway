# Contributing

Use issues for reproducible bugs and small proposals. Report vulnerabilities
privately as described in [SECURITY.md](SECURITY.md). Replace real infrastructure
names, addresses and credentials with reserved documentation values. Screenshots
must use synthetic data; text secret scanners cannot inspect their visible pixels.

Install mise, then run:

```sh
mise trust
mise install
mise run hooks:install
mise run check
mise run security
```

See [AGENTS.md](AGENTS.md) for architecture and invariants. Use Conventional
Commits. Add meaningful regression tests for behavior changes. Ordinary checks
must not contact private infrastructure, change host VPNs or use paid accounts.
`test:live` is an opt-in maintainer action and never runs in public CI.

Git hooks scan the exact staged snapshot, including documentation, with redacted
output. They do not scan ignored local files or modify the index. Do not suppress
findings with broad allowlists; review and replace the fixture or remove the secret.
Use a GitHub noreply/project email when committing; do not publish a private email.

PRs require passing CI and maintainer review. Changes to workflows, security policy,
authentication, source ACLs and release scripts require special attention. CI does
not deploy to the maintainer's VM/NAS. Dependency updates are proposed weekly by
Dependabot and must be reviewed rather than automatically merged.

## 繁體中文

公開 issue、PR、截圖與測試資料請使用假資料；漏洞依 [安全政策](SECURITY.zh-Hant.md)私下回報。上方指令可安裝共用開發工具、Git hooks，並執行檢查。修改行為時同步補上回歸測試，commit 使用 Conventional Commits 與 GitHub noreply／專案信箱。

一般測試與 CI 不得使用私人帳號、連到正式基礎設施或修改主機 VPN。`test:live` 由維護者明確啟動。不要用廣泛忽略規則隱藏秘密掃描結果。截圖可見內容仍需人工檢查；文字掃描工具無法判斷圖片中的資料。
