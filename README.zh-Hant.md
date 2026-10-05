# Rillway

[English](README.md) · [繁體中文](README.zh-Hant.md)

[![CI](https://github.com/JiaWeiXie/rillway/actions/workflows/ci.yml/badge.svg)](https://github.com/JiaWeiXie/rillway/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/JiaWeiXie/rillway)](https://github.com/JiaWeiXie/rillway/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

![Rillway — Choose your route.](docs/brand/rillway-cover.png)

Rillway 是以 Go 實作、可觀察的多出口 TCP Proxy。名稱結合 **rill**（小溪）與 **way**（路徑）：讓每條連線選擇適合的出口，並看得到結果。

Mac 保留公司的 Tailscale；公開網站經 PAC 送到 Ubuntu VM，再由 Rillway 選擇直連、WARP 或 WireGuard。

Web UI 與 TUI 會先填好常用出口設定。TUI 按 `o` 選擇服務、`?` 查看說明，切到出口頁按 `+` 新增。驗證成功的服務連線會記住，下次可直接開啟 TUI。服務設定頁按 `t` 可顯示 Web UI 權杖；離開該頁就會自動隱藏。

## 單一執行檔與引導安裝

每個發布平台只需要 **一個獨立執行檔**，內建 Proxy、HTTPS Web UI、TUI、tsnet、WireGuard userspace 引擎、圖片、離線中文字體／Emoji 字體與第三方授權。伺服器不需要 Go、Node、mise 或外部介面素材。**WARP／WARP+ 仍需另外安裝 Cloudflare 官方 client。**

從 [GitHub Releases](https://github.com/JiaWeiXie/rillway/releases/latest) 下載執行檔與 `SHA256SUMS`，或自行編譯。

從 `mise run release` 的產物選擇 `dist/rillway-linux-amd64`、`rillway-linux-arm64`、`rillway-darwin-amd64` 或 `rillway-darwin-arm64`。依 `dist/SHA256SUMS` 核對雜湊，將執行檔複製到目標主機並命名為 `rillway`：

```sh
chmod +x ./rillway
./rillway --lang zh-Hant setup
# 若要英文提示，改用：
./rillway setup
```

選擇其中 **一個** 安裝指令即可。引導流程會詢問指定的本機 IP、允許的來源、四個連接埠與公司略過網域，列出摘要並要求輸入 `yes` 才建立檔案。預設只使用 loopback；區網使用請選擇 VM 已配置的 LAN IP，並允許 Mac 的 IP。Linux 安裝時會要求 sudo，daemon 以獨立的低權限 `rillway` 帳號執行。macOS 不加 sudo，安裝目前使用者的 LaunchAgent。

自動化 **首次安裝** 可明確提供相同設定：

```sh
./rillway --lang zh-Hant setup --yes --listen 192.0.2.20 \
  --allow-client 192.0.2.30 --bypass-domains local,ts.net,tailscale.com,corp.example
```

IP 是操作範例，請改成 VM 與用戶端的實際位址。公司略過網域讓 Mac 的既有 Tailscale 繼續處理公司流量。安裝不會註冊、套用授權或連線 WARP，也不會修改 Mac 網路設定。

新 Linux 安裝位置：

| 路徑 | 用途 |
| --- | --- |
| `/usr/local/lib/rillway/rillway` | 單一執行檔 |
| `/usr/local/bin/rillway` | PATH 符號連結 |
| `/etc/rillway/config.json` | 服務實際使用的設定 |
| `/var/lib/rillway/` | 私有憑證與 VPN state |
| `/etc/systemd/system/rillway.service` | 已啟用並啟動的 systemd unit |

設定／狀態目錄為 `0700`，設定與憑證檔案為 `0600`。引導只顯示 TLS 指紋與 **權杖檔案路徑**，不輸出權杖內容。先核對指紋，開啟顯示的 HTTPS 網址，再於本機讀取私有權杖檔案登入。預設憑證為自簽憑證，請信任核對過的憑證，或提供自己管理的有效憑證。

偵測到既有安裝或保留檔案時會拒絕安裝。更新只替換執行檔並保留設定與狀態，**不要重跑 setup 或 install 當作升級**。舊版 `/var/lib/rillway/config.json` 部署繼續支援，不會自動搬移。

```sh
rillway service status
sudo rillway service restart
sudo rillway tui
# 只準備設定，不要求 sudo，也不安裝服務：
./rillway setup --no-install --config .local/config.json
./rillway serve --config .local/config.json
```

CLI 會自動讀取 OS 預設設定；`--config FILE` 可覆寫。Linux 優先使用 Rillway 安裝程式產生的 `/etc/systemd/system/rillway.service` 所指定的設定，再依序搜尋既有 `/etc/rillway/config.json`、舊 `/var/lib/rillway/config.json`，最後使用使用者設定目錄。已存在的本機設定優先於 TUI 自動記住的遠端連線。

所有指令、選項、預設值、服務操作、升級、Docker 匯出與 Mac PAC 還原，都在 [完整 CLI 操作說明](docs/cli.zh-Hant.md)。

## 功能與限制

- HTTP forward proxy、HTTPS CONNECT、SOCKS5 TCP；來源 CIDR 限制與選用 Proxy 驗證。
- WARP／WARP+ 官方 Local Proxy 管理、嵌入式 Tailscale tsnet、WireGuard userspace netstack。
- 網域、suffix、IP literal／CIDR 分流；固定 VPN 規則失敗不會偷偷改走直連。
- HTTPS Web UI 與 Bubble Tea TUI 共用 `/api/v1`，支援權杖登入、設定版本衝突檢查及原子更新。
- 英文與繁體中文 `zh-Hant` UI／CLI；Web 字體內建，可離線顯示中文與 Emoji。終端字形仍使用終端機的字體。
- Web UI 側欄的「名詞解釋」提供 37 個名詞的中英說明與例子，可搜尋兩種語言，包含「直連出口」與「略過代理」的差別。
- 連線依網域或 `IP:port` 分組，最上層顯示各目的地總流量，可展開個別連線；自動更新可選 1、2、5、10 或 30 秒。顯示速率、累積流量、建連時間、已知目的 IP、出口與命中規則，不解密 HTTPS 路徑或內容。
- 使用者啟用的自適應，依連線成功／逾時與延遲選擇出口，只影響新連線。
- Ubuntu systemd、macOS LaunchAgent、依 macOS network service 套用及還原 PAC。
- 明確啟動的 GitHub 診斷與限量下載比較。
- Docker pull／push、Build、容器 HTTP／HTTPS 代理匯出：daemon、client、環境變數、Compose；可合併既有 JSON，不改來源檔案。

不提供整機 TUN、SOCKS5 UDP、HTTPS 解密或透明 Proxy。內建 `direct` 不可刪除或停用。初始 WARP 停用，其固定 GitHub CDN 規則會失敗，請先設定 WARP 或手動替換規則。公司與私網維持固定路由；流量速率觀察本身不能證明另一個出口更快。

瀏覽器的 HTTP 與 HTTPS Proxy 都填 HTTP listener；SOCKS5 則使用 SOCKS listener，並啟用 Proxy 端 DNS。區網使用請將 loopback 改成 VM 位址。Mac PAC bypass 完全不經過 Rillway，不會顯示在觀察介面。

## 開發

```sh
mise trust
mise install
mise run check
mise run build
mise run dev
# 另一個終端：
./bin/rillway tui --config .local/config.json
```

`dev` 首次執行建立 `.local/config.json` 與私有憑證，管理介面為 `https://127.0.0.1:17892`，會顯示指紋與權杖檔案路徑。TUI 按 `i` 再按 Enter 安裝新服務；既有安裝受保護。安裝前先停止使用相同連接埠的前景 daemon；開發也可持續使用前景 `serve`。

`mise.toml`／`mise.lock` 固定 Go、gopls、golangci-lint 與 git-cliff。LSP 入口為 `mise exec -- gopls`，VS Code 使用 `scripts/gopls`，儲存時格式化並整理 imports。請在找得到 mise 的環境啟動編輯器，例如 `mise exec -- code .`。

| 任務 | 行為 |
| --- | --- |
| `mise run dev` | 本機前景 daemon／Web UI |
| `mise run build` | 建置 `bin/rillway` |
| `mise run fmt` | gofumpt／goimports 修正格式 |
| `mise run lint` | 驗證設定、Lint、格式檢查 |
| `mise run test` | 不需外部帳號的測試 |
| `mise run check` | Lint、race、shuffle、coverage |
| `mise run fuzz` | 限時設定／SOCKS5 fuzz |
| `mise run test:live` | 明確啟動的外部帳號測試 |
| `mise run notices` | 更新內建完整第三方授權 |
| `mise run release` | 四個 Linux／macOS 執行檔與雜湊 |
| `mise run hooks:install` | 啟用 repository Git hooks |
| `mise run hooks:check` | 檢查精確的 staged snapshot |
| `mise run changelog:preview` | 預覽 git-cliff changelog |
| `mise run changelog` | 更新 changelog |

測試保留 CGO 以使用 race detector；發布採 `CGO_ENABLED=0`。PAC 執行測試只在測試時使用 Node，缺少時明確 skip。Git／Agent hooks 直接呼叫 mise 工具。快取與本機秘密資料由 Git ignore 排除。修改套件或字體授權後需更新 notices，release 會在編譯前自動執行。

## 安全與公開發布

Rillway 適用於可信任區網／VPN，管理與 Proxy 連接埠應保持私有。請參考[安全政策](SECURITY.zh-Hant.md)、[貢獻指南](CONTRIBUTING.md)及[公開發布流程](docs/public-release.zh-Hant.md)。

CI 執行 Linux／macOS 檢查、秘密與歷史掃描、Go 漏洞檢查、fuzz 及隔離 Ubuntu 服務驗收。審查完成的 `v*` tags 可建立 Draft GitHub Release，包含四種單一 binary、checksum 與來源證明。流程不需要 VM／VPN 憑證；repository 保護及 `release` environment 必須在公開前另外設定。

公開 push 前執行 `mise run security`。專案授權為 [MIT](LICENSE)；第三方套件與字體維持原授權。

## 文件

- [CLI: English](docs/cli.md) · [CLI：繁體中文](docs/cli.zh-Hant.md)
- [Ubuntu／LAN 部署、升級、Mac PAC](docs/deployment.md)
- [WARP+、Tailscale、WireGuard](docs/providers.md)
- [分流、自適應、觀察資料與 API](docs/architecture.md)
- [驗證與外部環境未驗證範圍](docs/verification.md)
- [Docker、Build、Compose、OrbStack](docs/docker.md)
- [中英介面、中文字體、Emoji](docs/i18n.md)
- [Agent 規範與本機 hooks](docs/agent-workflow.md)
- [Git hooks、git-cliff](docs/changelog.md)
- [Logo 與圖片素材](docs/brand/README.md)
- [第三方元件與授權](THIRD_PARTY.md)

README 與完整 CLI 操作文件提供中英兩個版本；其餘連結的架構及操作文件目前以繁體中文撰寫。
