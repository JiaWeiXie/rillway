# 設定範例

建議透過 `rillway init` 產生具有絕對檔案路徑的初始設定。`config.json` 供閱讀與調整：若直接採用，先複製到專案根目錄的 `.local/config.json`，所有相對檔案路徑以**啟動程式時的工作目錄**為準；正式服務請使用絕對路徑。停用的 WireGuard／Tailscale profile 不會自動啟動。

`wireguard.conf` 僅有明顯的 placeholder，不能直接連線。真實設定與金鑰存入 `.local/secrets/` 或 VM 的 `/var/lib/rillway/`，不要放回 examples。安裝服務前，移除尚未準備好之 profile 的檔案參照，或準備好所指檔案。
