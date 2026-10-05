# 分流與管理契約

## 資料路徑

```mermaid
flowchart LR
  Mac[Mac 瀏覽器 / 系統 Proxy] --> PAC{PAC}
  PAC -->|公司 / 本機| TS[Mac 原有 Tailscale / LAN]
  PAC -->|公開流量| Proxy[HTTP / CONNECT / SOCKS5]
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

## 可觀察資料

每條已建立連線最多保存 hostname、port、已知目的 IP、family、流量、五秒窗口速率、建連時間、規則及出口。最多 2,048 條 flow 與 2,048 個自適應目的地；超過容量移除最舊資料，24 小時過期。統計不落盤，不記錄 payload、HTTPS path、cookie 或授權 header；畫面總計僅涵蓋保留的 flows。

WARP SOCKS 回應通常無法告知真正遠端 IP，會顯示未知，不使用本機 `127.0.0.1:40000` 假冒目的 IP。DNS、傳輸失敗與實際速度不能單靠 IP 城市推論；診斷只列出可實測的資料與回應提供的 CDN header。

## 管理 API

所有 `/api/v1` 請求需 `Authorization: Bearer <token>`。Web UI 的 token 只保存在該 tab 的 session storage；URL 不帶秘密。跨來源寫入拒絕。TLS 驗證不可關閉；TUI 可提供信任的 PEM 憑證。

| 方法與路徑 | 行為 |
|---|---|
| GET `/api/v1/info` | 已驗證身分後取得 daemon 的程式版本；與設定 revision 各自獨立 |
| GET `/api/v1/config` | 目前生效設定與 revision，秘密為檔案參照 |
| PUT `/api/v1/config` | 完整設定，revision 必須與目前一致，成功後遞增 |
| GET `/api/v1/stats` | flows、destinations、totals、config_revision、applied_at |
| GET `/api/v1/outbounds` | profile 狀態與可驗證的健康資訊 |
| DELETE `/api/v1/outbounds/{id}` | `{"revision":CURRENT,"replacement":"OUTBOUND_ID"}`，刪除 profile 並原子替換所有引用；保護 direct、檢查 revision，成功後遞增 |
| POST `/api/v1/outbounds/{id}/{action}` | `{"value":"..."}`，執行出口支援的動作 |

設定驗證、provider 建立、私有檔案原子寫入完成後才套用。無效更新與寫入失敗保留原設定；409 表示其他介面已更新版本。listener、ACL、TLS／登入安全設定，以及使用同一 state directory 的執行中 Tailscale 變更，需要在本機修改檔案並重啟 daemon。改變出口時會保留舊 provider，直到既有連線結束才釋放。

## Web UI 的設定邊界

- PAC 顯示獨立的完整 `http://host:port/proxy.pac` 網址與複製按鈕；這與 PAC 內的 HTTP Proxy 位址不同。Wildcard listener 以目前管理頁的 hostname 組合網址，IPv6 保留方括號。Loopback listener 不會被偽裝成區網可連的服務。
- Listener、來源限制、管理認證與完整 JSON 為唯讀；在 Server 修改設定後重新啟動。分流、出口、自適應與 PAC 規則使用各自的表單更新。Docker 匯出不會遠端修改另一台主機。
- `PUT /api/v1/outbounds` 接受 `revision`、`create`、`outbound`，以及選用的 `wireguard_config` 或 `tailscale_auth_key`。秘密只寫入私有檔案，不進入回應或可攜設定內容。新建模式拒絕重複名稱；驗證、同源與認證檢查、revision 衝突均在寫檔之前執行。Runtime 套用衝突／失敗會清除新檔。原始 provider 錯誤保持遮罩。
- 上傳的設定檔不會執行 Shell hooks，也不能指定檔案寫入位置。狀態目錄仍在使用中的既有 Tailscale，其節點身分與 DNS 欄位顯示唯讀及重啟說明，避免開啟第二個 state owner；啟停和登入操作仍透過既有 API。

- `POST /api/v1/service/restart` 使用相同的 Bearer、來源及同源保護。先驗證儲存設定、憑證及新的監聽位址，HTTP `202` 回應 flush 後才觸發重啟。所有 Proxy 連線關閉，Runtime／listeners 完成清理並重新開啟，Tailscale state owner 先釋放後建立。重啟在原本的低權限 daemon 程序內完成，不執行 shell／sudo／service-manager 指令，也不載入新 binary。Web UI 需要確認操作，最多等待 30 秒並顯示恢復／新位址登入提示。
