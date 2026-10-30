# 安全政策

[English](SECURITY.md) · [繁體中文](SECURITY.zh-Hant.md)

Rillway 適用於可信任的區網或 VPN。原始碼公開後，管理介面與 Proxy 仍應留在這些網路內。任何軟體或安全檢查都不能保證金鑰永遠不會外洩。

## 回報漏洞

請在 GitHub repository 使用 **Security → Report a vulnerability**。維護者應在公開前啟用私下漏洞回報。不要在公開 issue 張貼攻擊方式、Token、私鑰、license、真實主機位址、正式設定截圖或診斷封存檔。若私下回報尚未啟用，先等待維護者啟用，不要公開敏感內容。

提供版本、影響及使用假資料的最小重現案例。目前未承諾固定回應期限；安全修正以最新發布版本與 `main` 為主，舊版可能需要升級。

## 部署與防護範圍

- 預設 listener 只監聽本機。區網使用時選擇特定 IP、最小來源允許清單，並以主機防火牆限制四個 listener。不要從網際網路轉發這些連接埠，也不要把 `0.0.0.0/0` 或 `::/0` 加入來源 ACL。
- HTTP／SOCKS5 共用可設定的准入預算（`connection_limits`，預設最多 256 條用戶端連線，每個來源 IP 最多 256 條）。管理介面與 PAC 端點各有一份固定配額，最多 128 條，每個來源 IP 最多 32 條；管理介面額滿不會擋住 PAC。在解析協定或 TLS 握手前檢查 ACL 與容量，超額直接關閉連線。半關閉的 tunnel 到完全關閉才釋放配額。這些限制不代表頻寬或程序總記憶體上限。`GET /api/v1/stats` 的 `proxy_admission_rejections` 記錄 listener 啟動後，允許來源的 HTTP／SOCKS5 連線因單一來源或總量上限被關閉的次數；用戶端看到的是連線被重設。
- SOCKS5 UDP 目的地 socket 有兩層上限：每個 association 32 個，全域預算為生效中 `max_connections` 的 4 倍。超出全域預算、同一 association 內 5 秒內失敗過的目的地、字面位址是本 daemon 自己 listener 或使用中 UDP relay socket，或主機名稱實際連到使用中 relay socket 的封包，都會被丟棄並計入 `proxy_udp_drops`。主機名稱別名解析到 Rillway 自己的 HTTP／SOCKS5 Proxy listener 時會被拒絕，並在 `udp_dial_failures` 記為 `blocked`。失敗紀錄不會跨客戶端共用；UDP 計數不記錄主機名稱或 IP 位址。
- Proxy 拒絕連回自身 HTTP／SOCKS5 listener，包括 wildcard 綁定位址下已知的本機 IP，以及所選出口實際連線確認的 DNS 別名，不額外查詢主機 DNS。這項保護無法偵測所有涉及外部上游 Proxy 的迴圈。
- 管理介面與 PAC 只服務自己的用戶端：經 Proxy（轉發、CONNECT 或 SOCKS5）連到它們的請求會被拒絕，不論是直接用位址，或用所選出口解析到同一個 listener 的名稱，也不列入統計或自適應樣本。請直接開啟 Web UI 與 PAC，不要經過 Proxy；手動設定 Proxy 的瀏覽器需把 Rillway 位址加入例外清單。它們仍保有各自的來源 ACL、HTTPS 與管理驗證。`GET /api/v1/stats` 以 `proxy_management_refusals` 回報被拒絕的次數。同一主機的其他服務經明確設定的 Proxy 連線時仍可連線並會被記錄。PAC 自動讓已設定的 Proxy 主機直連，即使使用者停用了 bypass 項目也保留。
- 管理介面使用 HTTPS、自動產生的 256-bit 隨機 Token、修改操作的同源檢查、CSP、禁止快取及遮蔽秘密的錯誤訊息。核對並信任憑證，不要關閉 TLS 驗證。
- 同一來源 IP 一分鐘內登入失敗 20 次後，會暫時阻擋驗證，直到該時間區間結束；期間即使輸入正確憑證也不會通過。來源尚未被阻擋時，沒有附帶帳密的 HTTP Proxy 請求會收到一般的 `407` 驗證要求，不計入失敗次數；已被阻擋的來源或計數已滿時則收到 `429`。HTTP／SOCKS5 Proxy 共用一份計數，管理介面獨立計數。換來源連接埠、IPv4-mapped IPv6 或偽造 forwarded headers 都無法繞過。
- 每份計數最多保留 256 個來源；額滿時拒絕新來源，直到舊項目到期。重啟 daemon 會清除記憶體中的計數。這項措施不能取代 ACL、防火牆或 DDoS 防護。
- HTTP／SOCKS5 的 Proxy 帳密在用戶端與 Proxy 之間是明文，請留在可信任區網／VPN。若需帳密，使用足夠長的隨機密碼。HTTPS CONNECT 加密目的網站流量，沒有加密 Proxy 帳密。
- Linux daemon 使用專屬低權限 `rillway` 帳號；設定與 state 目錄 `0700`，憑證／設定 `0600`。主機管理員或同服務身分的程序仍能讀取，必須保護主機與備份。
- Web UI Token 只保留於瀏覽器記憶體，TUI 預設隱藏。共用電腦請登出／關閉工作階段。惡意擴充功能、本機程序及畫面擷取不在這些措施的防護範圍內。
- 不在觀察資料中保留秘密、HTTPS 解密內容或 payload。持有管理 Token 的人可管理服務並查看主機名稱與配置，因此 Token 應視為完整管理權限。

## 原始碼與發布

[公開發布流程](docs/public-release.zh-Hant.md)包含秘密掃描、Git 歷史／個人資料檢查、Go 漏洞掃描與 Release 來源證明。公開 PR 使用 GitHub 託管的暫時 runner 與唯讀權限，不應接觸家用網路或 VPN 帳號。發布工作另行隔離，使用短效 GitHub Token／OIDC，不放入 VM、NAS、WARP、WireGuard 或 Tailscale 憑證。

Checksum 可檢查檔案是否變更；來源證明可確認 repository 與 workflow。兩者都不能證明程式完全沒有漏洞。維護者仍需審查變更並更新依賴、主機作業系統與官方 WARP client。

若秘密曾進入 Git 或公開產物，先撤銷／輪替，再清理歷史。改寫 Git 紀錄無法收回別人的 clone、快取日誌或下載檔案。

Linux 記憶體控制程序是同一 binary 的獨立 root 程序；Web daemon 維持低權限。
私有 Unix socket 驗證 UID 與主服務 cgroup，限制請求大小與等待時間，只允許
查閱狀態及修改固定服務的 MemoryMax／MemoryHigh，不能執行輸入的指令或修改
其他 unit。root 擁有的數值設定不含憑證；binary 與上層目錄必須由 root 擁有，
且不允許群組或其他人寫入。登入的管理者可以調整資源限制，後續負載超過上限仍
可能終止服務；安全下限與容量檢查不構成可用性保證。
