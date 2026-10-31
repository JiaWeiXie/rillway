# 主機本機服務的存取

[English](host-local.md) · [繁體中文](host-local.zh-Hant.md)

主機本機目的地包括 loopback、未指定位址、link-local、Rillway 啟動時取得的主機介面位址、`localhost` 及其子網域，以及下列指定的 metadata 位址。用戶端來源是傳入連線的 socket 對端；轉送標頭不會授予權限。**本機用戶端只指 loopback 對端**，包含正規化後的 IPv4-mapped loopback。主機介面、Docker bridge 或 overlay 來源在這項政策中都視為外部來源。

| 模式 | 一般主機本機服務 |
| --- | --- |
| `open` | 允許，但仍受 baseline 與自身監聽端保護限制 |
| `local-clients-only` | 允許 loopback 對端；其他來源須符合例外規則 |
| `strict` | 所有用戶端都須符合例外規則 |

新安裝會明確寫入 `local-clients-only`。省略模式或填入空字串時，也使用 `local-clients-only`，**既有設定升級後立即適用**。讀取設定不會改寫檔案或重設其他設定。啟動提示顯示生效模式，不會宣稱偵測到升級。

## 例外與 baseline

請將以下物件合併至完整設定，保留其他設定與目前的 revision。範例位址僅供文件使用，代表允許指定用戶端存取 Proxy 主機上的特定服務：

```json
"host_local": {
  "mode": "local-clients-only",
  "allow": [
    { "to": "127.0.0.1:3000", "from": ["192.0.2.30/32"] },
    { "to": "[::1]:8080-8082", "from": ["2001:db8::30/128"] }
  ]
}
```

`to` 必須是 IP 字面位址與連接埠，或包含兩端的連接埠範圍；不接受主機名稱、目的網段或萬用字元。IPv4-mapped 位址與 IPv6 zone 在比對時會正規化。`from` 使用來源 CIDR；省略時適用於所有用戶端，明確指定空清單則無效。重疊例外以聯集處理。最多 256 條規則、合計 1,024 個來源 CIDR。驗證會提示未限定來源、連接埠範圍與 baseline 例外。例外不會繞過另一層來源 ACL。

**所有模式**下，下列目的地都須符合例外規則：

- IPv4 link-local `169.254.0.0/16`、IPv6 link-local `fe80::/10`；
- link-local multicast `224.0.0.0/24`、`ff02::/16`；
- metadata 端點 `fd00:ec2::254`、`192.0.0.192`、`100.100.100.200`。

任何模式下，都禁止透過 Proxy 存取 Rillway 自己的 HTTP／SOCKS5、管理、PAC 與使用中的 UDP relay 監聽端，即使加入例外也一樣。這些端點直接接受連線時，仍使用自己的驗證與 ACL。請透過瀏覽器 Proxy 例外清單直接開啟管理介面與 PAC。

## DNS 與出口限制

DNS 仍由所選出口負責；路由選擇與 `Explain` 不會解析名稱。direct 會在**連線前**檢查每個實際的數字位址候選，包含重新解析的別名與 adaptive probe。direct UDP 快取保留已驗證的目的 IP，每個 datagram 寫入前都重新檢查目前政策。

IP 例外不會假定 `localhost` 就是 `127.0.0.1`；IPv4 與 IPv6 候選各自都須符合例外。若由遠端解析名稱的出口，無法驗證 `localhost` 所需的 IP 例外，會拒絕連線，並以固定訊息說明需要可驗證的目的 IP。模式與來源原本允許的一般主機本機服務仍依其權限處理，但出口原有、更嚴格的私網路由限制優先，不能透過 fallback 繞過。

**一般遠端解析名稱的保護僅涵蓋目的地字面值**，不會從出口的傳輸對端推測 DNS 結果。例如，WARP 的本機 Proxy socket 並不是網站的目的 IP。`Explain` 會將尚須 direct 解析的名稱標示為 `pending_direct_resolution`；這不代表允許存取、DNS 成功或網路可達。

## 套用、切回 open 與升降版

使用原有的 revision 檢查流程套用設定，或修改服務實際使用的設定檔後重新啟動。只修改檔案不會立即載入。Linux 通常使用 `/etc/rillway/config.json`，舊安裝可能使用 `/var/lib/rillway/config.json`；請確認服務實際設定的路徑。

若要恢復一般主機本機服務原本的開放行為，只需將模式改為 `"host_local": {"mode": "open"}`，保留想繼續使用的 `allow` 規則與其他設定，再依正常程序套用。baseline 與自身監聽端保護仍然有效。若要**降回 v0.9.1**，須先移除整個 `host_local` 物件；只改為 `open`，不會讓舊程式認得新欄位。升級前請私下備份設定與 state。升降版不應重新產生憑證或出口 state。

政策發布的契約是：後續檢查使用新快照，已授權的傳送仍可完成。Apply 不是同步屏障。

Apply 不會中斷既有 TCP 串流。後續 UDP 傳送會依目前政策檢查，新政策禁止的快取目的地會關閉。發布前已授權的寫入仍可在發布後完成。重新啟動會關閉串流，並重設記憶體內的計數。

未指定模式的既有安裝在升級後立即受到保護；外部用戶端若透過 Proxy 存取主機本機服務，可能被拒絕。請為合法流量新增限定來源的例外，或依上述方式將模式改為 `open`。這是在說明升級行為，不代表某個實際安裝已升級或通過驗證。

## 離線驗證與觀察資料

```sh
rillway agent validate --input candidate.json
```

沿用原本的 JSON envelope、驗證錯誤與退出碼語意。`data.host_local` 顯示 `effective_mode`、受保護的 `baseline`、自身監聽端保護，以及 `rollback_field: "host_local.mode"`／`rollback_value: "open"`。`runtime_verified` 維持 `false`：驗證不會連線出口、讀取憑證、檢查即時介面或回報即時流量。Phase 1 不新增 `doctor`、setup 旗標、模式編輯器、Web UI 卡片或 TUI 控制。

需要驗證身分的 `rillway agent stats`／`GET /api/v1/stats` 包含 `host_local`：

- `actual_denials`：實際因政策被拒絕的請求／datagram；
- `hypothetical_local_clients_only_denials`：在**使用相同例外規則**的 `local-clients-only` 下，已觀察的政策決定中會被拒絕的次數；
- `external_host_local_by_port`：外部來源存取主機本機服務的連接埠分項，最多 128 個；其他不同連接埠彙總至 `port_overflow`；
- `recent`：最近八筆實際或假設拒絕，目的地已驗證時會附數字位址。這是管理員觀察資料，不是公開錯誤內容。

實際與假設計數彼此獨立，同一事件可以同時計入兩者，**不要相加**。在 `open` 下，假設拒絕不代表真的拒絕。符合例外的流量也不會計入假設拒絕。每個傳入請求或已接受的 datagram 最多計一次，不會按 DNS 候選、位元組、預覽或 probe 重複計算。多個 DNS 候選中有成功連線時，使用最後驗證的目的地；若只有一般 DNS／傳輸失敗，不會因另一候選被擋就當成實際拒絕。所有實際候選都被拒絕時，只要有候選在假設政策下符合存取條件，就不計入假設拒絕；回呼順序不影響計數。沒有最後確認的身分時，最近記錄的目的地是其中一個已觀察到的拒絕候選，不一定是 dialer 最後回傳錯誤所指的目的地。未知的遠端 DNS 結果維持未知。計數有容量上限、只存記憶體，重啟後歸零；既有管理與 UDP 計數仍保有各自語意。

HTTP 可顯示不含來源或內部目的地的固定政策錯誤。SOCKS5 可能只有協定錯誤；UDP 被丟棄時可能看起來像逾時。不能保證每個用戶端都顯示相同的文字訊息。

## 剩餘限制

主機上的轉送程式可能讓外部用戶端看起來像 loopback 對端；有此需要時可使用 `strict` 與限定來源的例外。`strict` 不是共用 VM 的安全邊界：本機程序不經 Rillway 也能直接連線。政策不會封鎖所有容器或 overlay 網段。主機介面目的地清單是在啟動時取得；相關位址變更後請重新啟動。PAC bypass 是路由便利功能，不是授權。
