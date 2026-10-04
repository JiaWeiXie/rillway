# Ubuntu 部署目標範本

這份文件只保留可公開的部署範本，不記錄個人帳號、實際主機名稱、IP、網卡名稱、SSH 金鑰位置、憑證位置或執行中服務狀態。正式環境資料應放在 repository 之外，並限制檔案權限。

## 建議 VM 規格

| 項目 | 建議值 |
| --- | --- |
| 作業系統 | Ubuntu Server 24.04 或 26.04 LTS |
| CPU | 2 vCPU；同機編譯或跑 race tests 時使用 4 vCPU |
| 記憶體 | 4 GB；同機開發時使用 8 GB |
| 磁碟 | 32 GB；保留建置快取時使用 64 GB |
| 網路 | 1 張 VirtIO 橋接網卡及 1 個 default gateway |
| 服務帳號 | 安裝器建立的低權限 `rillway` 帳號 |

## 文件用位址

下列 `192.0.2.0/24` 是 RFC 5737 文件保留網段，不能直接用於真實部署。請在 repository 外保存實際值。

| 用途 | 文件範例 |
| --- | --- |
| VM | `192.0.2.20` |
| 管理端 | `192.0.2.30` |
| HTTP Proxy | `192.0.2.20:17890` |
| SOCKS5 | `192.0.2.20:17891` |
| HTTPS 管理介面 | `https://192.0.2.20:17892` |
| PAC | `http://192.0.2.20:17893/proxy.pac` |

```sh
ssh -i ~/.ssh/id_ed25519_rillway operator@proxy-vm.example.invalid

./bin/rillway tui --url https://192.0.2.20:17892 \
  --token-file .local/servers/example/admin.token \
  --ca .local/servers/example/admin.crt
```

`.local/` 已被 Git 忽略，但實際 token、私鑰、WARP 註冊資料、Tailscale state 與 WireGuard 設定仍應使用 `0600` 或更嚴格權限，並由秘密管理工具或受限制的主機檔案保存。不要把正式環境清單、驗收截圖或備份路徑寫回 repository。

安裝、更新及還原流程見 [Ubuntu VM 與 Mac 部署](deployment.md)。外部 VPN 驗證方式見 [VPN 出口](providers.md)。
