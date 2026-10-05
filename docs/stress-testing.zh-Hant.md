# 拋棄式測試機壓力測試

[English](stress-testing.md) · [繁體中文](stress-testing.zh-Hant.md)

`tools/loadtest` 會向你擁有的測試主機傳送有時間及容量上限的合成流量。它是開發工具，不是另一個正式發布執行檔。使用專案的 mise 環境建置：

```sh
mise exec -- go build -trimpath -o bin/loadtest ./tools/loadtest
```

跨架構建置時，另設定 `GOOS`／`GOARCH`。在獨立測試機啟動目的端，它只提供程式產生的資料，不會讀取磁碟檔案；單次回應最多 8 MiB：

```sh
./bin/loadtest --owned-target --listen 192.0.2.30:18082
```

在另一台流量產生機選擇代理協定：

```sh
./bin/loadtest --owned-target \
  --target 'http://192.0.2.30:18082/bytes?bytes=8388608' \
  --protocol connect --proxy 192.0.2.20:17890 \
  --workers 32 --duration 15s --timeout 15s

# 128 個並行工作，每次建立新 TCP 連線；不等於 128 台實體裝置：
./bin/loadtest --owned-target \
  --target 'http://192.0.2.30:18082/bytes?bytes=65536' \
  --protocol connect --proxy 192.0.2.20:17890 \
  --workers 128 --fresh --duration 15s --timeout 15s
```

範例位址是文件專用位址，要換成自己測試機的位址。Rillway 的來源限制只允許流量產生機，目的端連接埠也只開放給測試機。不要把負載送往別人的網站、VPN 節點、公司服務或正式主機。`--owned-target` 是操作者的明確確認，程式不會自行證明所有權，也沒有強制限定私有位址。

| 參數 | 用途 |
| --- | --- |
| `--owned-target` | 必填，確認服務端及產生器皆為你擁有的測試目標 |
| `--listen HOST:PORT` | 目的端模式，前景執行直到停止 |
| `--target URL` | HTTP 目的端，拒絕 URL 內的帳密、不跟隨重新導向 |
| `--protocol` | 直連 `direct`、HTTP 轉送 `http`、TCP 通道 `connect`、`socks5` |
| `--proxy HOST:PORT` | HTTP／CONNECT 或 SOCKS 位址，目前不支援代理帳密 |
| `--workers` | 1～1024 個並行工作 |
| `--duration` | 1 秒～5 分鐘，預設 15 秒 |
| `--timeout` | 每次請求及通道建立的期限，100 ms～1 分鐘，預設 15 秒 |
| `--fresh` | 每次請求重新建立 TCP 連線 |

工具忽略環境變數中的代理設定。CONNECT 測試是在 TCP 通道內傳送 HTTP 合成資料，不含目的端 TLS 加密、HTTPS 解密、SOCKS UDP、上傳速度或瀏覽器畫面測試。VPN 必須連到你擁有的 peer，並設置固定分流規則；WireGuard／Tailscale 的 peer 要提供相同的有限容量回應，不需要公共 DNS fallback 或複製登入狀態。

結果以 JSON 顯示完成數、錯誤數、位元組數、Mbit/s、每秒請求數與回應時間 p50／p95。延遲包含完整下載，不是單純建連耗時；直方圖以 1 ms 分格，上限 60 秒，超出時標示 `latency_capped`。部分下載的位元組也計入流量，只有 HTTP 200 且完整收到宣告長度的回應才計為完成。整輪到期而取消的工作另列，不能算成服務錯誤，也不能算成下載完成。零完成數或請求錯誤會回傳非零結束碼。產生器不輸出或保存網址、內容、headers、憑證及上游原始錯誤。

## 配額與結果解讀

[GL.iNet Beryl AX 官方規格](https://www.gl-inet.com/products/gl-mt3000)列出雙核心 1.3 GHz、512 MB RAM 與 Gigabit LAN，作為此次家用規格的參考。VM 使用相同記憶體與核心數，也不等於相同 CPU 效能。

[OrbStack](https://docs.orbstack.dev/machines) 支援每台機器的 CPU／記憶體配額。本次只設 CPU 配額時，程序仍看得到 18 個 CPU index；因此另用只套在 Rillway 的臨時 [systemd CPUAffinity](https://github.com/systemd/systemd/blob/main/man/systemd.exec.xml) 限制一、兩個核心，重啟後讀取實際 PID 的 `Cpus_allowed_list`。同時讀取機器有效的 `cpu.max`、`memory.max`、`memory.swap.max`，不修改 OrbStack 全域設定或其他機器。

目的端與產生器要放在受測機之外，每種配額前重啟 daemon，避免上一輪保留的 heap 影響下一輪。測試完整安裝時保留官方 WARP daemon；它的記憶體不算在 Rillway 的 RSS 裡。Docker 若不是測試內容，就暫停或放在另一台機器。

Gigabit 情境只在拋棄式受測機的網卡暫設 TBF：`rate 1gbit burst 256kb latency 50ms`。檢查計數器，完成後移除。這是合計的傳出速率上限，不是完整重現雙向網卡、Wi-Fi 或 WAN。OrbStack 共用核心，Mac 的 CPU 及核心網路處理也可能在 daemon 的 affinity 之外完成工作，所以不能宣稱測得的速度就是實體路由器速度。

每秒量測程序 RSS、服務／機器的 cgroup 記憶體、匿名記憶體、swap、CPU 計數、檔案描述符、PID 是否改變、OOM 計數及登入後的 `/api/v1/stats` 延遲。RSS 是單一程序使用的實體記憶體；整機用量還包含 WARP、系統與快取。使用正確的 TLS 憑證，從私人檔案讀取 Token，不放進參數、log 或公開資料。從統計確認實際出口及使用中的連線數。觀察資料到達 2048 筆是保留容量，不是程式的連線數上限。

## 2026-10-05 實測結果

在 Ubuntu 26.04 arm64 測試目前未提交的開發 binary，另一台 Ubuntu 24.04 負責產生及接收流量。WARP 保持待機，Docker 暫停、沒有 swap，合計傳出速率限制為 1 Gbit/s；個別負載十五秒、每次請求期限十五秒。

| 配額 | 實際結果 |
| --- | --- |
| 256 MiB／一核心 | 直連三種代理及 128 工作短連線有完整監測；Tailscale 的監測逾時，停止該配額，WireGuard 未執行。完整 VPN 組合仍未驗證 |
| 512 MiB／一核心 | 六項全部有完整監測，請求及 API 錯誤、OOM 為零；WireGuard 約 555.8 Mbit/s、Tailscale 597.4 Mbit/s。整機用量到達 512 MiB 上限，程序 RSS 約 308.0 MiB |
| 512 MiB／兩核心 | 五項有完整監測；WireGuard 雖有下載完成，重跑仍無法在期限內取得完整監測，明確列為未驗證 |
| 1 GiB／兩核心 | 六項全部有完整監測，請求及 API 錯誤、OOM 為零；HTTP 直連 624.2、CONNECT 582.9、SOCKS5 577.1、Tailscale 677.0、WireGuard 450.7 Mbit/s。WireGuard 時整機用量最高 621.2 MiB |

1 GiB／兩核心另測一分鐘同時使用三種出口，每次 1 MiB：直連 32、WireGuard 八、Tailscale 八。管理統計確認三種出口及最高 48 條使用中連線，合計 674.7 Mbit/s、4805 次完整下載，請求／API 錯誤、PID 改變與 OOM 為零；到期取消的 48 個工作另列。程序 RSS 最高 322.9 MiB、整機最高 576.3 MiB，管理 API 最慢 22.5 ms。

依這次記憶體需求，正式 Ubuntu VM 建議以 **1 GiB／兩核心** 起步。這不是穩定最低規格或實體路由器速度承諾；每項基本負載只測一次、混合測試只有一分鐘，不能推論增加 CPU 必定更快。尚未測 IPv6、上傳、目的端 TLS、公網 WARP／WARP+ 下載壓力、路由器 Flash、OpenWrt 安裝及連續數小時／數日負載。初期五秒期限的錯誤與無法完整監測的記錄都保留，不列為成功。測試後已還原配額、限速、臨時 unit 設定及 Docker 服務。

實際位址及原始資料留在 Git 忽略的 `.local/`，資料夾 `0700`、檔案 `0600`。沒有監測、配額改變、PID 重啟、沒有完整下載或測項中斷，都必須列為失敗或未驗證。更多結果見[驗證紀錄](verification.md)。
