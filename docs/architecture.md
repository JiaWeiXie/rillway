# 分流與管理契約

## 資料路徑

```mermaid
flowchart LR
  Mac[Mac 瀏覽器 / 系統 Proxy] --> PAC{PAC}
  PAC -->|公司 / 本機| TS[Mac 原有 Tailscale / LAN]
  PAC -->|公開流量| Guard{Ingress Guard (IP ACL & 限流)}
  Guard --> Proxy[HTTP / CONNECT / SOCKS5]
  Proxy --> Engine[規則與自適應]
  Engine --> Direct[Ubuntu 直連]
  Engine --> WARP[官方 WARP Local Proxy]
  Engine --> WG[內嵌 WireGuard]
  Engine --> Tail[內嵌 tsnet]
  Engine --> Stats[有容量上限的記憶體統計]
  Stats --> API[HTTPS 管理 API]
  API --> Web[內嵌 Web UI]
  API --> TUI[Bubble Tea TUI]
```

## 規則

`direct` 是內建且預設使用的出口，必須保持 ID／type 為 `direct`、啟用及具公網能力；不可刪除、改名、改成其他類型或停用。介面不提供修改／刪除按鈕，設定驗證也會拒絕違反此契約的 JSON。使用者仍可把「沒有規則匹配時」的預設路由改為其他已啟用出口。

刪除其他出口前，Web UI 會列出引用它的規則、預設路由與自適應設定，要求明確選擇已啟用的替代出口。確認後原子刪除並替換所有引用、去除候選重複項，保留規則 ID／匹配條件／順序；不默默刪規則或轉成 direct。替換自適應候選時，替代出口需具公網能力且不可為 Tailscale。只有完全沒有引用時才可省略替代出口。取消、失敗與 revision 衝突均保留原設定，既有連線繼續使用原 provider。移除 profile 不會取消官方 WARP 註冊或刪除 VPN 憑證。

網域先轉小寫並移除結尾句點。`domains` 精確比對；`suffixes` 同時匹配本身與子網域，`example.com` 不會匹配 `notexample.com`。同類固定規則依設定順序，固定規則始終優先於自適應規則。要讓既有固定網域改用自適應，需移除／修改該固定規則。

CIDR 僅匹配 Proxy 客戶端給的 IP literal。系統不會先用 Ubuntu 的 DNS 解析 hostname 再套 CIDR，避免在選定出口前洩漏名稱。網域流量請使用網域規則；要求 `ipv4`、`ipv6` 或 `auto` 會傳入該出口。WARP 的遠端 SOCKS DNS 無法保證 hostname 強制 family，會回報不支援。

預設 `github.com`、`api.github.com`、`codeload.github.com` 直連；`raw.githubusercontent.com`、`avatars.githubusercontent.com`、`release-assets.githubusercontent.com`、`objects.githubusercontent.com` 與 `githubassets.com` suffix 固定 WARP。這是可編輯的起點，不宣稱 GitHub 所有 CDN 名稱都已列完。

解析由出口擁有：direct 使用系統 resolver；WARP 將 hostname 交給上游 SOCKS；WireGuard 用設定內的隧道 DNS；Tailscale 用其 MagicDNS 或明確配置的公司 DNS。不設跨出口共享的 DNS cache；底層 resolver 的快取不會被拿來為另一個出口選路。私有名稱解析失敗不改用公共 resolver。私網與公司 bypass 名稱不得經 WARP，也不參與自適應；若明確建立固定規則，可使用公司 WireGuard，即使該出口同時具備公網能力。預設公網 WireGuard 不會自行接手私網目的地。

## 自適應

預設關閉。選擇具公網能力的 direct、WARP、WireGuard；不使用 Tailscale 公司出口。每個 `host:port` 和 requested family 分開決策；`auto` 比較整段建連路徑，不把未知 WARP IP 宣稱成 IPv4 或 IPv6。連線快不代表傳輸吞吐快。

預設十分鐘窗口、至少三個成功樣本，候選連續兩輪比目前出口快至少 25% **且** 50 ms，且滿十分鐘冷卻才切換。連續三次逾時、有近期成功的替代出口時可提前切換。相同的舊證據不算新一輪改善。

探測只對近期使用的公開 80／443 目的地做 TCP connect，最多每分鐘十二次、同時兩次、單次四秒；不下載測速檔、不重播 HTTP 請求。被手動停用／停止的出口不會因探測重新連線。新決策只影響新連線，既有下載持續使用原出口。

學到的出口只存在記憶體，重新啟動後清除。套用設定時，目的地的規則、family、初始出口（預設出口或自適應規則指定的出口；不在候選中時為第一個候選）、候選順序及候選出口的 provider 都沒有改變，才保留原本學到的出口；任何一項改變都從設定的初始出口重新開始。這避免無關的設定修改讓網站的新連線換到另一個出口 IP。

## 可觀察資料

每條已建立連線最多保存 hostname、port、已知目的 IP、family、流量、五秒窗口速率、建連時間、規則及出口。最多 2,048 條 flow 與 2,048 個自適應目的地；超過容量移除最舊資料，24 小時過期。統計不落盤，不記錄 payload、HTTPS path、cookie 或授權 header；畫面總計僅涵蓋保留的 flows。

自身管理介面／PAC 的連線不建立 flow 或自適應樣本，HTTP／SOCKS5 的自身代理目的地則拒絕連線。判斷使用實際綁定的 IP／連接埠，wildcard 僅涵蓋已知本機位址；同一主機上其他經 Proxy 連線的服務仍會被記錄。已知的管理／PAC IP 直接在本機建連；別名只依所選出口連線結果判斷，不額外查詢主機 DNS。新自適應目的地在解析確認後才公開給探測排程，已排程的探測若連到自身也丟棄樣本。PAC 在用戶端自動 bypass 已設定的 Proxy 主機。

WARP SOCKS 回應通常無法告知真正遠端 IP，會顯示未知，不使用本機 `127.0.0.1:40000` 假冒目的 IP。DNS、傳輸失敗與實際速度不能單靠 IP 城市推論；診斷只列出可實測的資料與回應提供的 CDN header。

## 來源存取控制

Proxy（HTTP／CONNECT 與 SOCKS5）在 TCP socket accept 後、通訊協定解析前進行 pre-parser 入口審查。管理介面與 PAC 則獨立使用各自的授權與監聽邏輯，不經由 Proxy 連線守衛。

- **雙軌 ACL 與設定版本**：`config.json` 版本為 2。`security.allowed_clients` 維持唯讀，作為 HTTPS 管理介面與 HTTP PAC 端點存取的網路邊界；`source_access.rules` 則專門用於 Proxy 連入存取。若版本 2 的 `source_access.rules` 為空，表示明確全域拒絕（deny-all），不再回退至預設或 `security.allowed_clients`。
- **規則匹配語意**：規則採用前綴二元樹（Trie）評估。各規則定義動作（`allow` 或 `deny`）、啟用狀態與 CIDR 前綴列表。當有多條規則覆蓋同一個 IP 時，以最長前綴（longest/deepest prefix）優先；若在前綴長度完全相同時發生衝突，則以拒絕優先（deny beats allow）；若前綴長度相同且動作一致，則由設定順序中較早啟用的規則決定 `rule_id`。未匹配任何規則的連線預設拒絕（fail closed）。
- **連線容量與限流防護**：全域最多維持 256 條活躍 Proxy 連線，單一來源 IP 最多維持 64 條連線。超出上限之連線會立即關閉，並將統計計入 `proxy_admission_rejections`。
- **連線遙測與即時阻斷**：系統在記憶體中維護最多 1,024 筆來源遙測，超過容量時以 LRU 清理，資料在 24 小時無活躍後過期。常規規則編輯（`source_access.rules`）僅影響新連線准入，既有連線不受干擾保持存活；而明確的阻斷來源（Block source）操作具備交易原子性：驗證 revision、附加 /32 或 /128 deny 規則、編譯 ACL、原子存檔，並立即切斷該 IP 所有現存活躍 Proxy 連線（不影響其他來源或管理介面），失敗時保證不中斷任何既有連線。
- **封鎖輸入與既有規則**：只接受單一 IP，拒絕 CIDR 與 `IP:port`；revision 衝突優先於內容驗證。既有停用規則及備註保持原樣；新增 host deny 規則的 ID 若已使用，依序加上 `-2`、`-3`，不覆寫既有規則。
- **來源表單與重新載入**：Web 與 TUI 都可編輯 ID、名稱、動作、CIDR、啟用狀態及備註。409／422 保留草稿與原 revision；Web 表單的「重新載入設定」明確重新依 rule ID 載入，避免排序變動造成誤改。TUI 表單開啟期間不以背景快照取代來源資料；窄終端機以 grapheme-aware 來源卡片顯示。

## 管理 API

所有 `/api/v1` 請求需 `Authorization: Bearer <token>`。Web UI 的 token 只保存在該 tab 的 session storage；URL 不帶秘密。跨來源寫入拒絕。TLS 驗證不可關閉；TUI 可提供信任的 PEM 憑證。

| 方法與路徑 | 行為 |
|---|---|
| GET `/api/v1/info` | 已驗證身分後取得 daemon 的程式版本；與設定 revision 各自獨立 |
| GET `/api/v1/config` | 目前生效設定與 revision，秘密為檔案參照 |
| PUT `/api/v1/config` | 完整設定，revision 必須與目前一致，成功後遞增 |
| GET `/api/v1/stats` | flows、destinations、totals、config_revision、applied_at；`proxy_admission_rejections` 的 `source_limit`／`total_limit` 是 listener 啟動後 HTTP／SOCKS5 因容量上限直接關閉的連線數 |
| GET `/api/v1/outbounds` | profile 狀態與可驗證的健康資訊 |
| DELETE `/api/v1/outbounds/{id}` | `{"revision":CURRENT,"replacement":"OUTBOUND_ID"}`，刪除 profile 並原子替換所有引用；保護 direct、檢查 revision，成功後遞增 |
| POST `/api/v1/outbounds/{id}/{action}` | `{"value":"..."}`，執行出口支援的動作 |
| GET `/api/v1/source-clients` | 最近活躍的 Proxy 客戶端遙測快照（上限 1,024 筆，24 小時過期） |
| POST `/api/v1/source-clients/block` | `{"revision":CURRENT,"address":"IP"}`，原子附加 /32 或 /128 deny 規則、持久化設定並切斷該來源的現存 Proxy 連線 |

設定驗證、provider 建立、私有檔案原子寫入完成後才套用。無效更新與寫入失敗保留原設定；409 表示其他介面已更新版本。來源存取規則（`source_access.rules`）支援即時更新與 revision 保護，套用後即時影響新連線的准入決策，既有連線保持存活。僅 listener 監聽位址、管理認證與 TLS 安全設定（`security`），以及使用同一 state directory 的執行中 Tailscale 變更，需要在本機修改檔案並重啟 daemon。改變出口時會保留舊 provider，直到既有連線結束才釋放。

## Web UI 的設定邊界

- PAC 顯示獨立的完整 `http://host:port/proxy.pac` 網址與複製按鈕；這與 PAC 內的 HTTP Proxy 位址不同。Wildcard listener 以目前管理頁的 hostname 組合網址，IPv6 保留方括號。Loopback listener 不會被偽裝成區網可連的服務。
- Listener、管理認證與完整 JSON 在 Web UI 為唯讀；在 Server 修改設定後重新啟動。分流、出口、自適應、來源存取與 PAC 規則使用各自的介面即時更新。Docker 匯出不會遠端修改另一台主機。
- `PUT /api/v1/outbounds` 接受 `revision`、`create`、`outbound`，以及選用的 `wireguard_config` 或 `tailscale_auth_key`。秘密只寫入私有檔案，不進入回應或可攜設定內容。新建模式拒絕重複名稱；驗證、同源與認證檢查、revision 衝突均在寫檔之前執行。Runtime 套用衝突／失敗會清除新檔。原始 provider 錯誤保持遮罩。
- 上傳的設定檔不會執行 Shell hooks，也不能指定檔案寫入位置。狀態目錄仍在使用中的既有 Tailscale，其節點身分與 DNS 欄位顯示唯讀及重啟說明，避免開啟第二個 state owner；啟停和登入操作仍透過既有 API。

- `POST /api/v1/service/restart` 使用相同的 Bearer、來源及同源保護。先驗證儲存設定、憑證及新的監聽位址，HTTP `202` 回應 flush 後才觸發重啟。所有 Proxy 連線關閉，Runtime／listeners 完成清理並重新開啟，Tailscale state owner 先釋放後建立。重啟在原本的低權限 daemon 程序內完成，不執行 shell／sudo／service-manager 指令，也不載入新 binary。Web UI 需要確認操作，最多等待 30 秒並顯示恢復／新位址登入提示。

## Service memory control

`GET/PUT /api/v1/service/memory` is authenticated and uses a separate opaque edit
revision. A Linux socket-activated helper in the same binary serializes bounded
local JSON requests and invokes only fixed `systemctl show/set-property` commands
for `rillway.service` MemoryMax/MemoryHigh. The Unix socket is root:rillway 0660;
SO_PEERCRED requires root or the service UID inside the exact active service
cgroup. The network-facing daemon remains unprivileged. Root-owned metadata
contains numeric budgets and requested units only; no management/VPN credentials.
Settings persist without service restart. The numeric settings and unit drop-in are stored in one atomic, root-owned
`memory-limit.conf`, referenced by the final `zzzz-rillway-memory.conf` drop-in.
A valid empty initial file changes no existing limits. Persistence failure
prevents a live mutation; reload/property failures restore the previous file and
properties and return sanitized errors.
An ambiguous result must be read back before retrying. The helper itself has a
64 MiB cap, CPU/task limits, no network address families and exits after idle.
Policy and platform limits are documented in the bilingual CLI references.
