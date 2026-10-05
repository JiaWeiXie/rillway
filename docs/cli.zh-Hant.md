# Rillway CLI 操作說明

[English](cli.md) · [繁體中文](cli.zh-Hant.md) · [README](../README.zh-Hant.md)

非互動 JSON 指令、預覽、錯誤狀態與公開操作 Skill 見 [Agent CLI](agent-cli.zh-Hant.md)。請用 `rillway agent schema` 查詢指令。

下列 `rillway` 是 Linux 安裝後的入口；尚未安裝時使用 `./rillway`，開發使用 `./bin/rillway`。選項放在指令及子指令後方；全域 `--lang` 可放前方或後方。`rillway help` 列出指令，`rillway COMMAND --help` 列出選項；子指令使用 `rillway service install --help`、`rillway docker export --help`。**不帶參數會開啟 TUI，不會自動進入安裝引導。**

## 語言、路徑、輸出與結束狀態

```sh
rillway --lang zh-Hant help
RILLWAY_LANG=zh-Hant rillway setup
rillway tui --lang en --config .local/config.json
```

`--lang en|zh-Hant` 優先於 `RILLWAY_LANG`，預設英文。Web UI 語言獨立選擇。TUI 按 `L`／`Ctrl+L` 切換，輸入文字時使用 `Ctrl+L`。使用者資料與未知的上游訊息維持原文。Web 字體內建；終端機缺字時，請自行選擇支援中文／Emoji 的終端字體，工具不會安裝主機字體。

Linux 優先採用 Rillway 安裝程式產生的 `/etc/systemd/system/rillway.service` 所指定的設定，避免未使用的預設檔蓋過正式服務。找不到支援的 unit 時，依序搜尋既有 `/etc/rillway/config.json`、舊安裝的 `/var/lib/rillway/config.json`；都不存在時使用 `$XDG_CONFIG_HOME/rillway/config.json`，通常為 `~/.config/rillway/config.json`。macOS 使用 `~/Library/Application Support/Rillway/config.json`；大小寫有區別的檔案系統若只有舊 `rillway/config.json`，仍會沿用。`--config FILE` 可明確覆寫，相對路徑以目前目錄解析。自行使用 wrapper 或 systemd drop-in 覆寫設定路徑時，請明確指定 `--config FILE`。Linux 正式設定為私有檔案，本機管理請用 `sudo`。不要讓第二個 daemon 占用相同 listener 或 Tailscale state。遠端 TUI 使用獨立的連線紀錄，不會修改伺服器設定檔。

成功與 help 的結束碼為 `0`；錯誤選項、安裝輸入 EOF、驗證或操作失敗為 `1`。最後確認輸入非 `yes` 時會取消，結束碼 `0`，不寫檔。訊號會取消執行；`serve`／TUI 持續執行直到停止。一般輸出走 stdout，最後 CLI 錯誤走 stderr。`pac`、`docker export`、`diagnose`、`licenses` 可重新導向輸出。憑證從檔案讀取，setup／init 只印出權杖路徑。

## `setup`：引導首次安裝

```sh
./rillway --lang zh-Hant setup
./rillway setup --yes --listen 192.0.2.20 --allow-client 192.0.2.30 \
  --bypass-domains local,ts.net,tailscale.com,corp.example
./rillway setup --no-install --config .local/config.json
```

| 選項 | 預設 | 用途 |
| --- | --- | --- |
| `--config FILE` | 使用者設定路徑 | 新的暫存設定檔，安裝後仍保留 |
| `--listen IP` | `127.0.0.1` | 四個 listener 的指定本機 IPv4／IPv6 |
| `--allow-client CSV` | 選定的監聽 IP | 允許的來源 IP／CIDR，以逗號分隔 |
| `--http-port PORT` | `17890` | HTTP forwarding／HTTPS CONNECT |
| `--socks-port PORT` | `17891` | SOCKS5 TCP |
| `--admin-port PORT` | `17892` | HTTPS 管理介面 |
| `--pac-port PORT` | `17893` | HTTP PAC |
| `--bypass-domains CSV` | `local,ts.net,tailscale.com` | Mac PAC 略過的公司網域／suffix |
| `--yes` | `false` | 使用明確指定／預設值，不詢問；僅供首次安裝 |
| `--no-install` | `false` | 建立設定／憑證，不註冊服務或要求 sudo |

依序詢問 IP、來源、四個連接埠、公司略過網域，最後確認需輸入 `yes`。直接 Enter 保留顯示的預設值。IP 必須是目標主機已配置的位址；拒絕 `0.0.0.0`、`::`、multicast 與帶 zone 的位址。連接埠須互異、介於 `1024–65535`，並且沒有被占用。來源可填 CIDR，單一 IP 會轉成 `/32` 或 `/128`，另自動加入 loopback 與監聽位址本身。輸入驗證失敗時尚未寫檔或變更服務，請修正後重跑。輸入結束不代表同意安裝。

確認後驗證並建立私有暫存設定、管理權杖及包含 admin IP 的 TLS 憑證，再安裝服務。`--no-install` 到產生檔案為止，之後用 `serve --config FILE` 啟動。Linux 必要時要求 sudo；`--yes` 不會略過 sudo 驗證。macOS 使用已登入使用者的 LaunchAgent，安裝時不要加 sudo。

新 Linux 安裝建立服務帳號、複製 binary 至 `/usr/local/lib/rillway/rillway`、建立 `/usr/local/bin/rillway`、寫入 unit，再執行 daemon-reload、啟用開機服務並立即啟動。實際設定為 `/etc/rillway/config.json`，私有狀態為 `/var/lib/rillway`。服務沒有 capabilities 且限制檔案系統寫入，只開放其設定與 state。兩個目錄為 `0700`，設定與憑證為 `0600`。原使用者暫存檔不是安裝後服務的設定來源。

已有 unit、binary、PATH 連結、設定／state 目錄或保留檔案時，即使指定 `--yes` 也會拒絕。提升權限後會再次檢查。安裝錯誤可能留下部分檔案供診斷；先查看狀態與 journal，備份保留資料，不要直接反覆重跑。setup 不是升級或重設指令。

WARP 維持停用；固定 GitHub CDN 規則在完成 WARP 設定或手動修改前會失敗。setup 不變更 WARP 註冊／授權、主機 VPN、Mac Proxy、防火牆、Docker 或路由。安裝後再於 Web UI／TUI 設定出口。

核對顯示的 TLS 指紋後，開啟 HTTPS 網址，再於本機讀取權杖：

```sh
sudo cat /var/lib/rillway/admin.token
rillway service status
sudo journalctl -u rillway --no-pager -n 50
```

權杖請妥善保密。自簽憑證不會自動被信任，請核對後信任，或提供有效憑證。勿將權杖放進指令參數、Git、工單或日誌。

## `init`／`serve`：本機設定與前景 daemon

```sh
rillway init --config .local/config.json
rillway serve --config .local/config.json
```

都接受 `--config FILE`。`init` 產生預設設定與相鄰 `state/` 內的私有憑證，拒絕覆寫現有檔案。`serve` 載入設定，缺少時初始化，然後啟動四個 listener；Ctrl+C 停止。不安裝服務，也不修改 WARP 帳號。若需要 LAN，優先用 `setup --no-install`，讓憑證一開始就對應 IP。init 後修改 listener 時，應提供相符的 TLS 憑證；daemon 不會自動替換已有憑證。

## `tui`：本機與遠端管理

```sh
sudo rillway tui
rillway tui --url https://192.0.2.20:17892 \
  --token-file ./private/admin.token --ca ./private/admin.crt
```

| 選項 | 用途 |
| --- | --- |
| `--config FILE` | 本機設定，缺少時會初始化 |
| `--url URL` | 遠端 HTTPS 管理網址；不載入或初始化本機設定 |
| `--token-file FILE` | 管理權杖檔案，遠端模式必填 |
| `--ca PEM` | 受信任伺服器憑證／CA PEM；遠端未填則使用系統 trust |
| `--client-config FILE` | TUI 連線紀錄，儲存驗證成功的網址與檔案路徑 |

本機模式從設定取得 URL、權杖及憑證路徑，可由選項覆寫。TUI 管理已執行的 daemon，不會啟動前景 daemon。Linux 服務設定／state 是私有檔案，本機操作需相應權限。遠端模式請安全複製公開憑證與私有權杖到用戶端，權杖設 `0600`，透過可信方式核對指紋。沒有略過 TLS 驗證的選項。

第一次指定 VM 的 `--url`、`--token-file` 與 `--ca`，連線成功後就會記住。沒有本機設定的用戶端，下次可直接執行 `rillway tui`。紀錄位於相同 OS 應用程式設定目錄的 `client.json`，權限 `0600`；macOS 的舊小寫路徑仍相容。只保存網址與檔案路徑，不複製權杖內容。選用順序為：明確指定 `--url`、明確指定 `--config`、明確指定的 `--client-config` 連線紀錄、已存在的 OS 預設設定、最後才是自動記住的連線。明確選用的連線紀錄缺少或無效時會報錯。本機設定無效或無權限讀取時會直接報錯，不會偷偷改連另一台伺服器。選用本機設定時可直接管理安裝／啟動功能，不需要額外加上 `--config`。

連不到服務時，畫面會說明原因，按 `o` 可修改 HTTPS 網址、權杖與憑證檔案。按 `?` 查看使用方式；尚未載入設定時不會顯示假的版本或空白 Proxy 位址。連線成功後，使用說明會顯示實際 Proxy／PAC 位址。服務設定頁的 Web UI 管理權杖預設隱藏，按 `t` 才會顯示；離開該頁會自動隱藏。權杖只留在這次 TUI 執行期間的記憶體，不會複製進連線紀錄。

Web UI 和 TUI 顯示正在執行的**伺服器程式版本**。設定修訂號另行記錄設定變更，用來避免不同視窗的修改互相覆蓋。可在 Web UI「設定 → 進階資訊」查看，或在 TUI「服務設定」頁按 `x`。舊服務未提供程式版本時，顯示「版本資訊未提供」。

| 按鍵 | 功能 |
| --- | --- |
| `o` | 服務連線設定；沿用目前值，修改後 Enter 連線 |
| `?` | 使用說明 |
| `Tab`／右，Shift+Tab／左 | 切換連線總覽、出口與 VPN、服務設定 |
| `j`／下、`k`／上 | 選取列 |
| `r` | 更新，斷線後也能重試 |
| `a` | 切換自適應 |
| 連線總覽 Enter | 為選取主機建立規則，預選目前出口；`f` 切換 IP 版本 |
| 出口頁 `+` | 新增出口，預設 WARP；常用值已填好 |
| 表單 Tab／↑↓ | 換欄位；類型與開關用 ←→，Ctrl+U 清空文字 |
| 表單 Enter／Esc | 儲存或連線／取消；儲存失敗保留輸入 |
| 出口頁 `n`、`c`、`d`、`v` | 註冊、連線、斷線、驗證選定出口 |
| 出口頁小寫 `l` | 輸入 WARP+ license，輸入遮罩顯示 |
| 服務設定頁 `t` | 顯示或隱藏 Web UI 管理權杖 |
| 服務設定頁 `x` | 顯示或隱藏進階資訊（設定修訂號） |
| `i` 再 Enter | 安裝新本機服務，遠端模式不提供 |
| `s` 再 Enter | 啟動已安裝的本機服務，遠端模式不提供 |
| `L`／Ctrl+L | 切換中英；文字表單用 Ctrl+L，保留輸入 |
| Escape | 取消表單，非表單時離開 |
| `q`／Ctrl+C | 離開；輸入時 Ctrl+C 也會退出 |

Web UI 和 TUI 共用預設值：WARP 位址 `127.0.0.1:40000`、指令 `warp-cli`。WireGuard 設定檔路徑、Tailscale 節點名稱與獨立 state 路徑也會填好；這些路徑屬於執行服務的主機。WireGuard／Tailscale 初始停用，請提供自己的 WireGuard 設定或啟用 Tailscale 後登入。工具不會產生帳號金鑰，Tailscale 維持公司／私網用途。新增規則預填 `github.com` 與目前預設出口，可改成實際目的地。
Web UI 另外提供 profile／規則編輯與安全刪除出口；`direct` 不可刪除，有引用的出口需要明確指定替代出口。

## `service`：systemd／LaunchAgent

```sh
sudo rillway service install --config /absolute/new/config.json
rillway service status
sudo rillway service stop
sudo rillway service start
sudo rillway service restart
sudo rillway service uninstall
```

`install`、`start`、`stop`、`restart`、`status`、`uninstall` 都接受 `--config FILE`，只有 `install` 使用此路徑；首次安裝建議使用 `setup`。Linux install／uninstall 需要 root，start／stop／restart 也可能需要 systemctl 權限。status 不使用 pager。手動 stop 不會被 restart-on-failure 重新啟動；enabled 並不等於已通過重新開機驗收。

uninstall 停用／停止服務並移除 unit，**保留** 帳號、binary／PATH 連結、設定、權杖、TLS 與 VPN state。沒有破壞性 purge 指令；保留路徑會阻止意外重裝。

macOS 所有 service 指令不加 sudo。Label 為 `io.rillway.daemon`，binary 為 `~/Library/Application Support/Rillway/rillway`，plist 為 `~/Library/LaunchAgents/io.rillway.daemon.plist`。使用安裝來源設定路徑與 GUI 登入 session。stop 卸載 agent、start 載入、restart kickstart。日誌為 Application Support 目錄內 `daemon.log`／`daemon-error.log`。uninstall 只移除 LaunchAgent 註冊及 plist。

## Linux 既有安裝：更新與還原

不要重跑 `setup`／`service install`。先查看 `systemctl cat rillway`，備份 binary、實際設定及憑證，核對新版架構／雜湊。若要一致的 VPN state 備份，先停止服務再複製。以下只替換執行檔，備份名稱應使用尚未存在的檔名：

```sh
sudo cp -p /usr/local/lib/rillway/rillway /usr/local/lib/rillway/rillway.before-update
sudo install -m 0755 ./rillway-linux-amd64 /usr/local/lib/rillway/rillway.next
sudo mv /usr/local/lib/rillway/rillway.next /usr/local/lib/rillway/rillway
sudo rillway service restart
rillway service status
```

之後驗證可信 HTTPS 管理及實際 Proxy 請求。設定、TLS、權杖、WARP 註冊、公司 VPN 與 unit 都保留。同目錄 rename 避免執行檔只被部分覆寫。舊設定路徑仍可使用，binary 更新不會搬移設定或修改 unit。還原時先停服務，原子替換回相容的舊 binary，再使用保留且相容的設定／state 啟動。unit 變更與驗收界線見 [部署文件](deployment.md)。

## `pac` 與 macOS `client`

```sh
rillway pac --config .local/config.json > proxy.pac
rillway client list
sudo rillway client apply --service "Wi-Fi" \
  --pac-url http://192.0.2.20:17893/proxy.pac --backup "$HOME/rillway-proxy-backup.json"
sudo rillway client restore --backup "$HOME/rillway-proxy-backup.json"
```

`pac --config FILE` 將 PAC 輸出至 stdout，不修改系統；daemon 也提供 `/proxy.pac`。公司網域／CIDR 在 Mac bypass，公開流量送往 Ubuntu；不提供公開流量的自動 DIRECT fallback。macOS 可在「網路 → 詳細資訊 → 代理伺服器」開啟「自動代理伺服器設定」並填入 PAC URL；完整畫面步驟見 [Apple：在 Mac 上輸入代理伺服器設定](https://support.apple.com/zh-tw/guide/mac-help/mchlp25912/mac)。

`client` 只支援 macOS。list 列出 **network service 名稱**，例如 Wi-Fi、USB Ethernet，不是 en0。apply 接受 `--service`（預設 Wi-Fi）、必填 `--pac-url`、`--backup`（預設目前目錄的 `rillway-proxy-backup.json`）。先備份 PAC URL／狀態及手動 Proxy 啟用狀態，再設定 PAC、停用手動 Proxy；保留原 server／port／憑證。拒絕覆寫既有備份，套用失敗會嘗試還原。`restore --backup FILE` 還原後才刪除快照。只有此明確指令會修改選定的 Mac 網路服務。

## `docker export`：Engine、Build、容器

```sh
rillway docker export --target daemon --proxy-url http://192.0.2.20:17890 > daemon.proxy.json
rillway docker export --target client --proxy-url http://192.0.2.20:17890 \
  --input "$HOME/.docker/config.json" > docker-client.merged.json
rillway docker export --target env --proxy-url http://192.0.2.20:17890 > proxy.env
rillway docker export --target compose --proxy-url http://192.0.2.20:17890 > compose.proxy.yaml
```

| 選項 | 預設／用途 |
| --- | --- |
| `--target daemon|client|env|compose` | 預設 `client` |
| `--config FILE` | 從設定取得 HTTP URL 與預設 bypass |
| `--proxy-url URL` | Docker 能到達的 HTTP Proxy，可不載入設定 |
| `--no-proxy CSV` | 覆寫網域／IP／CIDR bypass；明確空值代表移除 bypass |
| `--input FILE` | daemon／client 合併既有 JSON，不修改來源 |

daemon 格式用於 image pull／push；client 設定提供 Build arguments 與新容器預設值。env／Compose 格式用於容器 HTTP／HTTPS client。只匯出 stdout，不安裝設定、不重啟 Docker、不修改 OrbStack／Desktop，也不自動驗證連線。地址必須讓 Docker／容器可到達；容器 loopback 指容器自身。請保留公司／私網 bypass；PAC 的 DNS-aware 行為與 Docker `NO_PROXY` 不等價。

套用前先檢查輸出。**不要將輸出重新導向 `--input` 的同一檔案**，shell 會先清空來源。合併保留 registry authentication 等無關設定，請保密。拒絕 URL 內含 Proxy 憑證；啟用 Proxy 密碼時需另外安排 Docker 相容驗證。Desktop／OrbStack 與 driver 差異見 [Docker 操作文件](docker.md)。

## `diagnose`：明確啟動的連線／下載診斷

```sh
sudo rillway diagnose --config /etc/rillway/config.json --outbound direct --family ipv4
sudo rillway diagnose --config /etc/rillway/config.json --outbound warp \
  --download-url https://YOUR_HOST/YOUR_TEST_FILE
```

接受 `--config FILE`，`--outbound ID` 預設 direct，`--family auto|ipv4|ipv6` 預設 auto。輸出 JSON，分開呈現 DNS、IP 類型、建連品質與此次觀察到的 CDN 資訊；各目標錯誤放在 JSON 的 `error` 欄位，完整報告即使有目標失敗仍可能結束碼 `0`，請同時檢查報告內容。WARP 需已設定／連線；避免同時占用相同 tsnet state。`--download-url HTTPS_URL` 明確下載至多 **4 MiB**，不跟隨 redirect；請選擇合適且允許測試的檔案。啟動不會自動下載測速。CDN header 只能代表該次回應，不能證明物理位置；建連延遲不是下載速度。

## `licenses`、`version`、`help`

```sh
rillway licenses > THIRD-PARTY-NOTICES.txt
rillway version
rillway help
rillway setup --help
```

licenses 輸出完整內建的套件、Go、字體授權，不需要設定或附屬檔案。version／`--version` 顯示建置版本，目前開發版為 `0.1.0-dev`；版本指令不代表已公開發布。help／`--help`／`-h` 顯示指令清單，各指令的 help 顯示選項，沒有安裝服務或生成憑證的副作用。
