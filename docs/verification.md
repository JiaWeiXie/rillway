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
