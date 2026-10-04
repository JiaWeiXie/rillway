# 驗證紀錄與驗收邊界

此紀錄日期為 2026-10-04。開發檢查環境是 macOS arm64、Go 1.27.1；一般測試使用暫存設定、本機 HTTP／TCP listener、測試用出口與可控制時鐘，不使用公司帳號。另已在 NAS 的 Ubuntu 26.04.1 LTS x86_64 VM 完成正式部署及 WARP／WARP+ 實測，結果與付費／公司帳號的未驗證範圍分開記錄。

## 整體交付檢查

- 最新 `mise run check`：全部 15 個 package 通過，包含 Docker 匯出／合併及 API、WARP 模式解析、systemd 私密目錄、i18n、agent helper、Git hooks 與 git-cliff 整合測試；lint 0 issues，race detector 未發現競態，總 statement coverage **67.3%**。較低的區段包含需要真實作業系統或帳號的安裝／VPN 流程，不能用 coverage 當作實機驗收證明。
- `mise run build` 與 `mise run release`：通過；已產生 Linux／macOS 的 amd64、arm64 binary（含 Logo 與完整中文字體／Emoji 字體，約 44.3–46.0 MiB）、SHA256SUMS、module 清單與第三方授權檔。兩份字體 OFL 授權已逐位元比對 release 內的副本。
- `mise exec -- gopls check cmd/rillway/main.go`：通過。LSP、Go、Lint 的快取均設在 repository 的 `.cache/`。
- 實際啟動編譯後的 daemon 與 TUI：HTTPS 管理登入成功，經 HTTP Proxy 取得 PAC 回應 200，TUI 正確顯示該連線的目的 IP、direct 出口、建連時間與流量；測試程序已停止。
- Web UI 在 Chrome 實測英文與繁中登入、總覽、規則及設定；桌面與手機版均完成互動驗證，詳見下方。
- `mise run test:live` 未指定設定時仍正確回報 `NOT VERIFIED / SKIP`。另將同一套 opt-in tests 交叉編譯後，在 Ubuntu VM 以實際設定與低權限帳號執行：首次 direct、免費 WARP 通過，`warp-plus` 明確 SKIP；使用者套用授權後，`warp-plus` 亦通過，見下方後續驗證。

GitHub Actions 已設定 Linux／macOS 的 `check`、build、fuzz，及 Ubuntu 24.04 的 systemd 驗收工作；尚未推送／在遠端 CI 執行。Ubuntu 26.04 正式部署已另做實機驗收。以下腳本限可拋棄的新建 VM，結束時會卸載 unit，不能對目前正式部署使用：

```sh
RILLWAY_SERVICE_ACCEPTANCE=1 sh scripts/acceptance-ubuntu.sh ./bin/rillway
```

腳本只接受全新、沒有既有 Rillway 安裝的 Ubuntu 24.04／26.04，會實際安裝並測試服務。這次沒有在目前 Mac 執行該腳本。

## NAS Ubuntu 26.04 正式部署與 WARP 實測

目標為 `192.0.2.21`，2026-10-04 實測 Ubuntu 26.04.1 LTS、x86_64、4 vCPU、5,408 MiB RAM。完整操作位置見 [部署目標](deployment-target.md)。

- `rillway.service` enabled、active/running，執行帳號 `rillway`，最後檢查 `NRestarts=0`。手動停止後等待 6 秒，超過 `RestartSec=5` 仍保持停止，再啟動並通過連線驗收。沒有重開 VM，因此未將 enabled 狀態當成開機實測成功。
- HTTP／SOCKS5／HTTPS 管理／PAC 分別只監聽 `192.0.2.21:17890–17893`。來源 ACL 限 Mac `192.0.2.70/32`、VM `.21/32` 與 loopback；未啟用整個 LAN 範圍或公開入口。
- 正式安裝發現 systemd 預設會把 state directory 改為 `0755`，已加上 `StateDirectoryMode=0700` 與回歸測試。實機目錄為 `0700`，config、token、TLS cert/key 為 `0600`。
- TLS SAN 包含 VM IP，使用專案私密副本中的公開 cert 嚴格驗證 HTTPS。未登入 API 回傳 `401`；使用 token 取得設定成功。正式設定 revision 為 `2`。
- 從 Mac 經 HTTP 與 SOCKS5 取得 VM PAC，回應逐位元相同；HTTPS CONNECT 透過受驗證 TLS 取得管理首頁成功。更新正式 binary 後三種協定再次通過。
- Binary 以備份加原子替換更新，沒有重跑安裝器。設定及憑證的 SHA-256 前後相同，state 權限保持不變。實作修正 commit 為 `02fb3ab`；首次部署 artifact SHA-256 為 `e514fcdea9a6e86294942cde7b3259d83fcc15c87e287c2856eed2a1424e8281`。VM 也保留對應第三方授權與 module 清單。
- 官方 `cloudflare-warp CURRENT_VERSION` 從 Cloudflare 的 Ubuntu `resolute` APT 來源安裝。透過 Rillway API 完成免費 consumer 註冊、Local Proxy 連線與端到端驗證。實際模式為 MASQUE／`WarpProxy on port 40000`，listener 僅在 `127.0.0.1:40000`；Cloudflare trace 為 `warp=on`、`colo=EXAMPLE`。API 現在正確顯示 `mode=proxy`、`state=connected`、`listener=true` 與驗證時間。
- 手動停止 WARP，等待 6 秒後狀態仍為 stopped；`raw.githubusercontent.com` 的固定 WARP 規則回傳 CONNECT `502`，沒有轉成直連，同時 `github.com` 的固定直連規則仍回傳 `200`。重新連線後端到端驗證通過。
- 在 VM 以 `rillway` 帳號執行交叉編譯的 `tests/live`：direct 與 WARP 皆完成真實 GitHub TCP 連線，WARP 另通過 Cloudflare trace；`warp-plus` 因沒有確認的 Unlimited 訂閱而明確 SKIP。沒有提供／套用任何付費授權碼。
- 實際代理觀察顯示 `github.com` 命中 `github-origin`／direct，目的 IP 可確認；`raw.githubusercontent.com` 命中 `github-cdn`／warp，目的 IP 保持 unknown，沒有把 SOCKS listener 當作網站 IP。
- VM 預設路由仍為 `192.0.2.1` 經 `vm-interface`。沒有套用 Mac PAC、修改 Mac 系統 Proxy 或操作 Mac 公司 Tailscale；公司服務的實際共存連線尚待指定目標驗證。

### 重複註冊修正與 WARP+ 後續驗證

同日使用者回報按「註冊」時出現 `WARP device registration failed`。唯讀確認官方 daemon 與 Rillway 都正常運作，裝置已有註冊、帳號已是 `Unlimited`，模式仍為 Local Proxy。原實作無條件執行 `registration new`，因此重複按鈕操作會被官方 client 拒絕。

- 修正為先執行 `registration show`；已有裝置時沿用並回傳成功。新註冊失敗後再確認一次，處理其他程序同時完成註冊的情況，不清除裝置或重新套用授權。
- Web UI 的「註冊」在已知註冊狀態時顯示 `Registered`／「已註冊」並停用。測試包含 Free／Unlimited 重複註冊、首次註冊、外部程序競爭、失敗遮罩與兩種 UI 語言。
- 完整 `mise run check` 通過，14 個 package、lint 0 issues、race 無競態、coverage 66.3%；四平台 release 建置成功。修正 commit 為 `576f820`。
- 新版已原子更新至 NAS，artifact SHA-256 為 `c80bca8ca166f556f9ebfa310115cce1cf6c9ebefa592d6c11a6ec90937fa045`。有效設定、TLS 憑證與管理 token 的 hash 前後相同；官方註冊輸出的 fingerprint 在更新及重複操作前後相同，未輸出或保存原始註冊內容。
- 真實 API 連續兩次 `register` 均回傳 `200`／`ok=true`。狀態仍為 `Unlimited`、`connected`、`proxy`、listener 可達；端到端驗證成功。
- 在 VM 以低權限 `rillway` 帳號執行 `tests/live`，WARP 與 `warp-plus` 子測試都 PASS。付費帳號狀態與透過代理的 Cloudflare trace 均已驗證；沒有再次下載測速或把先前免費測試的速率當成付費效果。
- 更新後的隔離 Chrome 實測英文／繁中「已註冊」按鈕都已停用，Unlimited 仍顯示，瀏覽器錯誤為 0；僅 GET／HEAD，未修改設定或 VPN。截圖為 `.local/servers/example/qa/registration-fixed-zh-Hant.png`。

### 限量 GitHub CDN 下載比較

使用相同固定 revision 的 [Noto Sans TC 原始檔](https://raw.githubusercontent.com/google/fonts/9710da1eacb3be272583c3224dcb70f9da6eadbb/ofl/notosanstc/NotoSansTC%5Bwght%5D.ttf)，從 Ubuntu VM 明確執行 `diagnose --download-url`。順序為 direct、WARP、WARP、direct、direct、WARP；每次最多 4 MiB、15 秒逾時，沒有跟隨重新導向。此測試不啟用自動下載測速，也不改變分流規則。

| 路徑 | 三次速率，MiB/s | 完成情形 | 完整樣本中位數 |
| --- | --- | --- | --- |
| direct | 0.227、0.318、0.281 | 第一次在 15 秒傳輸 3,571,264 bytes 後中止；另兩次完成 4 MiB | 0.299 MiB/s，2 個完整樣本 |
| WARP | 3.537、11.279、10.601 | 三次均完成 4 MiB | 10.601 MiB/s，3 個完整樣本 |

完整樣本中位數約相差 35 倍，但樣本少、cache 狀態不同，不能外推成所有網站或長期保證。`X-Served-By` 在 direct 樣本含 `SIN`、WARP 含 `NRT`；這是收到的 CDN header，不等於 traceroute 或物理路徑證明。WARP 的 `colo=EXAMPLE` 指 Cloudflare trace 的節點，與 GitHub CDN 的 header 分開解讀。

六個 GitHub 網域的 HEAD 診斷亦完成。`api.github.com` 直連為 `200`，強制 WARP 測得 `403`，因此沒有把 GitHub origin/API 的預設固定直連改為 WARP。回傳 `301`／`302`／資產根目錄 `404` 可證明 TLS／HTTP 可達，不能當成檔案下載成功。

### 部署後瀏覽器驗收

隔離 Chrome profile 連上 VM HTTPS 管理介面，以英文／繁中、1440 × 1000／390 × 844 驗證登入與雙向語言切換。四個頁面共 32 次寬度檢查，均無頁面水平溢出；Noto Sans TC 與 Noto Color Emoji 字體都從同一服務取得 `200` 並載入。瀏覽器錯誤、失敗請求、外部請求均為 0，驗收沒有發送修改設定／VPN 的請求。

瀏覽器的隔離 profile 使用自簽憑證例外；憑證與 VM IP 的身分檢查另由嚴格信任指定 cert 的 TLS 請求完成，沒有修改 Mac 系統信任庫。截圖保留於 Git 忽略的 `.local/servers/example/qa/`，不含管理 token。

更新後亦從 Mac 啟動正式 binary 的遠端 TUI，使用 `--token-file` 與 `--ca` 連線 VM。繁中介面顯示設定 revision `2`、真實代理觀察資料，以及 WARP `已連線`、版本 `CURRENT_VERSION`、模式 `proxy`、listener `true`；正常離開，未更改設定。

## Docker 代理匯出與 NAS 實測

2026-10-04 新增 CLI 與中英 Web UI 的 Engine、client、env、Compose 四種匯出。一般測試包含 URL／bypass 驗證、malformed JSON、合併保留認證與大整數、冪等性、空 bypass、秘密遮罩、API 認證／同源限制及設定 revision 不變。`internal/dockerproxy` statement coverage 為 **97.7%**，完整 check 的 15 個 package、lint、race 均通過。

VM 原先沒有 Docker。為實測從官方來源取得 Docker **29.4.0** 靜態 binary，啟動只監聽私有 Unix socket 的暫存 daemon，使用獨立 data／exec root、vfs、無 bridge、無 iptables／IP forwarding 修改。Mac 透過 SSH Unix socket forwarding 操作；未修改 Mac 現有 OrbStack context 或設定。

- daemon 使用實際匯出的 HTTP／HTTPS Proxy，兩者均為 `http://192.0.2.21:17890`。Docker Hub pull `curlimages/curl:8.14.1` 成功，digest 為 `sha256:9a1ed35addb45476afa911696297f8e115993df459278ed036182dd2cd22b67b`。Rillway 觀察到 `registry-1.docker.io`、`auth.docker.io` 與 `production.cloudfront.docker.com` 的真實傳輸。
- client 使用實際匯出的 JSON。新容器與 BuildKit 預設 `docker` driver 的 `RUN` 均透過 Rillway 成功取得 `https://example.com`；建置步驟另確認自動注入的 `HTTPS_PROXY` 正確。建置後 image environment 沒有 Proxy URL，沒有用 `ENV` 固化代理。
- Compose 匯出以 `docker compose config --quiet` 驗證成功。
- 再於 VM 的暫存 `registry:2` 驗證 image push。只在 QA daemon 清空 NO_PROXY 並允許該測試 registry 的 HTTP，Rillway 觀察到目的 `192.0.2.21:25000` 的上傳，單筆含約 3.8 MB／5.8 MB layer 資料，push 成功。沒有上傳至公共或使用者私人 registry；公共認證 push 尚未驗證。
- 第一次容器測試對固定 WARP 的 GitHub CDN 收到 502；唯讀確認官方 WARP 當時為 `Disconnected / Settings Changed`。沒有自行重新連線或將固定規則轉為 direct，後續 Docker 功能驗收使用 direct 公開目標。這次 Docker 驗收不代表 WARP+ 當時仍已連線。
- 暫存 registry、Docker daemon、子程序、QA mount、Unix socket forwarding 及 VM 測試資料均已清除，正式 Rillway 保持 active。沒有安裝 Docker systemd unit、修改 Mac 系統 Proxy、OrbStack 或公司 Tailscale。

此實測使用 host network 以避免改動 VM bridge／防火牆；一般 bridge、Docker Desktop、OrbStack 實際代理套用、獨立／遠端 BuildKit 與公司 DNS／Tailscale 連線仍未驗證。設定方法與範圍見 [Docker 文件](docker.md)。

## 中英介面、中文字體、Emoji 與品牌素材

- 實際啟動編譯後的 HTTPS daemon，在隔離 Chrome profile 測試 1440 × 1000 與 390 × 844。瀏覽器自身語言為 `zh-TW`，首次開啟仍遵守產品預設英文；選擇繁中後重新整理保留偏好。
- 登入前切換語言保留輸入；登入後切換保留頁面、規則識別碼與未儲存的 PAC 表單及進階 JSON 內容。使用 `公司服務 🚀 👨‍👩‍👧‍👦 🇹🇼 é` 驗證中文、家庭 Emoji、國旗及組合字原樣保留，並目視確認顯示。
- 以真正的 loopback HTTP origin 經 HTTP Proxy 傳輸 8 KiB，確認連線出現在管理畫面；建立與刪除規則成功，中英按鈕及對應操作均驗證。
- Noto Sans TC 與 Noto Color Emoji 兩個內建 font-face 均成功載入。全程沒有外部請求、失敗請求、瀏覽器錯誤或警告；權杖只保存在 `sessionStorage`，沒有寫入 `localStorage`。
- 桌面與手機的登入／連線／規則對話框均無頁面水平溢出。修正了隱藏的可存取文字在寬表格內造成溢出的定位，沒有以隱藏整頁水平捲動掩蓋問題。
- CLI 實際執行 `--lang zh-Hant help`；TUI 連上本機測試 daemon，以中文顯示真實觀察資料，再按 `L` 切回英文。測試終端正常結束。
- 單元與本機 HTTP 測試包含語系權重、catalog parity、來源訊息遮罩、錯誤切換語言、資料保留、物件原型名稱 fallback，以及中文字寬、Emoji 字素截斷與授權碼中的大寫 `L`。locale parser 另做有時間上限的 fuzz，141,358 個輸入通過。
- 完整 race／shuffle 檢查中，i18n package 的 statement coverage 為 **97.1%**，control 為 **91.1%**，TUI 為 **63.7%**。
- Logo、登入插圖、封面與字體均已保存於 repository；透明 Logo 的 alpha channel、圖片尺寸與 HTTP 類型已驗證。來源與授權見 [品牌素材](brand/README.md) 及 [字體清單](brand/fonts.md)。

預覽：[繁中登入](screenshots/login-zh-Hant.png)、[繁中桌面](screenshots/web-zh-Hant.png)、[英文桌面](screenshots/web-en.png)、[繁中手機](screenshots/web-zh-Hant-mobile.png)。
這次瀏覽器實測限 macOS Chrome；Safari、Ubuntu 瀏覽器與不同終端字體的實際渲染尚未驗證。

## 本機 AI Agent hooks

- `AGENTS.md`、Claude 共用入口、Codex／Claude `PostToolUse` 設定已建立；JSON 與 shell 語法檢查通過，mise 可載入新增的 hook tasks。
- 編輯後 wrapper 實際回傳「lint 與相關 Go 測試通過」的標準 JSON；同一棵來源樹再次執行時命中快取，不重跑檢查。helper 的 statement coverage 為 **84.4%**。
- 暫存副本測試涵蓋部分暫存、alternate index、異常檔名、私密路徑、shell／embedded assets 變更、檢查失敗及暫存清理；installer 測試確認保留既有 hook manager 與 hook 內容。
- wrapper 測試涵蓋 Go 環境清理、輸入原樣保留、不執行輸入命令、工具缺失／啟動失敗時的 JSON 提示、RTK 有無兩種環境。helper 測試確認檢查期間改檔不會快取到錯誤版本。
- 此 checkout 已執行 `mise run hooks:install`，repo-local `core.hooksPath` 為 `.githooks`。新 clone 仍須自行啟用。
- Codex／Claude session 中的自動觸發尚未驗證；Codex 的新 hook 仍需在 `/hooks` 完成信任。手動 wrapper 成功不能視為已完成平台信任。啟用方式見 [Agent 工作流程](agent-workflow.md)。

## Git commit 訊息與 git-cliff

- git-cliff 2.13.1 已由 mise 管理，lockfile 包含 Linux／macOS amd64、arm64 四個平台的下載校驗。
- `mise run hooks:install` 已啟用 `pre-commit` 與 `commit-msg`。真實 checkout 手動驗證合法中文 Conventional Commit 放行、一般非規範主旨拒絕，未建立額外提交。
- 使用真實 git-cliff 的臨時 repository 測試通過：合法／非法／空訊息、多行 breaking change、選項形狀的訊息，以及合併／fixup 的非規範主旨。檔案與 stdin 原始內容、尾端空行、真實 repository 的 refs/config 均保持原狀。
- Changelog 整合測試確認歷史非規範提交保留、功能／修正與 scope 分組、兩種 breaking 標記、正式版／prerelease／Unreleased 分段，以及非版本 tag 不改動發布界線。這些測試在 mise 環境中實際執行，沒有因缺少 git-cliff 而略過。
- `mise run changelog:check` 與 `CHANGELOG.md` 產生成功；CI 已加入完整歷史取得及相同檢查，遠端 CI 尚未執行。這些任務不連外、不執行自訂外部命令，也不建立 release 或部署。

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

- Ubuntu 24.04 的實際服務安裝／啟動／移除；Ubuntu 26.04 已驗證安裝、權限、停止及啟動，但未重開 VM 或卸載正式服務。
- macOS 主機上的 LaunchAgent 實際安裝，以及 Wi-Fi／Ethernet Proxy 套用與還原。一般測試只驗證產生內容與模擬的系統命令。
- macOS 的實際 WARP／WARP+ tunnel。Ubuntu 的免費 WARP 與後續 Unlimited 付費帳號均已通過上述端到端及 live 驗證。
- 公司 Tailscale 登入、ACL、MagicDNS、subnet route、與 Mac 原有 Tailscale 同時運作。
- 外部 WireGuard 伺服器與實際 VPN 設定。一般測試中的本機 userspace WireGuard 互連不等同外部 provider 驗收。
- 長時間、多時段與其他 GitHub CDN 檔案的效能。上述單一檔案限量比較不能取代這些驗收；Ubuntu 對外 IPv6 連線品質亦未驗證。

真實出口測試使用 `mise run test:live`，必須提供 `RILLWAY_LIVE_CONFIG`。缺少所需帳號或設定時回報 `NOT VERIFIED`／`SKIP`，不將跳過當成成功驗證。設定與限制詳見 [出口設定與驗證](providers.md)。

## 實作邊界

- CIDR 規則只比對客戶端提供的 literal IP；不先用宿主 DNS 解析 hostname 再套用 CIDR。hostname 的解析交給選定出口，避免在選路前洩漏私有名稱。
- WARP 不接受已知私有名稱或位址。具公網能力的 WireGuard 作為預設出口時也不接受私有目的地；明確的固定 WireGuard 規則可連私網，DNS 仍由該 profile 的 tunnel resolver 處理。
- IPv4、IPv6、unknown 的可確認觀察樣本分開呈現。`auto` 的自適應比較完整建連路徑；WARP remote DNS 不回傳真實目的 IP 時保留 unknown，不宣稱正在比較同一個 IP 類型。
- 自適應只比較建連時間與成功率，不依一般流量的下載速率重播請求或自動下載測速檔。
- 固定 VPN 出口不可用時回報錯誤，不轉成直連。規則更新只作用於新連線；主動停止服務或 VPN 則會結束受影響的串流。
- 觀察畫面保存最多 2,048 筆連線與 2,048 個自適應目的地，最久 24 小時；每個目的地／出口保存最近 20 個樣本。總計欄位涵蓋目前保留的連線資料，並非無上限的歷史帳本。
- 不解密 HTTPS，不保留 payload、網址路徑、Cookie 或認證 headers。TCP 計數包含傳輸的應用協定資料，並非解析後的檔案內容大小。
