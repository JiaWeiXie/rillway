# Ubuntu VM 與 Mac 部署

此專案已於 2026-10-04 部署至 `operator@192.0.2.21`，Rillway 以低權限帳號執行，systemd 服務為 enabled、active。HTTP／SOCKS5／HTTPS CONNECT 與管理 API 登入已通過實機驗收。實際位址、來源限制、管理憑證位置與 WARP 驗證狀態見 [NAS Ubuntu 部署目標](deployment-target.md)；下列 `192.0.2.20`／`.30` 為新安裝的操作範例。

## NAS VM 配額

個人使用起始配置：**Ubuntu Server 26.04 LTS、2 vCPU、4 GB RAM、32 GB 磁碟、1 張橋接虛擬網卡**。若也在 VM 編譯與跑 race，建議 8 GB／64 GB。這是工程起始配額，未宣稱已在你的 NAS 上量測吞吐；需依 CPU 與同時連線數調整。

使用 NAS 的有線 LAN 虛擬交換器／bridge，VirtIO 網卡優先。透過路由器 DHCP reservation 固定 VM IP。先保留一張虛擬網卡、一個 default gateway；不需要 PCI passthrough。第二張實體網卡只有在 NAS 檔案流量占滿第一張、或有 VLAN 隔離需求時才有價值。橋接本身足以讓 VM 與 Mac 在 LAN 直接通信。

更換實體網卡不會改變 ISP 到 GitHub 的上游路由；改善路徑由 WARP 或遠端 WireGuard 出口提供。公司網路繼續由 Mac 既有 Tailscale 處理。

參考：[Ubuntu 26.04 release notes](https://documentation.ubuntu.com/release-notes/26.04/)、[libvirt bridged networking](https://wiki.libvirt.org/Networking.html)。

## 安裝 binary

以下流程只供首次安裝。引導與安裝器偵測到既有 unit、binary、PATH 連結、設定／state 目錄時會拒絕，不覆寫既有設定。更新既有服務請只替換 binary。

從 `mise run release` 選擇 VM 架構的 `dist/rillway-linux-amd64` 或 `arm64`，核對 SHA256SUMS、改名 `rillway` 並給予執行權限。每個平台是單一執行檔，包含 Web UI、字體、圖片與完整第三方授權，可用 `rillway licenses` 匯出。Go、Node、mise 與附屬素材不是執行相依套件；WARP 官方 client 仍需另外安裝。

```sh
chmod +x ./rillway
./rillway --lang zh-Hant setup
```

引導詢問 VM 的指定 IP、允許的來源、HTTP／SOCKS5／HTTPS 管理／PAC 連接埠、公司略過網域，最後輸入 `yes` 才寫檔及安裝。Linux 必要時要求 sudo，daemon 使用低權限 `rillway` 帳號。自動化首次安裝範例：

```sh
./rillway setup --yes --listen 192.0.2.20 --allow-client 192.0.2.30 \
  --bypass-domains local,ts.net,tailscale.com,corp.example
```

VM 與 Mac 位址是範例，請使用實際位址。預設只聽 loopback；引導會自動加入 loopback 與 VM 自身 IP 的 ACL，避免 VM 存取自己的 LAN listener 被拒絕。IPv6 使用指定 IP，會自動加方括號、`/128` 與正確 TLS SAN。公司 bypass domains 仍保留於 Mac PAC，私網 CIDR 預設維持。

新安裝的有效設定在 **`/etc/rillway/config.json`**，私有 token、TLS 與 VPN state 在 `/var/lib/rillway/`；設定與 state 目錄 `0700`，秘密與設定檔 `0600`。Binary 位於 `/usr/local/lib/rillway/rillway`，PATH 連結 `/usr/local/bin/rillway`。systemd unit 啟用開機啟動並立即 start，只允許 daemon 寫入其設定／state。原暫存的使用者設定保留，安裝後服務不會讀取它。

現有 NAS 部署仍使用 **`/var/lib/rillway/config.json`**，新版 binary 可直接使用，不會搬移檔案或修改既有 unit。請先查看 `systemctl cat rillway`，再對實際設定操作。

安裝印出 TLS 指紋與權杖檔案路徑，不輸出秘密內容。核對後信任自簽憑證或改用有效 TLS 憑證，於本機讀取 token 登入 Web UI。HTTP／SOCKS listener 是標準明文 Proxy，限制於可信任 LAN／VPN；ACL 不取代加密，不要公開 port forwarding。setup 不會修改防火牆、Mac proxy、公司 Tailscale、WARP 註冊／license 或 Docker。

```sh
rillway service status
sudo rillway tui --config /etc/rillway/config.json
```

詳細選項與更新／還原說明見 [CLI：繁體中文](cli.zh-Hant.md)／[CLI: English](cli.md)。若只想準備設定，使用 `setup --no-install --config FILE`，之後 `serve --config FILE` 在前景執行。不要啟動使用相同 listener 或 Tailscale state 的第二個 daemon。

後續使用 Web UI，或把 admin 憑證與 token 安全複製到自己的 Mac，再使用遠端 TUI：

```sh
rillway tui --url https://192.0.2.20:17892 --token-file ./admin.token --ca ./admin.crt
```

`service stop/start/restart/uninstall` 由 Ubuntu systemd 管理，變更狀態可能需要 sudo。uninstall 移除 unit，保留帳號、binary、設定與 VPN state，避免意外遺失金鑰。手動停掉 service 不會被自身 restart policy 重新啟動。

## 正式部署驗收與更新

`scripts/acceptance-ubuntu.sh` 只供可拋棄的全新 VM 驗收。腳本結束時的 trap 會卸載 systemd unit，並保留設定與 VPN state；因此不能用它維持正式部署，已有安裝也會被腳本拒絕。

正式安裝後，先確認服務為 enabled、active、執行帳號為 `rillway`，檢查狀態目錄及秘密檔案權限，再從允許的來源驗證 PAC、受信任 TLS、未登入 API 拒絕、token 登入與各 Proxy 協定的實際傳輸。管理 token 不應放進可被程序列表看見的命令參數或檢查輸出；檢查工具應從受限檔案或標準輸入讀取。停止／啟動驗收完成後，再確認管理與 Proxy 連線恢復。這些步驟不需要修改 Mac 系統 Proxy 或公司 Tailscale。

更新現有安裝的 binary 時，先記錄目前版本並備份 binary、有效設定及必要狀態；確認新檔案的架構與 SHA-256。只替換 `/usr/local/lib/rillway/rillway`，保留原有設定、憑證及 VPN state，再重新啟動服務並完成上述驗收。不要重新執行 `setup`／`service install`：新版會拒絕既有安裝及保留檔案，不能當作更新指令。若需一致的 VPN state 備份，應在停止服務後進行，避免複製正在寫入的檔案。

單獨更新 binary 不會更新既有 systemd unit。若版本包含 unit 修正，須另備份並修改 `/etc/systemd/system/rillway.service`。目前 NAS 已使用 `StateDirectoryMode=0700`。新版首次安裝另外使用 `/etc/rillway` 與 `ConfigurationDirectoryMode=0700`，不會在 binary 更新時強制搬移舊設定。不需要為了更新 unit 重跑安裝器。

更新失敗時停止服務、還原先前 binary，再使用保留的有效設定啟動；若新版本已改變設定或 state 格式，必須同時使用相容的備份。不要以重新初始化設定取代還原。正式的 WARP／WARP+、Tailscale、WireGuard 與下載品質測試依 [VPN 出口文件](providers.md) 分開驗收，結果記錄於 [驗證紀錄](verification.md)。

## Mac PAC

先把公司網域加入 `pac.bypass_domains`；CIDR 加入 `pac.bypass_cidrs`。預設略過 RFC1918、loopback、link-local、Tailscale CGNAT、IPv6 ULA 及 `.ts.net`。PAC 在 **Mac** 解析公司名稱，保留現有 Tailscale DNS。DNS 查到私網位址也會 bypass；仍建議公司網域明確列入，避免 DNS 不可用時送出公司名稱。

公開流量送往 Ubuntu；Ubuntu 的 `direct` 仍會留下觀察資訊。Mac 的 `DIRECT` bypass 完全不經 Rillway，所以不會出現在介面上。PAC 對公開網站不附加自動 `DIRECT` fallback，VM 停機時公開代理連線會失敗。

```sh
rillway client list
sudo rillway client apply --service "Wi-Fi" --pac-url http://192.0.2.20:17893/proxy.pac --backup "$HOME/rillway-proxy-backup.json"
sudo rillway client restore --backup "$HOME/rillway-proxy-backup.json"
```

這裡選的是 macOS **network service 名稱**（例如 Wi-Fi、USB Ethernet），不是 `en0` 裝置編號。套用前備份原 PAC URL 與 HTTP／HTTPS／SOCKS 啟用狀態，手動 Proxy 的 server／port／帳密不變；失敗會嘗試還原。備份存在時拒絕覆寫。還原後移除備份。原本沒有 PAC URL 時還原為停用狀態。

只想設定單一瀏覽器時，直接使用該瀏覽器的 HTTP／SOCKS 設定或 PAC 即可；是否有獨立設定取決於瀏覽器。啟用 HTTP Proxy 不代表任意 App、UDP 或 QUIC 都會經過 Proxy，請檢查實際連線觀察結果。

## macOS 本機 daemon

```sh
rillway setup
rillway service status
rillway service stop
rillway service uninstall
```

不加 sudo：使用者 LaunchAgent 與 GUI 登入 session。binary 複製至 `~/Library/Application Support/Rillway/`，plist 在 `~/Library/LaunchAgents/io.rillway.daemon.plist`。安裝保留設定路徑，移動或刪除設定會影響下次啟動。Mac 的 WARP 可否以 Local Proxy 使用仍依官方 client 版本、帳號及模式驗證結果判定。
