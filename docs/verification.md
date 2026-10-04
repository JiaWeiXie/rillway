# 驗證紀錄與驗收邊界

此紀錄日期為 2026-10-04，環境是 macOS arm64、Go 1.27.1。測試使用暫存設定、本機 HTTP／TCP listener、測試用出口與可控制時鐘，不使用公司帳號或真實 VPN 授權。

## 整體交付檢查

- `mise run check`：全部 10 個 package 通過，lint 0 issues，race detector 未發現競態，總 statement coverage **59.6%**。較低的區段包含需要真實作業系統或帳號的安裝／VPN 流程，不能用 coverage 當作實機驗收證明。
- `mise run build` 與 `mise run release`：通過；已產生 Linux／macOS 的 amd64、arm64 binary（約 21–22 MiB）、SHA256SUMS、module 清單與第三方授權檔。
- `mise exec -- gopls check cmd/rillway/main.go`：通過。LSP、Go、Lint 的快取均設在 repository 的 `.cache/`。
- 實際啟動編譯後的 daemon 與 TUI：HTTPS 管理登入成功，經 HTTP Proxy 取得 PAC 回應 200，TUI 正確顯示該連線的目的 IP、direct 出口、建連時間與流量；測試程序已停止。
- Web UI 在 Chrome 檢查登入及總覽版面；JavaScript 語法及 API／UI 契約測試通過。
- `mise run test:live`：缺少明確指定的真實設定，正確回報 `NOT VERIFIED / SKIP`。

GitHub Actions 已設定 Linux／macOS 的 `check`、build、fuzz，及 Ubuntu 24.04 的 systemd 驗收工作；尚未推送／在遠端 CI 執行。Ubuntu 26.04 可在新建 VM 上執行以下相同驗收腳本：

```sh
RILLWAY_SERVICE_ACCEPTANCE=1 sh scripts/acceptance-ubuntu.sh ./bin/rillway
```

腳本只接受全新、沒有既有 Rillway 安裝的 Ubuntu 24.04／26.04，會實際安裝並測試服務。這次沒有在目前 Mac 執行該腳本。

## 已完成的核心與 daemon 測試

執行命令：

```sh
mise exec -- go test -race -shuffle=on -cover ./internal/engine ./internal/proxy ./internal/app
mise exec -- golangci-lint run ./internal/engine/... ./internal/proxy/... ./internal/app/...
```

| 範圍 | 結果 | Statement coverage | 主要驗證 |
| --- | --- | --- | --- |
| `internal/engine` | 通過，race detector 未發現競態 | 84.4% | 固定規則優先、網域邊界、CIDR、私網保護、故障門檻、樣本與冷卻、探測預算、設定並行更新、既有連線持續運作、統計與保留上限 |
| `internal/proxy` | 通過，race detector 未發現競態 | 78.6% | 真實 HTTP 轉送、CONNECT 初始緩衝資料與 TCP half-close、SOCKS5 驗證、拒絕 UDP／驗證降級、來源限制、移除代理帳密、WebSocket 雙向 Upgrade |
| `internal/app` | 通過，race detector 未發現競態 | 71.3% | 設定衝突、儲存失敗回復、provider 延後關閉、HTTPS 管理驗證、Proxy／PAC 整合、daemon 關閉取消停滯 HTTP origin |

Coverage 是本次執行數值，關閉時序可能使少量分支在不同執行中略有差異。此數字不代表外部 VPN 或目標 Ubuntu 平台已完成實機驗收。

自適應測試特別確認：同一批候選出口樣本不能重複計為兩輪改善；使用者取消連線不會被記為出口故障；手動固定規則不會被自適應取代。背景探測可被 daemon 關閉取消，不留下等待中的 probe。

HTTP／SOCKS5 測試用真正的本機 socket 傳輸資料。它們沒有修改系統 Proxy、路由、DNS、WARP 模式或現有 Tailscale 服務。

## 有時間上限的 fuzz 測試

```sh
mise exec -- go test ./internal/proxy -run '^$' -fuzz FuzzSOCKSAddress -fuzztime=3s -parallel=2
mise exec -- go test ./internal/engine -run '^$' -fuzz FuzzRuleHostname -fuzztime=3s -parallel=2
```

本次 SOCKS5 位址解析執行 55,239 個輸入，hostname／規則判斷執行 22,017 個輸入，皆通過。一般 `mise run test` 仍會執行保留的 fuzz seed；隨機 fuzz 由上述命令明確啟動。WireGuard 設定解析也有獨立 fuzz target，見 [出口文件](providers.md)。

後續完整 `mise run fuzz`（各 10 秒）亦通過：設定 JSON 159,050 個輸入、SOCKS5 671,349 個輸入。這些是有時間上限的測試結果，並非所有輸入的正確性證明。

完整專案的可重跑入口是 `mise run check`；它會執行整個專案的 lint、race detector、隨機測試順序及 coverage。`mise run release` 另建置 Linux／macOS 的 amd64、arm64 binary；交叉建置不能取代目標平台執行驗收。

## 尚未驗證的外部環境

以下項目明確為 **未驗證**，不能由一般測試通過推論為已完成：

- Ubuntu 24.04／26.04 主機上的實際 systemd 安裝、開機啟動、服務帳號權限及移除。
- macOS 主機上的 LaunchAgent 實際安裝，以及 Wi-Fi／Ethernet Proxy 套用與還原。一般測試只驗證產生內容與模擬的系統命令。
- 真實 WARP+ Unlimited 授權、官方 Local Proxy 帳號相容性及端到端 tunnel。
- 公司 Tailscale 登入、ACL、MagicDNS、subnet route、與 Mac 原有 Tailscale 同時運作。
- 外部 WireGuard 伺服器與實際 VPN 設定。一般測試中的本機 userspace WireGuard 互連不等同外部 provider 驗收。
- HiNet 線路的 GitHub CDN 吞吐量與實際加速效果、Ubuntu 對外 IPv6 連線品質。

真實出口測試使用 `mise run test:live`，必須提供 `RILLWAY_LIVE_CONFIG`。缺少所需帳號或設定時回報 `NOT VERIFIED`／`SKIP`，不將跳過當成成功驗證。設定與限制詳見 [出口設定與驗證](providers.md)。

## 實作邊界

- CIDR 規則只比對客戶端提供的 literal IP；不先用宿主 DNS 解析 hostname 再套用 CIDR。hostname 的解析交給選定出口，避免在選路前洩漏私有名稱。
- WARP 不接受已知私有名稱或位址。具公網能力的 WireGuard 作為預設出口時也不接受私有目的地；明確的固定 WireGuard 規則可連私網，DNS 仍由該 profile 的 tunnel resolver 處理。
- IPv4、IPv6、unknown 的可確認觀察樣本分開呈現。`auto` 的自適應比較完整建連路徑；WARP remote DNS 不回傳真實目的 IP 時保留 unknown，不宣稱正在比較同一個 IP 類型。
- 自適應只比較建連時間與成功率，不依一般流量的下載速率重播請求或自動下載測速檔。
- 固定 VPN 出口不可用時回報錯誤，不轉成直連。規則更新只作用於新連線；主動停止服務或 VPN 則會結束受影響的串流。
- 觀察畫面保存最多 2,048 筆連線與 2,048 個自適應目的地，最久 24 小時；每個目的地／出口保存最近 20 個樣本。總計欄位涵蓋目前保留的連線資料，並非無上限的歷史帳本。
- 不解密 HTTPS，不保留 payload、網址路徑、Cookie 或認證 headers。TCP 計數包含傳輸的應用協定資料，並非解析後的檔案內容大小。
