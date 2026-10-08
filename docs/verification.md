# 驗證狀態

這份文件只記錄可重跑的專案驗證，不保存個人帳號、主機名稱、IP、SSH 路徑、憑證位置、服務雜湊或私人基礎設施拓撲。正式環境的驗收紀錄應存放在受限制的營運系統。

## 2026-10-08：v0.4.1 發布與既有 Linux 服務升級

- 已推送可靠性修正與 changelog，發布 `v0.4.1`。GitHub CI 的 security、Ubuntu／macOS check、cross-build、service-acceptance 五項通過；Release build 重跑 security／race checks／fuzz 並成功產生資產。`release` 環境取得操作人員的明確人工核准後，產生附 provenance 的 draft，驗證後才公開發布。
- 四個 Linux／macOS binary 與 operations Skill 的 SHA-256 均通過；五個 provenance 均限定本 repository、Release workflow、`refs/tags/v0.4.1` 與對應來源 commit，並拒絕 self-hosted runner。LICENSE／THIRD_PARTY 與來源一致，Skill 封裝的七個允許檔案 CRC 通過。下載的 macOS arm64 binary 實際回報 `Rillway v0.4.1`。
- 在既有 Ubuntu 26.04 amd64 服務上，只原子替換已驗證的 binary，沒有重跑 setup／service install／memory-install。新 binary 的 `agent plan` 接受既有設定且 `would_change=false`；執行中的 `/proc/<PID>/exe` SHA-256 與發布資產一致。服務為 `active/running`、維持開機啟動，`NRestarts=0`。停止至可信 TLS 管理 API 回報新版約 1 秒，此值不代表所有出口端到端的恢復時間。
- 可信 HTTPS 管理 API 回報 `v0.4.1`，未驗證請求回 `401`；嵌入 Web 頁面、agent schema／status／唯讀 plan／未確認 mutation 拒絕、PAC 與 CLI PAC 一致性均通過。實際 HTTP 轉發、HTTPS CONNECT、SOCKS5 remote DNS 與 GitHub CDN 小型內容請求通過，未用請求重試改算成功。
- GitHub CDN flow 使用既有 WARP 出口且傳輸計數涵蓋回應內容；官方 Local Proxy 的 trace 為 `warp=plus`，帳號狀態為 `Unlimited`。只使用既有帳號與代理，未執行註冊／授權或下載速度測試。記憶體管理 API 只做唯讀驗收，回報值與 systemd 實際限制一致。
- 設定 revision 與內容、管理 token／TLS 檔案、主服務／記憶體 helper unit 及 drop-in 的 hash、owner 與 mode 不變；服務帳號、開機啟動、Restart、CPU／MemoryHigh／MemoryMax 政策保留。helper socket 維持 active/enabled，helper service 回到原本 inactive/static。
- 停止服務後備份既有設定／state、unit 與 drop-in，保留 ACL／xattr；備份目錄由 root 擁有且為 `0700`，私有驗證／state 檔案為 `0600`。確認 archive 內設定符合升級前 hash、舊 binary 仍可執行並回報原版本。部署流程設有驗收失敗回復；本次成功，未為測試回復而再次中斷正式服務。遠端暫存上傳檔案已移除，營運收據只保留在限權私有目錄。

限制：這次正式服務驗收沒有 Tailscale／WireGuard 出口，也沒有雙公網出口的自適應切換實測。下方 Tailscale 間歇失敗紀錄仍有效，根因未確認，不能用這次成功的 WARP 小型請求宣稱所有網站連線問題已排除。

## 2026-10-08：OrbStack Ubuntu 可靠性實測

使用兩台新建的 Ubuntu 24.04／26.04 arm64 測試機，執行目前工作樹交叉編譯的同一份 binary，核對安裝前後 SHA-256 一致。每台機器限制為 2 CPU、4 GiB RAM、32 GiB 磁碟，實際 cgroup 值為 `cpu.max=200000 100000`、`memory.max=4294967296`、`memory.swap.max=0`；關閉 Mac 檔案共享、SSH agent 轉送與通往宿主／其他 VM 的網路整合。Rillway listeners 僅監聽 loopback，未更動既有 VM、Mac Proxy／DNS／VPN 或正式部署。

| 項目 | Ubuntu 24.04 | Ubuntu 26.04 |
| --- | --- | --- |
| `scripts/acceptance-runtime.py` | 七項通過 | 七項通過 |
| 低權限 systemd、設定／state 權限 | `rillway` 帳號；目錄 `0700`、設定 `0600` | 同左 |
| 單一來源容量 | 64 條 HTTP CONNECT；額外 SOCKS5 被關閉，`source_limit` 增加 1 | 同左 |
| 共用總容量 | 四個來源各 64 條 CONNECT；第 257 條 SOCKS5 被關閉，只有 `total_limit` 增加 1 | 同左 |
| 滿載下管理與既有連線 | API 可用、PID 不變；既有 tunnel 收到正確內容，釋放後 SOCKS5 恢復 | 同左 |
| 自身 Proxy 迴圈 | 經 HTTP 轉發到自身 HTTP／SOCKS5 endpoints 均回 `502` | 同左 |
| 真實 `EMFILE` | 獨立測試程序 `RLIMIT_NOFILE=64`，實際 FD 數 64；同一 PID 存活，釋放後排隊 SOCKS5 傳輸成功 | 同左 |
| 超過握手期限的真實 TCP 建連 | listen backlog 滿載後延遲放行，約 11.23 秒建連成功，SOCKS5 回覆與內容正確 | 約 11.23 秒，同左 |
| 無法完成的真實 TCP 建連 | SOCKS5 約 15.01 秒失敗，符合建連上限 | 約 15.02 秒，同左 |
| Proxy 驗證 | 32 次未帶憑證均回 `407`，正確帳密仍成功；20 次錯誤帳密後 HTTP 回 `429`，SOCKS5 也拒絕正確帳密 | 同左 |
| Tailscale 端到端目的地矩陣 | IPv4／IPv6 literal、MagicDNS 完整／短名稱，經 HTTP、驗證測試 CA 的 HTTPS CONNECT、SOCKS5 remote DNS 均取得正確內容 | 同左 |
| 固定 Tailscale 路由與 DNS 隔離 | 可由 direct 存取的公網目的地仍被固定 Tailscale 路由拒絕；未知私有名稱失敗，不改選 direct | 同左 |
| Tailscale 停止／恢復與 state 所有權 | disconnect 後新連線失敗，connect 後恢復；停用時既有 1 MiB 串流雜湊正確，新連線拒絕；舊串流未結束時重新啟用回 `422`，結束後回 `200` | 同左 |
| 真實 systemd 重啟後的 Tailscale 傳輸 | HTTP 端到端探測恢復後，第一個 HTTPS CONNECT 仍曾回 curl `56`，不判為完整通過 | PID 改變、保留登入，驗證 CA 的 HTTPS CONNECT 恢復 |
| 不重試的穩態短連線取樣 | 第一批 HTTP 29／30、CONNECT 29／30、SOCKS5 30／30；另一批三種協定各 30／30 | HTTP 29／30、CONNECT 30／30、SOCKS5 30／30 |
| 加入匿名 origin trace、無逐筆 stats 查詢的短連線取樣 | 未執行此批 trace | HTTP 30／30、CONNECT 27／30、SOCKS5 30／30 |

七項既有驗收涵蓋 HTTP／CONNECT／SOCKS5、PAC、管理驗證／同源／revision conflict、無效設定與占用 listener 拒絕、daemon 內重啟 listener、systemd 停止／啟動／重啟及傳輸統計。Ubuntu 26.04 的 Python 3.14 另外印出既有驗收 helper 未顯式關閉 `HTTPError` 的 `ResourceWarning`；七項測試仍通過，此處保留警告，不算成 daemon 缺陷。

Tailscale 取樣仍重現間歇失敗，不能宣稱原先「偶爾連不到」已排除。Ubuntu 26.04 的失敗 HTTP 請求約 10.00 秒後由 curl 逾時；新建 flow 的出口為 Tailscale，建連約 7.09 秒、送出 144 bytes、收到 0 bytes。Ubuntu 24.04 的第一批 HTTP／CONNECT 各有一次 curl `28`。所有失敗均保留，沒有用請求重試改算成功。另停用 Proxy daemon，由相同 embedded outbound 直接建立新連線的 HTTP／HTTPS 對照各 30／30 成功；短時對照不足以排除間歇性底層傳輸問題，也不足以確認 Proxy 為根因。

另外一批保留 10 秒總預算、fresh connection、無請求重試，移除逐筆 stats 查詢，只在失敗後取統計，並在專用 origin 以匿名連線／sample ID 記錄接收時間。三個失敗 CONNECT 中，一個尚未收到 CONNECT `200`，另外兩個已收到 `200` 但沒有完成 TLS；origin 對後兩個連線記錄接受 TCP 後約 5 秒的 TLS handshake `i/o timeout`，沒有進入對應 HTTP handler。該時段 origin 對指定測試 peer 的 `CurAddr` 非空，沒有 peer relay；這只證明單邊狀態保留 direct endpoint，不能證明每個封包的實際路徑或把故障歸因於 DERP。`connect_ms` 包含 provider 的狀態／DNS／TCP 建連，`upload_bytes` 只計本地 Write 接受的 bytes，不能證明 origin 已收到。唯讀 review 未找到可證明由本次可靠性 patch 引入的根因；底層停滯原因仍未確認，未放寬 timeout 或加入產品重試。

`Running` 不當作端到端健康證據：重新啟用及重啟後，以專用 Tailnet HTTP 目的地的實際回應確認可用；此探測只供驗收等待就緒，未加入產品重試。IPv4／IPv6 literal 的已選 IP family 與傳輸統計相符；強制 IPv6 的 MagicDNS 名稱在實際 DNS 回覆沒有 AAAA 時明確失敗，不改走 IPv4 或宿主 DNS。

限制：OrbStack 共用 Linux 核心，且實際 `NoNewPrivileges`、`ProtectSystem`、`ProtectHome`、`PrivateTmp` 都被環境覆寫為關閉，因此未驗證獨立 VM 的核心／網卡隔離或 unit 的全部安全防護。此輪未操作 WARP／WireGuard，也未實測雙公網出口的自適應切換。兩個專用 embedded Tailscale 節點已由使用者登入；只使用本次建立的測試節點，未變更 Mac 或既有機器的 VPN 設定。

收尾：兩台測試機均還原原路由，移除專用驗收規則；systemd 服務為 `active`，管理 API 回 `200`，保留登入的 Tailscale 為 `Running`，還原後的實際 HTTP Proxy 請求均回 `200`。自建 origin／診斷程序及暫存驗收程式已移除；測試機與登入 state 保留，未刪除 Tailnet 裝置、未 commit／push、未部署正式主機。原始診斷紀錄只保留在限權的私有暫存目錄，不進入公開文件。

## 2026-10-08：連線可靠性與自適應路由保留

- 每項修正都先寫回歸測試並確認失敗，修正後才通過：SOCKS5 慢速但成功的出口建連不再因 10 秒握手期限而收不到回覆；一般 HTTP 轉發的出口建連與 CONNECT、SOCKS5 共用 15 秒上限；SOCKS5 accept 遇到 `EMFILE` 等暫時錯誤會退避重試，不再讓 daemon 結束；沒有附帶帳密的 Proxy `407` 不計入驗證失敗；套用無關設定不再清除自適應已學到的出口。
- `mise run check` 通過：Lint 0 issues、全部套件 race／shuffle／coverage 及 Python 測試。
- 以隔離的本機設定與實際 binary 在 loopback 驗證 HTTP 轉發、CONNECT、SOCKS5、自身 HTTP／SOCKS5 目的地拒絕。透過 `rillway agent apply` 啟用自適應後，`example.com:80` 的已學出口在新增無關規則後保留，為該網域新增固定規則後清除。未啟用 VPN、未修改系統 Proxy、未操作正式部署；臨時檔案已刪除。
- Code review 後追加修正並補回歸測試：初始出口（預設出口或自適應規則指定的出口）改變時清除已學到的出口；SOCKS5 出口建連完成後，回覆用戶端重新套用握手期限，進入 relay 前清除；SOCKS5 accept 退避期間取消會立即返回；`/api/v1/stats` 新增 `proxy_admission_rejections`，並以實際 daemon 塞滿同一來源的 Proxy 配額後，經管理 API 確認 `source_limit` 為 1。以暫時移除修正的方式確認：初始出口比較、建連後的期限重設、`serveOnce` 的計數接線，各自移除後對應測試都會失敗，還原後通過。修正後再次執行 `mise run check`，結果通過。另以實際 binary 在 loopback 驗證 HTTP、CONNECT、SOCKS5 均回應 200；同一來源持有 64 條連線時，第 65 條被直接關閉，`agent stats` 的 `source_limit` 從 0 增為 1，釋放連線後 SOCKS5 恢復回應 200。初始出口變更只由單元測試驗證，未在 binary 上重跑。
- 這些驗證不能證明正式環境「偶爾連不到」的原因已排除；正式部署仍需在授權後觀察。

## 2026-10-06：連線容量限制與受控部署

- `mise run check` 通過：Lint 0 issues、race／shuffle／coverage 及 Python 測試。新增來源與總連線容量、並行關閉後釋放、HTTP／CONNECT／SOCKS5 自身目的地拒絕、DNS 別名、IPv4 mapped 位址、TCP 半關閉後延遲回覆，以及代理額滿時仍能操作管理 API 的測試。
- 將 Linux amd64 開發 binary `0.3.1-incident-guard` 部署至 Ubuntu 26，先完成 loopback-only 驗證，再於明確授權後啟動限定來源的區網驗證。保存回復備份、既有憑證與 VPN 設定；服務維持開機不自動啟動，受控觀察期間不自動重新啟動。這不是公開 Release。
- 有效 systemd 限額為 `MemoryHigh=384M`、`MemoryMax=512M`、`MemorySwapMax=0`、`CPUQuota=100%`、`TasksMax=256`，另設定 `GOMEMLIMIT=256MiB`。這些限額只涵蓋 Rillway 服務，不涵蓋外部 WARP daemon 或整台 VM。
- 真實客戶端驗證受信任 TLS 管理 API、HTTP、CONNECT、SOCKS5 TCP、PAC、自身代理目的地拒絕，以及 GitHub CDN 的 HTTPS 請求命中既有 WARP 規則。官方 Local Proxy 的獨立 HTTPS trace 回傳 `warp=plus`。未變更 WARP 註冊、授權或模式。
- 66 個停留的 HTTP sockets 觸發每來源容量拒絕；同時 SOCKS5 共用代理額度，管理 API 仍成功，釋放後能建立新代理連線。未列入允許清單的來源對四個 listener 均被關閉。這是短時、受控的容量驗證，不是公開網路 DDoS 測試。
- 完成代理請求後，以三次間隔取樣觀察：程序 RSS 約 24 MiB，服務 cgroup 峰值約 6.1 MiB，沒有服務重新啟動，VM 可用記憶體維持約 4.7 GiB、swap 使用量為零。路由器連通測試 15／15 回覆；另一個區網來源連到 VM 的測試 5／5 回覆。NIC 收發錯誤為零；RX dropped 累計值在這三次取樣間沒有增加，不能據此宣稱整個開機期間都沒有丟包。
- 原先區網不穩的原因仍未確認；歷史 journal 沒有找到 OOM，且 NAS 的整台 VM 記憶體圖表不能代替 Rillway 程序記憶體。上述短時驗證沒有重現故障，也不等於數小時／數日穩定性驗收。臨時 listener、測試程序及上傳目錄已清除，私有操作紀錄與備份不進入 Git。

## 2026-10-05：Agent CLI 與公開 Skill

- `mise run check` 通過：Lint 0 issues、完整 race／shuffle／coverage 及 Python 測試通過。新增 JSON 成功／失敗格式、結束狀態、中英文錯誤、輸入上限、明確變更意圖、設定版本衝突、無變更不寫入、TLS 拒絕、秘密安全錯誤及遺失回覆不重試的測試。
- 以隔離、隨測試刪除的本機設定與真實 daemon，驗證受信任 TLS、讀取狀態、設定預覽、原子套用、舊版本拒絕、API 重啟及保存設定恢復。未啟用 VPN、修改系統 Proxy 或操作正式部署。
- Skill metadata validator 通過。封包測試確認固定七個成員、可重現的 ZIP、額外私有檔案不被收集，以及拒絕符號連結。以實際 binary 另確認 stdout 只有 JSON、正確結束狀態及 stderr 無重複錯誤。
- `mise run security` 通過；可達程式碼未發現受影響漏洞。一般掃描不代表能識別所有個資或未知漏洞，仍另外使用受限 denylist 檢查公開來源與 Skill 封包。

## 一般檢查命令

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

## 2026-10-05：程式版本與設定修訂號顯示

- 新增需要 Bearer 驗證的 `GET /api/v1/info`，只回傳 daemon 的程式版本。Web UI／TUI 使用伺服器版本；舊服務回傳 404 時明確顯示版本資訊未提供，其他認證或服務錯誤不當作舊版略過。
- Web UI 將設定修訂號及生效時間移至預設收合的「進階資訊」；TUI 服務設定頁按 `x` 顯示／隱藏修訂號，離開該頁會收起。設定 revision、原子更新及衝突檢查保持原有行為。
- `mise run check` 通過，涵蓋 Lint、race／shuffle／coverage、雙語文字、API 權限、遠端版本及舊服務相容性；發布腳本測試確認四平台都注入共用版本欄位。使用實際 linker 注入的開發版 binary 啟動隔離的本機 daemon，CLI 與 Web UI 顯示相同版本。
- Browser plugin 未提供，改用既有 Playwright／Chromium。桌面 1440×960、手機 390×844 的英文及繁體中文畫面皆正常載入、沒有水平溢出或非預期 JavaScript／console 錯誤；實際展開／收合進階資訊，確認修訂號只在展開時可見。另模擬舊服務的 404，確認仍可登入且顯示版本未提供。
- 瀏覽器僅對隔離的本機自簽憑證測試環境放寬憑證檢查，產品 TLS 驗證未變更。本輪未發布新版本或變更正式 VM、VPN、網路設定；測試程序與臨時憑證已清除。

## 2026-10-06：自有流量排除與服務記憶體介面

- Rillway 的管理／PAC listener 流量不進入觀察或自適應樣本；指向自身
  HTTP／SOCKS listener 的迴圈仍遭拒絕。測試涵蓋 literal IP、可確認的 DNS
  別名、雙棧 listener、探測預算釋放與更新中的設定世代。管理權杖與 ACL
  仍有效，其他同主機服務仍可正常觀察。Ubuntu VM 實際經 CONNECT 重複讀取
  自身 API，確認沒有增加該次測試的 flow 或流量總計。
- Web「設定 → 服務記憶體限制」及 TUI「服務設定 → m」共用需要權杖的管理
  API；支援百分比／MiB／GiB、現有值、單位換算、主機容量、即時用量及可設定
  上下限。編輯中的數值與版本不被背景更新／語言切換覆蓋，明確刷新才接受新
  版本。百分比使用主機／上層 cgroup 容量，不把服務既有上限當作主機容量。
- 安全下限採直連／官方 WARP 256 MiB、內建 VPN 1 GiB，並納入當下 cgroup
  用量加 25% 餘裕；保留仍有既有連線的退役 Tailscale 節點下限。設定低於
  下限、超過 90% 主機容量、過期版本、額外 JSON 欄位均有回歸測試；新啟用
  內建 VPN 前也檢查現有服務上限。這是保守政策，並非實測的精確最低需求。
- Linux 使用同一 binary 的獨立 socket-activated 控制程序，Web daemon
  維持低權限。實際驗證同 service UID 但不同 cgroup 的程序遭拒絕，未登入
  API 回傳 401、過期版本 409、無效範圍 422。Linux 專用的九項測試在 Ubuntu
  26.04 x86_64 執行通過，包括有界協定、即時屬性、原子保存、失敗還原、VPN
  下限與 peer 身分。
- 實際 VM 測試 MiB、GiB、百分比設定，逐次讀回 MemoryMax／MemoryHigh，
  確認修改期間 PID 不變，沒有新增自動重啟或 OOM kill。測試後還原先前
  上限，並在 OS 服務重啟後確認保留；設定檔位元組與部署前備份一致。控制程序
  的保存方式修正既有較晚 drop-in 蓋回舊限制的情況，數值與 drop-in 保存於
  同一原子檔案；保存失敗不套用，屬性／reload 失敗則還原。首次安裝另以檔案
  fixture 確認空白 drop-in 有效且不改既有上限，不留下 dangling symlink。
- Browser plugin 未提供，使用已有 Playwright 與隔離 Chrome；實際 VM 經
  loopback SSH 轉送，先以已取得的 CA／憑證指紋核對端點，再只在測試 browser
  context 放寬自簽憑證檢查，產品 TLS 驗證未變更。英文／繁中、1440 與 390 px
  的記憶體表單皆完成檢查；GiB 儲存後以 API 確認有效值，切換單位與語言保留
  輸入，沒有橫向溢出或 JavaScript／console 錯誤。另以實際 TUI 完成中英切換、
  讀取、換算與儲存；權杖維持隱藏。
- `mise run check` 通過：Lint、race／shuffle／coverage 與八項 Python 測試。
  Linux 專用 source 另做 cross lint；systemd unit 驗證通過，僅出現 Ubuntu
  既有 XFS 相依 unit 的 CPUAccounting 棄用提示。追蹤來源／可達歷史的
  public-source 檢查，以及本次已修改與新增檔案的私人模式及 Gitleaks 目錄
  掃描通過；沒有新增外部依賴、MCP 或發布產物。
- 本輪更新到未發布的 `0.3.1-incident-guard.5`；原 ACL、主服務開機啟動政策、
  VPN 身分與網路設定保留。測試憑證／權杖複本、轉送、暫存測試 executable
  與 browser profile 清除；部署備份及原始驗證收據只保留於私有位置。未新增
  VPN 註冊、付費 license 套用、全新 VM 首次引導驗收或長時間負載測試；不以
  此次檢查宣稱先前 NAS 記憶體／區網事件的根因已確認。
