# NAS Ubuntu 部署目標

使用者指定的部署主機，確認日期：2026-10-04。**目前只完成唯讀確認，尚未部署 Rillway。**

| 項目 | 已確認內容 |
|---|---|
| SSH 主機 | `operator@192.0.2.21` |
| 本機 SSH 金鑰參照 | `~/.ssh/id_ed25519_rillway`，不複製金鑰到專案或 VM |
| 作業系統 | Ubuntu 26.04.1 LTS |
| 架構 | x86_64，使用 `dist/rillway-linux-amd64` |
| CPU | 4 vCPU |
| VM 可見記憶體 | 5,408 MiB，約 5.3 GiB |
| 根目錄檔案系統 | 31G，22G 可用 |
| 網卡／IP | `vm-interface`，`192.0.2.21/24` |
| 預設閘道 | `192.0.2.1`，經 `vm-interface` |
| 本次 Mac 來源 IP | `192.0.2.70`，正式部署前再確認 |
| SSH 與 sudo | 指定金鑰可登入，`sudo -n true` 成功 |
| 現有 Rillway | 未安裝 systemd unit，沒有 `/var/lib/rillway/config.json` |
| 所需連接埠 | TCP 17890–17893 目前未被占用 |
| VPN client | `warp-cli`、`tailscale` 均未在 PATH 找到 |

```sh
ssh -i ~/.ssh/id_ed25519_rillway operator@192.0.2.21
```

後續部署的預定位置：

- HTTP Proxy：`192.0.2.21:17890`
- SOCKS5：`192.0.2.21:17891`
- HTTPS 管理介面：`https://192.0.2.21:17892`
- PAC：`http://192.0.2.21:17893/proxy.pac`
- systemd 執行帳號：`rillway`
- 有效設定：`/var/lib/rillway/config.json`

上述網址尚未提供服務。正式部署時依當時 Mac IP 設定來源 ACL，憑證包含 VM IP，WARP 保留 Local Proxy 模式，公司流量繼續由 Mac 的 Tailscale 處理。WARP+ 授權碼與公司帳號尚未提供，不寫入 Git。

部署程序見 [Ubuntu VM 與 Mac 部署](deployment.md)，驗收邊界見 [驗證紀錄](verification.md)。本次沒有修改 VM 網路、套件、服務或 Mac 的 Proxy 設定。
