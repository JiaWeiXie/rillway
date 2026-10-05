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

## 2026-10-05 公開專案準備驗證

- `mise run check` 通過：Lint 0 issues，完整 race／shuffle／coverage 測試通過。新增管理驗證及 HTTP／SOCKS5 共用的登入失敗限速，包含來源正規化、容量上限、到期恢復、並行操作及正確憑證無法繞過暫時封鎖的測試。
- `mise run security` 通過：目前追蹤內容、所有可達歷史 blob、commit／tag metadata 及私下保存的已知個資 denylist 均未命中；Gitleaks 歷史掃描未找到秘密。私人 denylist 與掃描資料未追蹤。
- `govulncheck` 依 Linux／macOS 的 amd64／arm64 四種發布設定執行，沒有可達漏洞，也沒有匯入含已知漏洞的 package。Module 層級另列出 [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932) 的 OpenPGP 警告；Rillway 沒有匯入該 package，沒有藉由升級其他 crypto API 宣稱消除此警告。
- `actionlint` 驗證 CI／Release workflow 通過；Actions SHA 從各 upstream repository 的 tag commit 核對。Workflow 設計採 PR 唯讀、關閉 checkout 憑證保存、GitHub 託管 runner、指定產物清單，以及獨立有寫入權限的 Draft Release 工作。
- Git hook 測試確認文件 commit 也須執行秘密掃描，掃描失敗會阻止提交；檢查使用已暫存副本，不包含忽略的正式資料。
- Release 測試確認版本進入四平台 linker flags、拒絕參數注入，且 checksum 不納入 dist 中其他未知檔案。四平台發布建置、manifest 雜湊檢查與有時間上限的 parser fuzz 測試通過。
- 來源 commit `18cf723` 的四種 binary 已以 `v0.1.0-rc.1` 預覽版本建置，CLI 版本正確，VCS metadata 顯示 `vcs.modified=false`，四份 checksum 通過。檢查 binary 與 Git source archive 都未命中私下保存的已知個資；source archive 未包含本機 state、憑證、部署紀錄或產物目錄。沒有因此建立任何發布 tag 或公開 Release。
- 人工查看目前追蹤的四張 Web UI 截圖：只顯示本機合成流量、沒有 Token、私人主機位址或姓名。文字掃描不代表已自動驗證圖片內的所有內容。
- 本輪尚未建立 GitHub remote／repository、執行 hosted CI、產生 GitHub attestation、公開 Release 或啟用 repository 保護；相關流程已準備，實際設定與驗收仍待指定 repository 後完成。沒有部署本輪認證限速修改到正式 VM。

## 2026-10-05：公開 GitHub repository 設定

- 公開 repository 為 [JiaWeiXie/rillway](https://github.com/JiaWeiXie/rillway)，已設定專案介紹、topics、雙語 README 的 CI／Release／MIT badge、下載連結與公開 CODEOWNERS。
- REST API 回讀確認 secret scanning、push protection、Dependabot security updates 及私下漏洞回報啟用。沒有加入 VM／VPN repository secrets。
- `main` ruleset 要求 PR／CODEOWNERS 審查、已解決的對話及五項 GitHub Actions CI 檢查，禁止一般使用者刪除／force push；指定公開維護者保留有紀錄的 bypass。
- `v*` tags 限定指定維護者建立、更新及刪除。`release` environment 要求指定維護者核准，且只接受 `v*` tags。
- Actions 預設唯讀、禁止核准 PR，所有外部貢獻者需執行核准；僅允許 GitHub Actions 與指定 SHA 的 mise-action，並要求 action 使用完整 commit SHA。
- 這一節只記錄設定驗證；首次 hosted CI 與正式 Release 的結果另由公開 workflow／Release 記錄確認。

## 2026-10-05：首次 GitHub hosted CI

來源 commit `96f1be54695685cd897c491333b65e90d04fc348` 的 [CI run 37251254819](https://github.com/JiaWeiXie/rillway/actions/runs/37251254819) 全部通過：

- `security`：Git 歷史／秘密掃描、四平台 Go 漏洞檢查及 workflow 語法檢查。
- `check (ubuntu-24.04)`／`check (macos-15)`：changelog 驗證、lint、race／shuffle／coverage、本機建置與有時間上限的 fuzz。
- `cross-build`：Linux／macOS 的 amd64／arm64 四種獨立 binary 與明確的 checksum／module inventory 產物清單。
- `service-acceptance`：全新、可丟棄的 GitHub Ubuntu 24.04 runner 上實際首次安裝 systemd 服務，驗證低權限帳號、設定／state 權限、TLS／Token、PAC、HTTP Proxy 與服務 stop／start／uninstall。沒有接觸正式 VM 或 VPN 帳號。

Workflow 與下載檔的最終發布紀錄可在 [Actions](https://github.com/JiaWeiXie/rillway/actions/workflows/release.yml) 及 [Releases](https://github.com/JiaWeiXie/rillway/releases) 查看。首次 hosted CI 不代表 Ubuntu 26.04、macOS LaunchAgent、真實 VPN 或家用網路效能已完成驗收。Dependabot 更新仍須獨立審查；未經審查的 action SHA 不會自動加入允許清單。

## 2026-10-05：發布來源一致性修正

正式 binary 檢查發現 CI 安裝工具後改寫 `mise.lock`，Go 因此標示 `vcs.modified=true`。第一輪發布在保護關卡取消，沒有公開 GitHub Release。CI／Release 改用 `MISE_LOCKED=1`；CI 檢查工具安裝不改動追蹤來源，release 在 notices 準備後拒絕任何未提交變更。回歸測試確認生成來源漂移時不會建置 binary，沒有使用停用 VCS metadata 的方式掩蓋狀態。

## 已覆蓋的行為

- 分流規則優先序、網域邊界、CIDR、雙棧、私網保護與出口 DNS 隔離。
- HTTP Proxy、HTTPS CONNECT、SOCKS5 TCP、來源限制、驗證、錯誤輸入、雙向傳輸與關閉。
- 自適應樣本、改善門檻、冷卻、探測預算、取消與既有連線維持原出口。
- 設定驗證、revision conflict、原子儲存、管理 API、TLS、權杖驗證與秘密遮罩。
- WARP 模式檢查、重複註冊保護、Tailscale 私網限制與 userspace WireGuard 設定驗證。
- systemd／LaunchAgent 產生內容、PAC 套用／還原、Docker 匯出與安全 JSON 合併。
- 英文／繁體中文 Web UI、TUI、CLI 文案，以及中文與 Emoji 輸入。
- TUI 的 Web UI 管理權杖預設隱藏、明確顯示、離開頁面自動隱藏，以及 client profile 不保存權杖內容。
- CLI 依 OS 慣例搜尋設定、Linux 新／舊安裝路徑、macOS 大小寫路徑相容、明確路徑覆寫，以及 TUI 本機設定優先於自動記住的連線。測試確認私有／無效設定不會偷偷切換伺服器，明確設定路徑也不依賴使用者設定目錄的環境變數。

## 外部環境

### 2026-10-05：既有 Ubuntu VM 的 binary 更新驗收

已在既有 Ubuntu 26.04.1 LTS／x86_64 VM 更新 Linux amd64 binary。來源 commit 為 `36958c0d046a14faf609432f8a0dd20c00444072`；更新後 SHA-256 為 `85f60dde8cb1f60aea497aec3c9dfe98d554cba7c95412c3be4732d75516268e`。先停止 Rillway，再備份有效設定與 state，原子替換 binary，使用既有 unit 重新啟動。

- systemd 為 `active/running/enabled`，執行帳號 `rillway`，更新後 `NRestarts=0`；運行中 executable 的 SHA-256 與新 binary 相同。
- 設定、unit、管理 Token 與 TLS 憑證的雜湊均保持不變；既有設定版本保留。備份目錄 `0700`、state archive `0600`。
- VM 的 HTTP forwarding、HTTPS CONNECT 與 SOCKS5 TCP 實際傳輸通過；Mac 的 HTTPS CONNECT、SOCKS5、明確信任憑證的 Web UI、PAC 與內建中文字型／Emoji 字型可取得，未登入 API 正確回傳 `401`。
- VM 與 NAS 經 Proxy 對 GHCR 的 `GET /v2/` 得到預期的未登入 `401`；觀察紀錄確認 GHCR 使用既有 WARP 出口，GitHub 主站使用 direct。WARP 顯示 `connected`、`Unlimited`、`proxy` 且 listener 可用，未重新註冊或修改 license。
- 此次未驗證首次安裝、重開機、備份還原、Docker 私有映像授權、真實 Tailscale／WireGuard 或下載效能。以下外部環境清單仍保留其未驗證範圍。

私人主機資料、完整操作紀錄與備份位置保存在 repository 之外或已被 Git 忽略的受限資料夾，未加入 Git。

### 2026-10-05：CLI 預設設定路徑更新驗收

來源 commit 為 `616a3b90a9a247dbd193ab126d0521712c92e71d`。`mise run check`、本機建置、四平台發布建置與發布檔雜湊檢查通過，並更新既有 Ubuntu VM。

- VM 同時有新、舊預設設定檔；CLI 沿用 Rillway 安裝 unit 指定的有效設定，沒有改動 unit 或搬移設定。
- 在 SSH Terminal 中直接執行已安裝的 `rillway`，不傳 `--config`／`--url`；TUI 載入現有設定版本，出口與 listener 正確，管理權杖保持隱藏。
- 使用隔離的使用者設定目錄及刻意失效的舊 client profile，確認本機服務設定優先，成功連線後只記住網址與憑證檔路徑，不保存權杖內容。
- `pac`、`docker export --target env` 不加設定路徑即可運作。更新後 TLS、API 權限、PAC、HTTP CONNECT、SOCKS5 與 GHCR 未登入回應均驗證；設定、unit 與憑證／權杖雜湊未變，服務維持 active/enabled。
- macOS 路徑與舊小寫目錄相容已由自動測試驗證；本次未重新執行 macOS LaunchAgent 安裝或 Ubuntu 首次安裝。

真實出口測試使用 `mise run test:live`，必須明確提供 `RILLWAY_LIVE_CONFIG`。缺少帳號、設定或目標時應回報 `NOT VERIFIED`／`SKIP`，不能把跳過視為成功。

以下仍需在各自的受控環境驗證；後文的 OrbStack 實測只涵蓋明列範圍：

- NAS 的完整 Ubuntu hypervisor VM：冷開機、核心／虛擬網卡、有效 systemd 安全隔離與實際網路拓樸。
- macOS LaunchAgent，以及指定 network service 的 PAC 套用與還原。
- 新裝置的 WARP+ license 套用，以及公司 Tailscale ACL／subnet routes。既有 WARP+ 與測試 Tailnet 的 MagicDNS 已在下文明列驗證。
- 外部 WireGuard provider、公司服務與長時間 GitHub CDN 效能。
- Docker Desktop 與 OrbStack 內建 Docker 的全域代理設定；機器內的 Docker Engine、獨立 BuildKit 與容器內連線已在下文驗證。

操作限制與必要前置條件見 [部署文件](deployment.md)、[VPN 出口](providers.md)及 [Docker 代理](docker.md)。

## 實作邊界

- CIDR 規則只比對客戶端提供的 literal IP；hostname 解析交給選定出口。
- 固定 VPN 出口失敗時不會偷偷改成直連；更新規則只影響新連線。
- 不解密 HTTPS，不保留 payload、網址路徑、Cookie、認證 header 或 VPN 私鑰。
- 觀察資料有容量與保存時間上限；未知的上游 IP 會維持 unknown。

## 2026-10-05：PAC、出口表單與 Web 重啟

- `mise run check` 通過：Lint 0 issues，完整 race／shuffle／coverage 測試通過；`mise run build` 通過。
- 新增出口 API 測試涵蓋登入／同源限制、revision 衝突、重複名稱、錯誤類型、路徑穿越、設定大小限制、Shell hooks 拒絕、秘密不回傳、`0600`／`0700` 權限、空白編輯保留原檔與套用失敗清理。
- 重啟 API 先檢查已儲存的設定、憑證與新的監聽位址；真實本機 TLS 服務測試確認接受回應後完成重啟、重新載入設定、釋放並重綁 listeners、沿用管理權杖，以及無效設定／憑證／占用位址不停止原服務。重啟在同一低權限程序內完成，不執行 sudo 或重開主機。
- 使用已安裝的 Playwright／Chrome 驗證隔離的本機 HTTPS daemon，英文／繁體中文與 1440、390 px 四種畫面皆通過。實際操作 PAC 網址複製及檔案讀取、唯讀 Server／JSON、WireGuard 從瀏覽器匯入／儲存／空白編輯、Tailscale 選用授權金鑰、清除關閉表單中的秘密，以及重啟確認／取消／服務恢復。
- 頁面身分、非空畫面、無錯誤 overlay、無橫向溢出與截圖檢查通過；沒有未預期的 JavaScript／console 錯誤。重啟期間的暫時 connection-refused 已在明確重啟區間分類，重啟後恢復連線。截圖與測試設定放在 repository 之外，截圖不顯示金鑰內容。
- `security:source` 的追蹤來源／歷史／私人 denylist 檢查通過；Gitleaks 掃描目前 `internal/control` 目錄（含本輪新增檔案）未發現秘密。

這一階段的介面驗收未啟用真實 VPN、部署正式 VM 或發布版本。一般設定與完整 JSON 仍為唯讀；新增 Web 重啟只重新載入 Server 上儲存的設定。後續真實 VPN 與 systemd 驗收見下文；macOS LaunchAgent 仍需另行驗證。

## 2026-10-05：OrbStack 隔離機器實測

使用兩台新建、專供 Rillway 測試的 Ubuntu 24.04／26.04 arm64 機器。各配置 2 CPU、4 GiB RAM、32 GiB 磁碟，關閉 Mac 檔案共享與 SSH agent 轉送。未修改既有機器、Mac VPN／DNS／系統 Proxy、NAS 或正式 VM；既有 WARP+ 只做唯讀狀態與代理 trace 驗證。測試當時使用尚未發布的開發版本；實測證據與後續發布檔案的建置／來源驗證分開。

| 項目 | 實際結果 |
| --- | --- |
| 引導安裝與 Linux 服務 | 兩個 Ubuntu 版本均完成單一 binary 首次安裝；daemon 帳號為 `rillway`，設定及 state 目錄 `0700`、設定／Token／TLS 檔 `0600`，預設路徑 CLI 可讀取有效設定 |
| Proxy 與 API | HTTP forwarding、HTTPS CONNECT、SOCKS5 實際傳輸、PAC 讀取與傳輸統計通過；無 Token、跨來源寫入、無效設定、版本衝突、內建 direct 刪除與未允許來源皆拒絕 |
| 代理驗證與防猜測 | 真實 HTTP／SOCKS5 要求正確代理憑證；管理 API 二十次失敗後回傳 `429` |
| Web 重啟 | 載入新 HTTP listener、釋放舊位址，daemon PID 不變；無效已儲存設定及占用連接埠會先拒絕，原服務繼續運作 |
| systemd 生命週期 | 真實停止／啟動／重啟，以及兩台機器重新啟動後的自動啟動通過；解除安裝保留設定與憑證，還原 unit 後可啟動 |
| Binary 更新與還原 | Ubuntu 24.04 的 `v0.1.0` release → 目前開發 binary → `v0.1.0` 回復 → 開發 binary 通過；每次原子替換並重新啟動，設定、Token 與 TLS 身分雜湊保持不變 |
| 免費 WARP | 官方 Linux client `2026.7.1377.0` 實際安裝、Web API 首次與重複註冊、Local Proxy 連線及 trace 驗證通過；HTTP CONNECT／SOCKS5 與 Docker 請求均確認 `warp=on` |
| 手動停止 | WARP 與 WireGuard 停止後，固定規則的新連線失敗，不改用 direct、不自行連回；明確 Connect 後可恢復並驗證 |
| 既有 WARP+ | 正式部署的帳號 `Unlimited`、模式 `proxy`、listener 可用；直接經官方 Proxy 的 Cloudflare trace 為 `warp=plus`。沒有更換 license 或重新註冊。管理 UI 的 `verified_at` 尚未設置，因此不把此欄位當成本次證據 |
| WireGuard | userspace client 實際連到另一台機器的 Linux kernel WireGuard peer；IPv4／IPv6 HTTP、HTTPS、私有 DNS、SOCKS5 remote DNS 通過；Web 匯入的金鑰不回傳，檔案權限正確 |
| 既有串流 | 停用 WireGuard 出口時，進行中的 1 MiB 測試下載仍完成，新連線依固定規則失敗 |
| Tailscale | 兩個經使用者瀏覽器授權的專用 tsnet 節點實際連線；IPv4、IPv6、MagicDNS 與重啟後登入恢復通過，和 WARP／WireGuard 共存；未擁有的公網目的地不能透過主機網路逃逸 |
| DNS 隔離 | WireGuard 可解析的私有名稱切到 Tailscale 後失敗，不沿用 WireGuard 答案或改走公共 DNS |
| Docker | Ubuntu 26.04 Docker Engine `29.1.3` 經代理拉取公開映像；client 匯出設定注入容器及 Build，舊版 builder、獨立 BuildKit、Compose、容器內 WARP 與 WireGuard 私有 DNS／HTTPS 通過；建置成品未保留 Proxy 環境變數 |
| 另一台主機的 Docker | Ubuntu 24.04 Docker Engine 透過另一台測試機的 Rillway 拉取映像，容器的實際 trace 為 `warp=on`，模擬 NAS 使用遠端 Proxy |
| 自適應 | 只對 RFC 5737 合成目標的直連路徑加入 400 ms 延遲，採原本三個成功樣本、兩輪證據及十分鐘冷卻設定。約第 605 秒，新連線從 direct 切到 WireGuard；當時 median 約 403.5 ms → 4.15 ms，候選有五個成功樣本。此結果驗證決策流程，不代表 GitHub 下載加速倍數 |
| Web UI | 實際 VM 經 loopback SSH 轉送以 Playwright／Chrome 驗證：英文／繁中、1440／390 px、完整 PAC URL、唯讀設定、重啟控制與中文字型皆正常，沒有橫向溢出或未預期瀏覽器執行錯誤 |

`scripts/acceptance-runtime.py` 在兩個 Ubuntu 版本均通過七項測試，修改後在同一機器可重跑；執行與保護條件見 [拋棄式驗收](deployment.md#拋棄式-ubuntu-執行驗收)。一般 `mise run check` 通過，Lint 無問題，race／shuffle／coverage 測試通過。以低權限 daemon 帳號執行既有 `tests/live`，WireGuard、免費 WARP、Tailscale 真實 TCP 測試通過；免費 WARP 的 WARP+ 子測試明確 `SKIP`，與上列既有正式 WARP+ 的已驗證結果分開。

### 環境限制與剩餘範圍

- OrbStack 共用 Linux 核心，機器重啟驗證的是重新啟動 systemd 使用者空間，不是 NAS VM 的獨立核心冷開機。它也會用全域 drop-in 覆寫 `NoNewPrivileges`、`ProtectSystem` 等設定；因此這輪不宣稱 OrbStack 已執行 unit 裡的全部隔離。既有正式 VM 的有效 `NoNewPrivileges=yes`、`ProtectSystem=strict`、`ProtectHome=yes` 與 `PrivateTmp=yes` 另由唯讀查詢確認。
- 新建 x86_64 模擬機連基本命令也無法完成，已停止，改用原生 arm64。這輪沒有把該機器算作 x86_64 通過；既有正式 x86_64 VM 的先前驗收仍是不同證據。
- Docker 內建 BuildKit 的 nested overlay 掛載被 OrbStack 拒絕；獨立 `docker-container` BuildKit 改用官方支援的 `native` snapshotter 後實際建置通過，未降低 Mac 或既有機器的安全設定。
- 尚未操作 macOS LaunchAgent／系統 PAC 套用還原、Docker Desktop／OrbStack 的全域代理、公司 Tailnet ACL／subnet routes、第三方 WireGuard 公網 VPN、首次 WARP+ license 套用或長時間 GitHub CDN 下載比較。
- 測試期間的金鑰、登入 URL、設定、主機位址、完整記錄及截圖只放在 Git 忽略的私有目錄或 repository 之外，不放入公開文件。驗收後先清除測試延遲、合成路由與臨時瀏覽器轉送。依使用者要求，發布前再刪除兩台原生測試機、未能驗收的 x86_64 測試機，以及本機測試金鑰、設定、記錄與截圖；使用者已移除兩個 Tailnet 測試節點。公開報告與可重跑的測試程式保留，正式環境設定及部署備份不受影響。

## 2026-10-05：家用規格配額壓力測試

在上述 Ubuntu 26.04 arm64 專用機測試目前開發 binary。流量產生器及目的端放在另一台 Ubuntu 24.04 測試機，目的端只回傳合成資料；沒有對 NAS、正式 VM、公司服務或公開網站做壓測。官方免費 WARP 保持待機，WireGuard 與 Tailscale 使用既有專用測試 peer。Docker／containerd 暫停，避免把 Docker 的資源用量混入結果。

參考[家用 Beryl AX 官方規格](https://www.gl-inet.com/products/gl-mt3000)的雙核心／512 MB 配置，比較 256 MiB／一核心、512 MiB／一核心、512 MiB／兩核心、1 GiB／兩核心。OrbStack 配額之外，另用臨時 `CPUAffinity` 限制 Rillway 可見核心，確認實際 PID 的 `Cpus_allowed_list` 為 `0`／`0-1`；有效 `cpu.max`、`memory.max` 符合每輪設定，swap 上限及實際用量皆為零。受測機的傳出速率以 TBF 限制為合計 1 Gbit/s，目的端及產生器在另一台機器共用四核心配額。

每個基本負載執行 15 秒，每次請求期限 15 秒：HTTP／CONNECT／SOCKS5 的直連出口各使用 32 個並行工作下載 8 MiB；短連線使用 128 個工作、64 KiB、每次重新建立 TCP；WireGuard 與 Tailscale 各使用八個工作下載 8 MiB。每秒以正確 TLS 憑證與 Token 讀取管理統計，確認命中出口、使用中的連線、RSS、cgroup 記憶體、FD、PID 與 OOM 計數。

| 配額 | 完整量測結果 |
| --- | --- |
| 256 MiB／一核心 | HTTP／CONNECT／SOCKS5 直連及 128 工作的短連線通過，四項均有有效監測；Rillway RSS 最高約 54 MiB。Tailscale 負載的完整監測超過等待上限，中止該配額並還原資源；WireGuard 不再執行。不能列為完整 VPN 組合通過 |
| 512 MiB／一核心 | 六項皆有完成下載及有效監測，請求錯誤、API 失敗及 OOM 為零；WireGuard 約 556 Mbit/s、Tailscale 約 597 Mbit/s。WireGuard 時程序 RSS 約 308 MiB、整機 cgroup 到達 512 MiB 上限；記憶體餘裕不足 |
| 512 MiB／兩核心 | 直連三種協定、128 工作短連線、Tailscale 共五項有有效監測；WireGuard 有完成下載，但完整監測逾時，重跑仍未取得有效資料。此項明確列為未驗證，不能只因產生器結束碼為零就算整輪通過 |
| 1 GiB／兩核心 | 六項全部有有效監測；請求錯誤、API 失敗及 OOM 為零。WireGuard 階段整機 cgroup 最高約 621 MiB，較 512 MiB 有餘裕 |

**1 GiB／兩核心的個別負載：**

| 負載 | Mbit/s | 完整請求 | 到期取消 | RSS 峰值 MiB | 管理 API p95 ms |
| --- | ---: | ---: | ---: | ---: | ---: |
| HTTP 轉送／直連 | 624.2 | 128 | 32 | 43.1 | 4.7 |
| CONNECT／直連 | 582.9 | 114 | 32 | 46.2 | 7.1 |
| SOCKS5／直連 | 577.1 | 113 | 32 | 50.2 | 5.2 |
| 128 工作短連線／直連 | 53.4 | 1490 | 128 | 70.5 | 14.6 |
| Tailscale | 677.0 | 148 | 8 | 173.1 | 12.3 |
| WireGuard | 450.7 | 98 | 8 | 363.1 | 11.2 |

另在 1 GiB／兩核心同時執行 60 秒混合負載：直連 32、WireGuard 八、Tailscale 八，單次 1 MiB。實際統計確認三個出口及最高 48 條使用中連線，合計約 674.7 Mbit/s；完整下載共 4805 次，請求錯誤、API 失敗、PID 改變及 OOM 皆為零。到期取消 48 個工作另列，不宣稱所有已開始請求都完成。程序 RSS 最高約 322.9 MiB、整機 cgroup 576.3 MiB，管理 API 最慢 22.5 ms。依此測試的記憶體需求，正式 Ubuntu VM 建議至少 1 GiB／兩核心；256／512 MiB 適合找出資源壓力，不作完整組合的穩定最低規格承諾。

### 重跑與限制

- 新增 `tools/loadtest` 與[中英操作文件](stress-testing.zh-Hant.md)。一般測試只連本機測試伺服器，涵蓋三種代理及直連、來源容量限制、SOCKS5 畸形／分段回覆、錯誤參數、百分位與到期取消分類。真正壓測需要 `--owned-target`，不會由 CI、hooks 或一般測試自動啟動。
- 吞吐量包含已收到的部分下載位元組，完成數只計完整 HTTP 200；回應 p95 包含整個 body，不等於建連時間。每輪結束的取消與中途請求錯誤分開計算。初期五秒期限測試的逾時及缺少有效監測的記錄都保留，沒有改稱成功；後續資料採明確十五秒期限及修正為六十秒上限的延遲直方圖。
- 基本負載每項只執行一次，混合負載一分鐘；沒有持續數小時／數日 soak、TLS 下載吞吐量、上傳、IPv6、實體 NIC／Wi-Fi、路由器 Flash 或 OpenWrt 安裝驗收。WARP／WARP+ 的公網下載吞吐量未測，先前已驗證的 WARP+ 付費狀態不受影響。
- Apple Silicon 主機及共用核心、核心網路 offload、CPU 時間配額與 TBF 都無法重現路由器 SoC。更高 CPU 配額未保證更高速度／更低記憶體；這些單輪數字不構成 CPU scaling 或實體路由器速度結論。流量產生與目的端共用同一測試 peer，也不等同三台獨立實體機器。
- 測試後兩台機器還原原本兩核心／4 GiB，移除臨時 CPUAffinity 與 TBF，恢復 Docker／containerd、停止合成目的端。真實設定、帳號、Token、憑證與測試金鑰未進入 Git；原始結果留在忽略的私有目錄。
- 本輪 `mise run check` 通過：Lint 零問題、所有套件的 race／shuffle／coverage 測試通過，包含新增壓測工具。新增工具與文件的 Gitleaks 目錄掃描未發現秘密，私人 denylist 的追蹤來源與 437 個可達歷史 blob／metadata 審查通過。還原後 WireGuard、Tailscale 實際小量請求及 WARP Connected／healthy 狀態正常；壓測階段未推送、發布或更新正式 VM。

## 2026-10-05：相依更新 PR 與每月彙整

- 核對五個 Dependabot PR 的上游 tag 與完整 action SHA，包括來源證明 action 內部固定的 `actions/attest` SHA；合併保留原始 PR commits。
- 修正 mise-action 新 SHA 尚未加入 GitHub Actions 允許清單造成的 CI 啟動失敗，只加入已核對的 SHA，沒有放寬成 wildcard。
- 產物上傳明確保留 archive 模式，下載明確解壓縮並在 digest 不符時失敗；發布工作仍不執行 repository 程式碼。
- `mise run check` 通過：Lint 0 issues、完整 race／shuffle／coverage，以及五項每月報表測試。測試涵蓋 Go JSON 串流、替換模組略過、Actions 去重與 annotated tag、上游資料跳脫、只更新 bot 自己的 issue，以及無效產物拒絕發布。
- `mise run security` 通過：私人 denylist／可達 Git 歷史／秘密掃描與 workflow 檢查未命中；四平台 Go 檢查仍只有未匯入 package 的既有 module 層級警告。
- 實際執行唯讀報表收集，可列出 Go 與 Actions 更新。設定改為每月 1 日台灣時間 09:17 更新同一個 bot issue，停用版本及安全修補 PR；REST 回讀確認自動安全修補關閉，漏洞警示仍啟用。
- 本輪不更動正式 VM、VPN 或網路設定。Hosted CI、每月 issue 與發布結果以對應的公開 Actions／Release 紀錄為準。
