# 驗證狀態

這份文件只記錄可重跑的專案驗證，不保存個人帳號、主機名稱、IP、SSH 路徑、憑證位置、服務雜湊或私人基礎設施拓撲。正式環境的驗收紀錄應存放在受限制的營運系統。

## 一般檢查

```sh
mise run check
mise run build
mise run release
mise run fuzz
```

- `check` 執行 lint、race detector、隨機測試順序與 coverage。
- `build` 建置目前平台的單一 binary。
- `release` 交叉建置 Linux／macOS 的 amd64、arm64 單一 binary；交叉建置不等於目標平台執行驗收。
- `fuzz` 對設定與 SOCKS5 等解析器執行有時間上限的 fuzz 測試。

一般測試使用暫存設定、本機 listener、假出口與可控制時鐘，不需要真實 VPN 帳號，不修改主機 Proxy、路由、DNS、防火牆或 VPN。

## 2026-10-05 審查修正驗證

- 最後一輪獨立 Codex Code Review 回報 `No findings.`，前輪五項 finding 均已修復；範圍、修正與外部驗證缺口見 [審查紀錄](review-2026-10-05.md)。UI／TUI 的實際操作驗證由維護端完成，獨立 reviewer 另檢查原始碼與自動測試。
- `mise run check` 通過：Lint 0 issues，所有 package 的 race detector、隨機測試順序及 coverage 測試通過。
- `mise run build` 與 `mise run release` 通過，產生目前平台 binary 與 Linux／macOS 的 amd64、arm64 四種發布執行檔。
- 本輪 parser fuzz 測試通過；後續修正未改動設定或 Proxy 解析器。
- Web UI 使用隔離的本機假管理 API，實際檢查英文／繁體中文，以及 390、820、1440 px 畫面。目的地分組、展開、累計流量與「設定出口」操作可用，頁面沒有橫向溢出；小畫面仍顯示分組累計流量。
- 實際檢查出口預填值、停用出口的操作限制、PAC 開關的可辨識名稱、中文與 Emoji 備註、語言切換及名詞搜尋。Tailscale 登入連結在自動刷新後仍保留鍵盤焦點；此檢查使用假登入 URL，未登入真實帳號。
- TUI 在真實 Terminal 工作階段檢查頁籤切換、英文／繁體中文、出口表單預填值與取消，以及管理權杖預設隱藏。另以回歸測試確認刷新保留目的地／出口選擇，且不覆蓋已開啟編輯器的設定版本。
- Tailscale 的跨程序狀態目錄鎖定、關閉後釋放、初始化失敗後釋放及重新登入狀態皆有不需要帳號的測試。自適應分流另驗證合法的 0 ms 改善門檻。
- Agent 編輯後 hook 實際檢查通過；回歸測試確認大型 embedded 字型被納入內容雜湊，且一般原始碼大小限制仍保留。

以上是本機與模擬出口的驗證，外部帳號及目標平台的實際驗收仍依下節另行進行。

## 已覆蓋的行為

- 分流規則優先序、網域邊界、CIDR、雙棧、私網保護與出口 DNS 隔離。
- HTTP Proxy、HTTPS CONNECT、SOCKS5 TCP、來源限制、驗證、錯誤輸入、雙向傳輸與關閉。
- 自適應樣本、改善門檻、冷卻、探測預算、取消與既有連線維持原出口。
- 設定驗證、revision conflict、原子儲存、管理 API、TLS、權杖驗證與秘密遮罩。
- WARP 模式檢查、重複註冊保護、Tailscale 私網限制與 userspace WireGuard 設定驗證。
- systemd／LaunchAgent 產生內容、PAC 套用／還原、Docker 匯出與安全 JSON 合併。
- 英文／繁體中文 Web UI、TUI、CLI 文案，以及中文與 Emoji 輸入。
- TUI 的 Web UI 管理權杖預設隱藏、明確顯示、離開頁面自動隱藏，以及 client profile 不保存權杖內容。

## 外部環境

### 2026-10-05：既有 Ubuntu VM 的 binary 更新驗收

已在既有 Ubuntu 26.04.1 LTS／x86_64 VM 更新 Linux amd64 binary。來源 commit 為 `36958c0d046a14faf609432f8a0dd20c00444072`；更新後 SHA-256 為 `85f60dde8cb1f60aea497aec3c9dfe98d554cba7c95412c3be4732d75516268e`。先停止 Rillway，再備份有效設定與 state，原子替換 binary，使用既有 unit 重新啟動。

- systemd 為 `active/running/enabled`，執行帳號 `rillway`，更新後 `NRestarts=0`；運行中 executable 的 SHA-256 與新 binary 相同。
- 設定、unit、管理 Token 與 TLS 憑證的雜湊均保持不變；既有設定版本保留。備份目錄 `0700`、state archive `0600`。
- VM 的 HTTP forwarding、HTTPS CONNECT 與 SOCKS5 TCP 實際傳輸通過；Mac 的 HTTPS CONNECT、SOCKS5、明確信任憑證的 Web UI、PAC 與內建中文字型／Emoji 字型可取得，未登入 API 正確回傳 `401`。
- VM 與 NAS 經 Proxy 對 GHCR 的 `GET /v2/` 得到預期的未登入 `401`；觀察紀錄確認 GHCR 使用既有 WARP 出口，GitHub 主站使用 direct。WARP 顯示 `connected`、`Unlimited`、`proxy` 且 listener 可用，未重新註冊或修改 license。
- 此次未驗證首次安裝、重開機、備份還原、Docker 私有映像授權、真實 Tailscale／WireGuard 或下載效能。以下外部環境清單仍保留其未驗證範圍。

私人主機資料、完整操作紀錄與備份位置保存在 repository 之外或已被 Git 忽略的受限資料夾，未加入 Git。

真實出口測試使用 `mise run test:live`，必須明確提供 `RILLWAY_LIVE_CONFIG`。缺少帳號、設定或目標時應回報 `NOT VERIFIED`／`SKIP`，不能把跳過視為成功。

下列項目必須在各自的受控環境重新驗證，repository 不宣稱目前狀態：

- Ubuntu 24.04／26.04 的實際安裝、重開機、更新與還原。
- macOS LaunchAgent，以及指定 network service 的 PAC 套用與還原。
- WARP／WARP+ 帳號、官方 Local Proxy、Tailscale ACL／MagicDNS／subnet routes。
- 外部 WireGuard provider、公司服務與長時間 GitHub CDN 效能。
- Docker Engine、Desktop、OrbStack、獨立 BuildKit 與容器內連線。

操作限制與必要前置條件見 [部署文件](deployment.md)、[VPN 出口](providers.md)及 [Docker 代理](docker.md)。

## 實作邊界

- CIDR 規則只比對客戶端提供的 literal IP；hostname 解析交給選定出口。
- 固定 VPN 出口失敗時不會偷偷改成直連；更新規則只影響新連線。
- 不解密 HTTPS，不保留 payload、網址路徑、Cookie、認證 header 或 VPN 私鑰。
- 觀察資料有容量與保存時間上限；未知的上游 IP 會維持 unknown。
