# Rillway

Rillway 是以 Go 實作的多出口 TCP Proxy。名稱結合 **rill**（小溪）與 **way**（路徑）：讓每條連線選擇適合的出口，並看得到選擇結果。

適用情境是 Mac 保留公司的 Tailscale，公開網站經 PAC 送到 Ubuntu VM，再由 Rillway 選擇直連、WARP 或 WireGuard。核心、Web UI、TUI、Tailscale 與 WireGuard 引擎都編入同一個 binary；WARP 使用另外安裝的官方 client。

## 目前能力

- HTTP forward proxy、HTTPS CONNECT、SOCKS5 TCP；來源 CIDR 限制、選用 Proxy 密碼。
- WARP／WARP+ 官方 Local Proxy 管理、嵌入式 Tailscale `tsnet`、WireGuard userspace netstack。
- 網域、suffix、IP literal／CIDR 分流；固定規則失敗不會偷偷改走直連。
- HTTPS Web UI 與 Bubble Tea TUI 共用 `/api/v1`，具 token 登入、設定版本衝突檢查與原子更新。
- 每秒更新連線、上下載速率、流量、建連時間、已知目的 IP、出口及命中規則。HTTPS 僅知道目的主機，不解密網址路徑。
- 使用者啟用的自適應，依建連延遲及成功／逾時樣本選擇出口。
- Ubuntu systemd、macOS LaunchAgent、Mac network service PAC 套用及還原。
- 明確啟動的 GitHub 診斷與限量下載比較。

不提供整機 TUN、SOCKS5 UDP、HTTPS 解密或透明代理。初始 WARP profile 停用；GitHub CDN 固定規則會在 WARP 尚未啟用時失敗，請完成 WARP 設定或手動修改該規則。

## 開發

```sh
mise trust
mise install
mise run check
mise run build
mise run dev
```

`dev` 首次執行會建立 `.local/config.json` 與私有憑證，之後開啟 `https://127.0.0.1:17892`。伺服器會顯示憑證指紋與 token **檔案路徑**，不輸出 token。瀏覽器使用自簽憑證時，先核對指紋並信任該憑證；也可替換成你管理的有效 TLS 憑證。登入畫面貼上本機 token 檔案的內容。

另一個終端：

```sh
./bin/rillway tui --config .local/config.json
```

TUI 按 `i`、Enter 安裝背景服務；Ubuntu 透過 sudo 顯示權限提示，完成後 daemon 以 `rillway` 帳號執行。Mac 安裝目前使用者的 LaunchAgent。開發期間也可以維持 `serve` 在前景執行。

| 任務 | 用途 |
|---|---|
| `mise run dev` | 本機服務與 Web UI |
| `mise run build` | `bin/rillway` |
| `mise run fmt` | gofumpt 與 goimports 修正格式 |
| `mise run lint` | 驗證設定、Lint 與格式檢查，不改檔 |
| `mise run test` | 不需 VPN 帳號的測試 |
| `mise run check` | Lint、race、shuffle、coverage |
| `mise run fuzz` | 有時間上限的設定與 SOCKS5 fuzz |
| `mise run test:live` | 需要明確提供真實 VPN 設定的測試 |
| `mise run release` | Linux／macOS amd64、arm64 binary |

工具固定為 Go 1.27.1、gopls 0.23.0、golangci-lint 2.14.0，追蹤 `mise.lock`。測試保留 CGO 以支援 race detector，發布獨立使用 `CGO_ENABLED=0`。一般測試使用本機 listener 和 mock；PAC 的 JavaScript 執行測試使用 Node（僅測試，未安裝時會明確 skip；CI runner 已提供），產品不需要 Node。

LSP 共通入口是 `mise exec -- gopls`。VS Code 設定使用 repository 的 `scripts/gopls` 包裝器，啟用格式化及 imports 整理；請讓啟動編輯器的環境可找到 `mise`，例如 `mise exec -- code .`。其他編輯器以相同入口設定 LSP。快取在忽略的 `.cache/`，產物在 `bin/`、`dist/`、`coverage/`。

## 部署與設定

- [Ubuntu VM、LAN、Mac PAC 操作](docs/deployment.md)
- [WARP+、Tailscale、WireGuard](docs/providers.md)
- [分流、自適應、觀察資料與 API](docs/architecture.md)
- [測試與驗收狀態](docs/verification.md)
- [第三方元件](THIRD_PARTY.md)
- [AI Agent 規範與本機 hooks](docs/agent-workflow.md)
- [Git hooks、提交格式與 changelog](docs/changelog.md)

基本瀏覽器 Proxy：HTTP 與 HTTPS Proxy 均填 `127.0.0.1:17890`；SOCKS5 填 `127.0.0.1:17891`。遠端 VM 改填其 LAN IP。使用 SOCKS 時讓瀏覽器透過 Proxy 解析 DNS，才保留網域分流與出口 DNS 語意。

```sh
./bin/rillway init --config .local/config.json
./bin/rillway serve --config .local/config.json
./bin/rillway diagnose --config .local/config.json --outbound direct
# 明確下載最多 4 MiB，URL 由使用者選定；不跟隨 redirect。
./bin/rillway diagnose --config .local/config.json --outbound warp --download-url https://YOUR_HOST/YOUR_TEST_FILE
```

診斷的 CDN header 是該次回應提供的資訊，不能單靠名稱推斷物理節點。建連速度也不等於下載速度，下載比較必須另行啟動。
