# Docker 代理

Rillway 的 HTTP Proxy 可處理映像 pull／push、Build 中的 HTTP／HTTPS 下載，以及容器內遵守 Proxy 環境變數的程式。這些連線仍使用現有分流規則、出口及觀察介面；不需要 HTTPS 解密。

Web UI 的 **Settings → Docker proxy** 可產生、複製或下載四種格式，支援英文與繁中。這個面板只產生設定，不修改 Docker、Rillway 設定或主機網路。CLI 也提供相同格式，另可合併既有 JSON。

## 選擇設定位置

| 連線 | Linux Docker Engine | macOS Docker Desktop／OrbStack |
| --- | --- | --- |
| pull／push、建置時取得基底映像 | Engine 的 `daemon.json` | Desktop 或 OrbStack 自己的 Proxy 設定 |
| Build 的 `RUN` 下載 | Docker client 的 `proxies.default` 或明確的 build args | 同左 |
| 新容器內 HTTP／HTTPS | Docker client 設定、`--env-file` 或 Compose environment | 同左 |

目前 NAS Proxy 是 `http://192.0.2.21:17890`。HTTP_PROXY 與 HTTPS_PROXY 都用這個 **HTTP** URL；HTTPS 目的地經 CONNECT 傳輸，不能將 Proxy URL 改成管理介面的 HTTPS 埠。Docker 不讀瀏覽器 PAC，因此須另外設定。

容器中的 `127.0.0.1` 通常指容器自己。使用能從 Docker daemon、builder 及容器抵達的 LAN 位址；本機 host gateway 名稱與網路模式須依 Docker 平台確認。Proxy 的來源 ACL 也須允許實際來源。目前 NAS 只允許既有 Mac、VM 與 loopback；其他主機需另加明確來源。

## 產生及合併 JSON

以下命令不改原始檔。先保護輸出檔，再檢查、備份並套用到對應 Docker 設定。

```sh
umask 077
mkdir -p .local/docker
# 不需載入本機 Rillway 設定，也能指定遠端 Proxy。
rillway docker export --proxy-url http://192.0.2.21:17890 --target client > .local/docker/config.json
rillway docker export --proxy-url http://192.0.2.21:17890 --target daemon > .local/docker/daemon.json
# 已有 client 設定時，合併保留 auths、credsStore 及其他欄位。
rillway docker export --proxy-url http://192.0.2.21:17890 --target client --input "$HOME/.docker/config.json" > .local/docker/config-merged.json
# 也可從 Rillway 設定讀取公布的 Proxy 位址與 PAC bypass 清單。
rillway docker export --config /path/to/rillway/config.json --target client
```

Linux 的既有 `/etc/docker/daemon.json` 可用 `--target daemon --input` 合併；若須讀取 root 擁有的檔案，使用適當權限。不要盲目覆蓋既有設定，合併後先用 `dockerd --validate --config-file /path/to/merged.json` 檢查，再於合適時段套用及重啟 Docker。重啟可能影響容器。已用 daemon flags 指定相同選項時，也須解除重複設定。

client 格式為 `proxies.default.httpProxy`／`httpsProxy`／`noProxy`；daemon 格式為 `proxies.http-proxy`／`https-proxy`／`no-proxy`。client 設定套用後，Docker 會為**新建容器及後續 Build**提供大小寫 Proxy 變數，不修改既有容器，也不替 daemon 的 pull／push 設定 Proxy。

合併會保留 client 的個別 daemon Proxy 區塊；它們可能優先於 `default`，請檢查目前 Docker context 對應的設定。合併輸出也可能包含原有 registry 憑證，勿提交或分享。Web 匯出不讀 Docker 憑證；Proxy 有啟用認證時顯示提醒，但不輸出帳密。

## Build、docker run 與 Compose

```sh
rillway docker export --proxy-url http://192.0.2.21:17890 --target env > .local/docker/rillway-docker.env
docker run --rm --env-file .local/docker/rillway-docker.env curlimages/curl:8.14.1 https://example.com
rillway docker export --proxy-url http://192.0.2.21:17890 --target compose > .local/docker/compose.yaml
```

Compose 匯出是範例，將 `your-image:tag` 換成你的映像，再把 environment 區塊合併進既有服務。環境變數檔使用沒有引號的 `KEY=value`，適合 `docker run --env-file`；不需要用 shell 執行它。

若 client JSON 尚未套用，可明確傳 Build 的參數：

```sh
docker build --build-arg HTTP_PROXY=http://192.0.2.21:17890 \
  --build-arg HTTPS_PROXY=http://192.0.2.21:17890 \
  --build-arg http_proxy=http://192.0.2.21:17890 \
  --build-arg https_proxy=http://192.0.2.21:17890 \
  --build-arg NO_PROXY=localhost,.corp.example,10.0.0.0/8 \
  --build-arg no_proxy=localhost,.corp.example,10.0.0.0/8 .
```

Docker 預定義的 Proxy build args 不需要在 Dockerfile 宣告 `ARG`。避免用 `ENV` 把 Proxy 或認證永久寫入映像；明確參照／重新宣告參數也可能改變快取及 history 行為。獨立 `docker-container`／遠端 BuildKit builder 的 registry 連線另屬 builder 本身，須為該 builder 設定 Proxy，client 的 build args 只處理建置步驟。請在正式環境分別驗證使用中的 builder driver。

## macOS

Docker Desktop 的 Engine JSON **不套用** daemon Proxy，請使用 Desktop 自身的 Proxy 設定；依版本分別設定 image pull 與容器代理。client／env／Compose 的匯出仍可用於建置及容器。

OrbStack 預設跟隨 macOS Proxy，也可明確設定：

```sh
orb config get network_proxy
orb config set network_proxy http://192.0.2.21:17890
orb config set network.proxy.exclude "localhost,.corp.example,10.0.0.0/8,192.168.0.0/16,100.64.0.0/10"
# 恢復跟隨系統設定；若原本是自訂值，應恢復原值。
orb config set network_proxy auto
```

執行前保存現有值，依你實際公司網域調整略過清單。本次開發沒有修改 Mac 的 OrbStack、系統 Proxy 或 Tailscale。

## 略過公司服務與出口選擇

預設 `NO_PROXY` 沿用 Rillway PAC bypass 網域及私有／Tailscale CIDR，加上 `localhost`。可用 `--no-proxy 'localhost,.corp.example,10.0.0.0/8'` 覆寫；明確的 `--no-proxy ''` 清空清單。不同程式的 NO_PROXY／CIDR／wildcard 行為不完全相同，建議公司名稱加入明確網域 suffix，並在容器與 Build 中實際驗證。

略過 Proxy 仍需要 Docker 本身能使用公司 DNS、Tailscale 或 subnet route。不要因為 Mac 能連公司服務就推論 Ubuntu 或容器也能連；這部分尚未使用公司目標驗收。此功能不提供整個容器網路的透明代理，忽略 Proxy 變數的程式、UDP 與任意 TCP 不在範圍內。

Docker Hub 的 `registry-1.docker.io`、`auth.docker.io` 及實際下載 CDN 會出現在 Rillway 觀察介面。可為觀察到的網域新增固定 WARP 規則或啟用自適應；本功能不自動更改這些網域的出口。CDN redirect 可能改變，不應把固定網域清單當成完整名單。固定 WARP 規則在 WARP 停止時會失敗，不會轉成直連。

## API 與驗證

`GET /api/v1/integrations/docker` 回傳預設 Proxy URL、NO_PROXY、loopback／認證提示與四份匯出。`POST` 接受 `{"proxy_url":"http://host:17890","no_proxy":"localhost,.corp.example"}`，只驗證並產生設定；需管理 token、來源 ACL 與同源請求，不更改 revision、VPN 或設定檔。

測試及 NAS 實機結果見 [驗證紀錄](verification.md)。官方設定語意參考 [Docker daemon Proxy](https://docs.docker.com/engine/daemon/proxy/)、[Docker client Proxy](https://docs.docker.com/engine/cli/proxy/)、[OrbStack networking](https://docs.orbstack.dev/docker/network)。
