# NAS Ubuntu 部署目標

使用者指定的部署主機，驗證日期：2026-10-04。**Rillway 已部署並由 systemd 持續執行。** 下列資訊是本次實測紀錄；來源 IP、可用磁碟與服務狀態仍可能隨環境變動。

| 項目 | 已確認內容 |
|---|---|
| SSH 主機 | `operator@192.0.2.21` |
| 本機 SSH 金鑰參照 | `~/.ssh/id_ed25519_rillway`，不複製金鑰到專案或 VM |
| 作業系統 | Ubuntu 26.04.1 LTS |
| 架構 | x86_64，使用 `dist/rillway-linux-amd64` |
| CPU | 4 vCPU |
| VM 可見記憶體 | 5,408 MiB，約 5.3 GiB |
| 根目錄檔案系統 | 31G，21G 可用 |
| 網卡／IP | `vm-interface`，`192.0.2.21/24` |
| 預設閘道 | `192.0.2.1`，經 `vm-interface` |
| 本次 Mac 來源 IP | `192.0.2.70` |
| SSH 與 sudo | 指定金鑰可登入，`sudo -n true` 成功 |
| Rillway 服務 | `rillway.service` 為 enabled、active，使用低權限帳號 `rillway` |
| Binary | `/usr/local/lib/rillway/rillway`；PATH 入口 `/usr/local/bin/rillway` |
| 有效設定 | `/var/lib/rillway/config.json` |
| 狀態與憑證權限 | `/var/lib/rillway` 為 `0700`；config、token、TLS cert/key 為 `0600` |
| 來源 ACL | 僅 `192.0.2.70/32`、`192.0.2.21/32`、`127.0.0.0/8`、`::1/128` |
| 管理憑證 | SAN 包含 `192.0.2.21`，已驗證 TLS 連線 |
| WARP 官方 client | `CURRENT_VERSION`，由 Cloudflare 官方 `resolute` APT 來源安裝；Local Proxy 已完成端到端驗證；使用者套用授權後，WARP+ Unlimited 與 live 子測試亦通過 |

```sh
ssh -i ~/.ssh/id_ed25519_rillway operator@192.0.2.21
```

目前提供服務的位置：

- HTTP Proxy：`192.0.2.21:17890`
- SOCKS5：`192.0.2.21:17891`
- HTTPS 管理介面：`https://192.0.2.21:17892`
- PAC：`http://192.0.2.21:17893/proxy.pac`
- systemd 執行帳號：`rillway`
- 有效設定：`/var/lib/rillway/config.json`

HTTP Proxy、SOCKS5、HTTPS CONNECT 的真實傳輸，以及管理 API 未登入回傳 `401`、使用 token 登入，均已通過驗收。WARP Local Proxy 與端到端 trace 已通過；同日稍後 WARP+ Unlimited 與付費帳號 live 子測試也通過。公司 tailnet 與外部 WireGuard 尚未驗證。`github.com` 命中直連規則，`raw.githubusercontent.com` 命中 WARP 規則，已在真實代理流量與統計中確認。

Mac 的管理 token 與公開 CA 憑證副本位於專案已被 Git 忽略的 `.local/servers/example/admin.token`、`.local/servers/example/admin.crt`；沒有複製 TLS 私鑰。從專案目錄可啟動遠端 TUI：

```sh
./bin/rillway tui --url https://192.0.2.21:17892 \
  --token-file .local/servers/example/admin.token --ca .local/servers/example/admin.crt
```

這台 Mac 已在嚴格 TLS 驗證成功後，將上述網址及兩個檔案路徑記錄於使用者私有的 TUI client profile。現在從專案目錄執行 `./bin/rillway tui` 會直接連到此 VM；profile 不含 token 內容。

Mac 原有 Proxy 設定與 Tailscale，以及 VM 預設路由均未修改。公司流量繼續由 Mac 的 Tailscale 處理。WARP+ 授權已由使用者套用，未寫入 Git；公司帳號仍未提供；完整外部出口狀態以 [驗證紀錄](verification.md) 為準。

後續操作見 [Ubuntu VM 與 Mac 部署](deployment.md)。此主機已有正式安裝，更新 binary 時不要重新執行 `setup`／`service install`：新版會拒絕既有安裝，更新應保留實際設定與狀態；也不要執行會在結束時卸載服務的 `scripts/acceptance-ubuntu.sh`。

目前已更新至出口預設值、TUI 連線引導及目的地分組版本，最新 Linux amd64 artifact SHA-256 為 `56142e04469be3634df0754bc0a50e4ba3db4b03d6c5656ea158ff40a37d0018`，最後讀取的實際 revision 為 `9`。中英桌面／手機介面與三種 Proxy 協定驗收通過；設定、憑證、unit 與 WARP registration 均保持不變，沿用舊設定路徑與 PATH 入口。詳見 [最新驗證紀錄](verification.md)。
