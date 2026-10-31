# Changelog

Generated from committed Git history with git-cliff. Regenerate before a release.

## 0.10.0 - 2026-10-31

### Upgrade notes (English)

#### 1. Upgrade behavior changes immediately.

Configurations without `host_local`
immediately use `local-clients-only` after upgrade. A client on another
computer using Rillway to reach a service on the Rillway host is refused
until an exception permits it. Rillway's management and PAC listeners are
also refused through the proxy (#10), even with an exception. Clients on the
VM connecting over loopback, ordinary browsing and existing VPN routes are
unaffected by the default change.

#### 2. Allow the intended client and service.

For example, allow the source
`192.0.2.30/32` to reach `127.0.0.1:3000`. Replace the documentation address
with the source address Rillway actually sees on the incoming connection;
forwarding or address translation can change it. Merge this object into the
complete configuration, keeping its current revision and unrelated settings:

```json
"host_local": {
"mode": "local-clients-only",
"allow": [{ "to": "127.0.0.1:3000", "from": ["192.0.2.30/32"] }]
}
```

Apply the complete configuration through the normal revision-checked apply
workflow, or edit the service's actual configuration file and restart it.
Editing the file alone does not apply it. See [Host-local access](https://github.com/JiaWeiXie/rillway/blob/main/docs/host-local.md).

#### 3. Return to `open` if needed.

Set `host_local.mode` to `open`, preserve any
wanted exceptions and unrelated settings, then apply or restart as above.
Management/PAC listener restrictions and the documented baseline still apply.
`rillway agent validate --input candidate.json` shows the effective mode,
baseline and the `open` rollback path **offline**; it does not verify live
access. Before downgrading to v0.9.1, remove the entire `host_local` object;
selecting `open` alone does not make the old binary accept the new field.

#### 4. Not tested

A separate remote machine; the full protocol matrix from a
non-loopback source; live metadata access; live VPN and provider combinations;
or any real deployment. The disposable released-binary upgrade, explicit
`open` restart, downgrade and retention checks passed on one hosted runner.
Its non-loopback HTTP check is same-runner evidence, not remote-host proof.

### 升級說明（繁體中文）

#### 1. 升級後會立即改變行為

未含 `host_local` 的設定在升級後立即使用
`local-clients-only`。其他電腦的用戶端若透過 Rillway 連線至 Rillway
主機上的服務，會遭到拒絕，直到符合例外規則。透過 Proxy 連線至 Rillway
自己的管理與 PAC 監聽端也會遭到拒絕（#10），即使加入例外也一樣。
VM 上透過 loopback 連線的用戶端、一般網頁瀏覽與既有 VPN 路由，不受這次
預設值變更影響。

#### 2. 允許指定用戶端與服務

例如，允許來源 `192.0.2.30/32` 存取
`127.0.0.1:3000`。請將文件範例位址換成 Rillway 在傳入連線中實際看到的
來源位址；轉送或位址轉換可能改變這個位址。將以下物件合併至完整設定，
保留目前的 revision 與其他設定：

```json
"host_local": {
"mode": "local-clients-only",
"allow": [{ "to": "127.0.0.1:3000", "from": ["192.0.2.30/32"] }]
}
```

使用正常、會檢查 revision 的流程套用完整設定，或修改服務實際使用的
設定檔後重新啟動。只修改檔案不會套用設定。詳見[主機本機服務的存取](https://github.com/JiaWeiXie/rillway/blob/main/docs/host-local.zh-Hant.md)。

#### 3. 需要時切回 `open`

將 `host_local.mode` 設為 `open`，保留想繼續使用的
例外與其他設定，再依上述流程套用或重新啟動。管理／PAC 監聽端限制與文件
所述的 baseline 仍然有效。`rillway agent validate --input candidate.json`
會**離線**顯示生效模式、baseline 與切回 `open` 的方式，不會驗證即時連線。
若要降回 v0.9.1，須先移除整個 `host_local` 物件；只改為 `open`，
不會讓舊程式認得新欄位。

#### 4. 尚未測試

另一台遠端電腦、完整的非 loopback 來源協定矩陣、即時 metadata
存取、實際 VPN 與出口組合，以及任何真實部署。已在單一 hosted runner 上
完成舊版執行檔升級、明確切回 `open` 並重啟、降版及設定／state 保留檢查。
其中的非 loopback HTTP 檢查仍在同一個 runner 上進行，不能當作遠端主機證據。


### Features

- **proxy:** **BREAKING:** add named host-local access modes (#12)

### Fixes

- **proxy:** **BREAKING:** refuse proxy connections to the management and PAC listeners

### Documentation

- **verification:** record v0.9.1 release and VM upgrade

## 0.9.1 - 2026-10-22

### Fixes

- **deps:** patch Tailscale, WireGuard, x/crypto, and x/net; update govulncheck

### Documentation

- **verification:** record v0.9.0 release, VM upgrade, and LAN UDP observations

## 0.9.0 - 2026-10-21

### Features

- **proxy:** add SOCKS5 UDP ASSOCIATE forwarding
- **proxy:** isolate PAC capacity and observe SOCKS5 UDP relay

### Fixes

- **changelog:** keep curated entries when preparing a release
- **web:** avoid repeating product name in SOCKS5 glossary
- **deps:** update Go to 1.27.3 for GO-2026-6809

### Documentation

- **verification:** record v0.8.0 release, VM upgrade, and PAC checks

## 0.8.0 - 2026-10-14

### Features

- **engine:** share adaptive affinity per registrable domain and fail over per dial

### Fixes

- **outbound:** pace WARP SOCKS5 handshakes and retry transient resets

### Performance

- **pac:** bound client DNS bypass decisions

### Documentation

- **verification:** record v0.7.0 deployment
- describe WARP retries, adaptive affinity, and PAC DNS cache

## 0.7.0 - 2026-10-13

### Features

- **config:** add outbound groups and composite rule schema
- **routing:** match source, inbound, port, and rule sets
- **engine:** select and balance outbound groups
- **control:** explain routes and manage outbound groups
- **web:** add accessible light and dark themes
- **web:** add group, rule-set, and route-preview editors
- **web:** add search, bulk actions, keyboard flows, and responsive controls
- **control:** improve management web interactions
- **tui:** display outbound groups

### Fixes

- **config:** allow group replacements for fixed routes
- **web:** invalidate previews only on revision change
- **routing:** preserve legacy rule matching
- address proxy core review findings
- **tui:** bound group rows to viewport

### Refactoring

- **web:** make browser initialization explicit

### Maintenance

- **engine:** cover disabled preview targets
- **web:** cover logout busy cleanup
- **tui:** cover service switch log reset

## 0.6.0 - 2026-10-12

### Features

- **resource-control:** manage WARP service logging
- **control:** add WARP service logging management and migrate resource control
- **web:** add WARP logging and refresh controls
- **tui:** add WARP logging and refresh controls
- **resource-control:** manage WARP service logging

### Fixes

- **tui:** gate status refresh during loading
- **warp:** preserve forced status refresh
- **web:** correct outbound refresh scheduling
- **web:** translate WARP logging status
- **tui:** translate WARP logging status
- **control:** localize WARP logging errors
- **web:** complete resource-control error translations
- **tui:** complete resource control message translations
- **i18n:** cover resource control messages
- **web:** translate unavailable resource control reason
- **resource-control:** roll back failed socket activation
- **platform:** satisfy release lint checks
- **resource-control:** format Linux helper imports

### Performance

- **warp:** reduce status polling overhead

### Refactoring

- **resource-control:** generalize privileged helper

### Documentation

- **verification:** record v0.5.1 release deployment and post-deployment checks

### Maintenance

- **resource-control:** cover migration and acceptance
## 0.5.1 - 2026-10-11

### Fixes

- **proxy:** configure shared connection capacity and expose rejections

### Documentation

- **verification:** record v0.5.0 release and deployment
- **verification:** use Traditional Chinese source terminology

### Maintenance

- **deps:** patch Go and x/net security vulnerabilities
- **release:** prepare v0.5.1 release notes
## 0.5.0 - 2026-10-08

### Features

- **proxy:** add live source access control

### Fixes

- **network:** harden proxy and tunnel reliability

### Documentation

- **verification:** record v0.4.1 release and deployment acceptance
- **release:** prepare v0.5.0 changelog
## 0.4.1 - 2026-10-08

### Fixes

- **proxy:** preserve connection budgets and adaptive routes

### Documentation

- **release:** prepare v0.4.1 changelog
## 0.4.0 - 2026-10-06

### Features

- **service:** add safe memory controls and self-traffic protection

### Fixes

- **ci:** focus dependency digest on direct updates

### Documentation

- **release:** prepare v0.4.0 changelog
## 0.3.1 - 2026-10-05

### Fixes

- **ui:** show server version and hide configuration revisions

### Documentation

- **release:** prepare v0.3.1 changelog
## 0.3.0 - 2026-10-05

### Features

- **agent:** add safe JSON CLI and public operations skill

### Documentation

- **release:** prepare operations skill release notes
## 0.2.2 - 2026-10-05

### Fixes

- **release:** ignore Python test bytecode in clean source checks

### Maintenance

- **release:** prepare v0.2.2 changelog
## 0.2.1 - 2026-10-05

### Fixes

- **ci:** reuse the digest issue with GitHub CLI bot identities

### Maintenance

- **deps:** bump actions/download-artifact from 4.3.0 to 8.0.1
- **deps:** bump actions/checkout from 6.1.0 to 7.0.1
- **deps:** bump jdx/mise-action from 3.6.3 to 5.0.1
- **deps:** bump actions/upload-artifact from 4.6.2 to 7.0.1
- **deps:** bump actions/attest-build-provenance from 3.0.0 to 4.2.2
- **ci:** merge reviewed action updates and report dependencies monthly
- **release:** prepare v0.2.1 changelog
- **release:** refresh v0.2.1 notes after digest verification
## 0.2.0 - 2026-10-05

### Features

- **control:** manage VPN profiles and restart service from the web

### Documentation

- record cleanup of disposable test environments

### Maintenance

- **release:** prepare v0.2.0 changelog
## 0.1.0 - 2026-10-05

### Features

- **ui:** add bilingual interfaces and Rillway branding
- **docker:** export proxy settings for pulls builds and containers
- **install:** add guided standalone binary setup and bilingual CLI docs
- **ui:** guide setup and group destination traffic
- **pac:** add managed bypass presets
- **tui:** reveal web management token
- **security:** prepare public CI and protected release workflows

### Fixes

- **deploy:** protect state directory and recognize WARP proxy mode
- **warp:** preserve existing device on repeat registration
- **outbounds:** protect direct and replace references on deletion
- **ui:** compact sidebar and add bilingual glossary
- **review:** harden routing lifecycle and traffic UI
- **review:** resolve provider lifecycle and UI refresh findings
- **cli:** prefer OS configuration conventions over remembered connections
- **cli:** follow installed service configuration before fallback paths
- **security:** reject runtime credentials before staging checks
- **ci:** freeze tools and reject dirty release source

### Documentation

- **deploy:** record Ubuntu rollout and live WARP verification
- **warp:** record repeat-registration fix and Unlimited validation
- **docker:** record deployed API and bilingual UI validation
- **outbounds:** record deployment and deletion UI validation
- **deployment:** record Ubuntu binary update verification
- record installed CLI configuration acceptance
- record public source and release artifact security checks
- prepare v0.1.0 public release
- **security:** record public repository protections

### Maintenance

- **tooling:** configure git-cliff and commit message hooks
- **security:** remove private deployment details
- **release:** prepare v0.1.0 verified source
- detect tracked source changes during tool setup
- **release:** finalize v0.1.0 changelog

### Other changes

- Initialize Rillway multi-outbound proxy with management interfaces and tests
- Record Ubuntu deployment target and verified prerequisites
- Configure local agent guidance and validation hooks

<!-- generated by git-cliff -->
