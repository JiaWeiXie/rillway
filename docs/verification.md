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
