# Ubuntu VM 與 Mac 部署

## NAS VM 配額

個人使用起始配置：**Ubuntu Server 26.04 LTS、2 vCPU、4 GB RAM、32 GB 磁碟、1 張橋接虛擬網卡**。若也在 VM 編譯與跑 race，建議 8 GB／64 GB。這是工程起始配額，未宣稱已在你的 NAS 上量測吞吐；需依 CPU 與同時連線數調整。

使用 NAS 的有線 LAN 虛擬交換器／bridge，VirtIO 網卡優先。透過路由器 DHCP reservation 固定 VM IP。先保留一張虛擬網卡、一個 default gateway；不需要 PCI passthrough。第二張實體網卡只有在 NAS 檔案流量占滿第一張、或有 VLAN 隔離需求時才有價值。橋接本身足以讓 VM 與 Mac 在 LAN 直接通信。

更換實體網卡不會改變 ISP 到 GitHub 的上游路由；改善路徑由 WARP 或遠端 WireGuard 出口提供。公司網路繼續由 Mac 既有 Tailscale 處理。

參考：[Ubuntu 26.04 release notes](https://documentation.ubuntu.com/release-notes/26.04/)、[libvirt bridged networking](https://wiki.libvirt.org/Networking.html)。

## 安裝 binary

從 `mise run release` 選擇 VM 架構的 `dist/rillway-linux-amd64` 或 `arm64`，改名 `rillway` 並給予執行權限。Go 與 Node 都不是執行相依套件。WARP 官方 client 另外安裝，詳見 providers 文件。

```sh
./rillway init --config "$HOME/.config/rillway/config.json"
```

初始化設定預設僅聽 loopback。假設 VM 是 `192.0.2.20`，Mac 是 `192.0.2.30`，在啟動前編輯：

```json
{
  "listeners": {
    "http": "192.0.2.20:17890",
    "socks5": "192.0.2.20:17891",
    "admin": "192.0.2.20:17892",
    "pac": "192.0.2.20:17893"
  }
}
```

這是欄位節錄，請保留其餘設定。將 `security.allowed_clients` 設為 `["192.0.2.30/32", "127.0.0.0/8", "::1/128"]`，`pac.proxy_address` 設為 `192.0.2.20:17890`。IPv6 用明確位址及適當 `/128` ACL，listener IPv6 加方括號。

`init` 已產生 loopback 憑證。改成 LAN IP 後，請提供包含 VM IP／名稱的憑證，或在尚未啟動時移走 `security.tls_cert_file` 與 `tls_key_file` **這一對檔案**，讓首次 `serve` 依新的 admin 位址重新產生；勿只移走其中一個。daemon 不會自動替換你提供的憑證。TLS 憑證預設一年有效，輪替後重啟。

Web UI 管理流量使用 HTTPS。HTTP／SOCKS listener 是標準明文 Proxy 協定，應限制於可信任的 LAN／VPN；ACL 限制來源，不要把這些埠做網際網路 port forwarding。若啟用 `proxy_username`／`proxy_password_file`，密碼檔案權限為 0600。PAC 本身不攜帶 Proxy 密碼；不同瀏覽器的驗證體驗需依使用者端測試。

```sh
sudo ./rillway service install --config "$HOME/.config/rillway/config.json"
./rillway service status
```

Linux 安裝器複製 binary 到 `/usr/local/lib/rillway/rillway`，複製設定與必要狀態到 `/var/lib/rillway/`，建立 `rillway` system user 與 systemd unit。安裝後有效設定是 `/var/lib/rillway/config.json`，原始家目錄檔案不再是服務的設定來源。不要同時啟動使用相同 Tailscale state 的前景服務。

後續使用 Web UI，或把 admin 憑證與 token 安全複製到自己的 Mac，再使用遠端 TUI：

```sh
rillway tui --url https://192.0.2.20:17892 --token-file ./admin.token --ca ./admin.crt
```

`service stop/start/restart/uninstall` 由 Ubuntu systemd 管理，變更狀態可能需要 sudo。uninstall 移除 unit，保留帳號、binary、設定與 VPN state，避免意外遺失金鑰。手動停掉 service 不會被自身 restart policy 重新啟動。

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
rillway init
rillway service install
rillway service status
rillway service stop
rillway service uninstall
```

不加 sudo：使用者 LaunchAgent 與 GUI 登入 session。binary 複製至 `~/Library/Application Support/Rillway/`，plist 在 `~/Library/LaunchAgents/io.rillway.daemon.plist`。安裝保留設定路徑，移動或刪除設定會影響下次啟動。Mac 的 WARP 可否以 Local Proxy 使用仍依官方 client 版本、帳號及模式驗證結果判定。
