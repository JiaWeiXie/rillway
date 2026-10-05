# VPN 出口設定與驗證

Rillway 的分流只作用於送進 HTTP／SOCKS5 Proxy 的 TCP 連線。每個出口自行建立連線；指定出口失敗時回報錯誤，不改用直連。變更規則只影響新連線，手動斷開 VPN 則會影響正在使用該 VPN 的流量。

直接連線使用宿主 resolver，WARP 使用官方 Proxy 的 resolver；這兩類出口不接受 Rillway 的 `dns` 覆寫。`dns` 設定只用於 WireGuard 與 Tailscale，填在其他出口會明確拒絕。

## Cloudflare WARP 與 WARP+

先依 [Cloudflare 官方 Linux 套件來源](https://pkg.cloudflareclient.com/) 安裝 `cloudflare-warp`；官方目前列出 Ubuntu 24.04、26.04。macOS 使用 [官方安裝程式](https://developers.cloudflare.com/warp-client/get-started/macos/)。Rillway 不附帶、下載或自行安裝 Cloudflare daemon。

Rillway 操作的是同一台機器上的官方 WARP client。開啟 Rillway 的「連線」會改變該 client 的模式；若這台機器已有其他程式使用 WARP，請先安排好用途。Mac 使用 Ubuntu Proxy 的部署方式只需要在 Ubuntu 安裝 WARP。

1. 在 Web UI「出口與 VPN」編輯 `warp`，啟用出口，保留 `127.0.0.1:40000`；儲存本身不會切換 WARP 模式或自動連線。
2. 沒有註冊過裝置時，按「註冊」。既有註冊直接沿用；重複請求只檢查既有裝置，不重新產生註冊或清除 WARP+ 授權。Web UI 取得註冊狀態後顯示「已註冊」並停用該按鈕。
3. 有 WARP+ 訂閱時，按「WARP+ 授權」輸入 license key。輸入框會遮罩，送出後清空，不保存至 Rillway 設定或日誌。
4. 按「連線」，再按「驗證出口」。前者必須先成功切換 Local Proxy 模式；後者透過代理讀取 Cloudflare trace，確認真的經過 WARP。

TUI 的出口頁也提供註冊、連線、斷線、驗證及 WARP+ 授權輸入。WARP+ 訂閱仍由 Cloudflare 的行動版 App 購買；license 使用方式與 `Account type: Unlimited` 判斷依 [Cloudflare Linux 文件](https://developers.cloudflare.com/warp-client/get-started/linux/)。

Rillway 使用的官方命令如下；這些是說明用，專案初始化、一般測試、開啟 daemon 不會自行執行它們：

```text
warp-cli --accept-tos registration new
warp-cli --accept-tos registration license <license-key>
warp-cli --accept-tos mode proxy
warp-cli --accept-tos proxy port 40000
warp-cli --accept-tos connect
warp-cli --accept-tos disconnect
```

授權建議透過 UI 輸入，避免將實際 key 存進 shell history。授權時官方 CLI 需要透過程序參數接收 key；Rillway 不會回傳 CLI 原始註冊輸出。

狀態欄位分開呈現：

| 欄位 | 能證明的事情 |
| --- | --- |
| `version` | 目前可呼叫的官方 client 版本 |
| `mode` | 官方設定回報的模式，預期為 `proxy` |
| `state` | 官方 daemon 回報的連線狀態 |
| `listener` | 本機 SOCKS5 連接埠可以建立 TCP 連線 |
| `account` | 官方註冊資訊是否明確回報 `Unlimited` |
| `verified_at` | 上次經代理取得 `warp=on` 或 `warp=plus` 的時間 |

listener 存在、帳號為 Unlimited，都不能單獨證明 tunnel 正常。`verified_at` 是過去一次驗證成功時間，並非持續健康保證，也不是下載加速證明。狀態每五秒最多讀取一次，避免 UI 輪詢反覆啟動 CLI。

Local Proxy 不支援目前帳號／版本時，Rillway 回報錯誤，不改成全機 tunnel，也不改用非官方 WARP 註冊服務。官方 WARP daemon 負責自身連線維持；Rillway 不在背景重複註冊或重新連線，手動斷線後也不會重啟它。

SOCKS5 以網域發起連線時，由 WARP 解析 DNS，Rillway 不會猜測目的 IP。SOCKS5 協定無法同時指定網域與強制 IP 類型，因此 WARP 的網域規則請使用 `auto`；`ipv4`／`ipv6` 只有在目的地是相符的 literal IP 時可用。使用者傳入網址 hostname 的連線，目的 IP 顯示為未知。

## Tailscale

Rillway 內嵌 [tsnet](https://tailscale.com/docs/features/tsnet)，建立獨立節點，不控制宿主現有的 Tailscale App／daemon。

在 Web UI 新增 `tailscale` 出口時，只需設定節點名稱，程式會安排專用的私有 `state_dir`，並關閉「公網出口」。授權金鑰可直接填入選用的密碼欄位，或留空使用瀏覽器登入，不需要先在 Server 建立 auth key 檔案。狀態目錄仍在使用中的既有節點，其身分、auth key、DNS 與狀態目錄在 Web UI 為唯讀；需在 Server 修改設定後重啟，避免同時開啟同一份 state。使用設定檔／TUI 管理時，仍支援既有的檔案參照。State directory 必須為權限 `0700`；選填 `auth_key_file` 必須為 `0600`。沒有 auth key 時從 Web UI 的登入連結授權該節點；tailnet 的 ACL、裝置核准及 subnet route 核准仍由 Tailscale 管理。

```json
{
  "id": "office",
  "type": "tailscale",
  "enabled": true,
  "public_internet": false,
  "state_dir": "/var/lib/rillway/outbounds/office",
  "hostname": "rillway-office",
  "dns": ["100.100.20.1"]
}
```

以上 DNS 是格式示例，請替換成公司實際 resolver，或刪除 `dns` 只使用 MagicDNS。不要把宿主現有 Tailscale 的 state directory 指給 Rillway。

- MagicDNS 使用該節點的 Tailscale DNS；簡短機器名會補上目前 tailnet suffix。
- 其他公司網域必須設定 `dns`，而 resolver IP 本身必須能經過已生效的 tailnet／subnet route。查詢以 TCP port 53 在 tunnel 內進行；resolver 必須支援 TCP DNS。
- 解析失敗不轉送公共 resolver。解析出的 IP 也必須符合目前 peer 或已核准的 subnet route，才呼叫 tsnet 建連。
- 此版不支援 Tailscale exit node，不允許 Tailscale 當自適應公網出口；`public_internet: true` 會被拒絕。
- 同一個 `state_dir` 不能同時由 daemon 與另一個測試／程序開啟。Rillway 在啟動節點前取得 `.rillway.lock` 排他鎖，持有到節點關閉；另一個 Rillway 程序會收到「狀態目錄已在使用中」的錯誤。程序結束後由系統釋放鎖，留下鎖檔是正常的，請勿在節點運行時刪除它。此鎖只協調 Rillway 程序，其他 tsnet 應用也應使用自己的目錄。修改節點身分或重用既有 state 的設定時，先安排重新啟動。
- 「登出」或「斷線」後可按「登入」重新授權；成功開始登入流程後，Web UI 會顯示登入連結。若登入操作失敗，出口仍維持停止狀態。

Mac 的 PAC bypass 公司網域／IP 時，流量走 Mac 原有 Tailscale，不會進入 Rillway 的觀察畫面。只有刻意把某條規則送往 Ubuntu 上的 `office` 出口時，才會使用上述獨立節點。

## WireGuard

WireGuard 的裝置與網路堆疊都內嵌 binary，不執行 `wg-quick`，也不建立宿主 TUN 或修改宿主路由。在 Web UI 選擇 WireGuard 後，可從目前裝置匯入 `.conf` 或直接貼上內容，最多 1 MiB。設定會先以同一個受限解析器驗證，再以 `0600` 儲存到 Server 的專用 `0700` 資料夾，API 與重新開啟的表單只顯示檔案參照，不回傳私鑰。編輯時留空保留既有設定；貼上新內容會建立新的私有檔案，儲存或套用失敗會刪除新檔案並保留原設定。替換或刪除出口不會刪除舊憑證檔，Server 管理者可在確認不再使用後清理。使用設定檔／TUI 的既有檔案參照仍受 `0600` 權限限制。

```ini
[Interface]
PrivateKey = <32-byte-base64-private-key>
Address = 10.77.0.2/24, fd77::2/64
DNS = 10.77.0.1
MTU = 1420

[Peer]
PublicKey = <32-byte-base64-peer-public-key>
Endpoint = vpn.example.net:51820
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
```

這是欄位示例，佔位文字不是可用金鑰。私鑰、公鑰、位址與 endpoint 由你的 WireGuard 提供者設定。

| 區段 | 接受的欄位 |
| --- | --- |
| Interface | `PrivateKey`、`Address`、`DNS`、`MTU`、`ListenPort` |
| Peer，可多個 | `PublicKey`、`PresharedKey`、`Endpoint`、`AllowedIPs`、`PersistentKeepalive` |

`PostUp`、`PostDown`、`PreUp`、`PreDown`、`SaveConfig`、`Table` 與未知欄位都拒絕。地址使用 CIDR；DNS 僅接受 IP，不接受 search domain，且必須位於某個 peer 的 AllowedIPs 內。MTU 範圍為 1280–65535。

只有 tunnel endpoint 的初始解析使用宿主 DNS。目的網站的 DNS 透過 userspace tunnel 內的 resolver；未設 DNS 時只接受 literal IP，不回退到宿主 DNS。Outbound 的 `dns` 可覆寫設定檔 DNS，仍受相同路由限制。

`public_internet` 是使用者對此 profile 的用途宣告，不代表公網連線已驗證。要用於自適應，先確認 peer 的 AllowedIPs、伺服器轉送／NAT 與 DNS 能連到公網。私網 profile 保持 `false` 並使用固定規則。

## 本機測試與真實環境驗收

一般測試不使用帳號，包含 SOCKS5 遠端 DNS、WARP 命令順序與秘密遮罩、tailnet 路由限制、設定解析，以及兩個本機 userspace WireGuard 裝置之間的真實加密 TCP 連線。WireGuard 解析另有 bounded fuzz 測試。

真實環境測試只在明確提供設定檔路徑時運作：

```sh
RILLWAY_LIVE_CONFIG=/absolute/path/to/config.json mise run test:live

# 只測一個出口；私網出口需要指定可連線的目的地。
RILLWAY_LIVE_CONFIG=/absolute/path/to/config.json \
RILLWAY_LIVE_OUTBOUND=office \
RILLWAY_LIVE_TARGET=server.example-tailnet.ts.net:443 \
mise run test:live
```

不設 `RILLWAY_LIVE_CONFIG` 時顯示 `NOT VERIFIED` 與 `SKIP`，不會啟動 VPN。此測試不執行 WARP 註冊、授權、連線或模式變更，必須事先完成 Local Proxy 設定。Tailscale 要有專用且已登入的 state directory，測試會開啟及更新這個 embedded node 的 state；請先停止使用相同 state 的 Rillway daemon。測試不從 auth key 建立新節點。

WARP 測試先確認 Local Proxy 與端到端 trace；`warp-plus` 子測試還必須取得真實 `Unlimited` 帳號狀態。只有免費 WARP 時，WARP+ 子測試明確跳過，不能視為付費功能已驗證。

WARP client 的版本、帳號狀態、連線協定及 Cloudflare colo 都可能改變。Rillway 會解析官方 Local Proxy 模式，並以端到端 trace 區分免費 WARP 與 WARP+；正式結果應保存在 repository 之外的受限制營運紀錄。缺少帳號、tailnet 或 WireGuard profile 時，一律標示 `NOT VERIFIED`，不得沿用其他環境的結果。

所有出口目前只支援 TCP。此版不提供 UDP ASSOCIATE、整機流量接管、HTTPS 解密，或「低下載速度就保證換到更快線路」；自適應比較成功率與建連時間。
