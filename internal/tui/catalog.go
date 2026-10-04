package tui

import (
	"errors"
	"rillway/internal/control"
	"rillway/internal/i18n"
)

// English messages are stable keys; dynamic names and IDs are never translated.
var catalog = map[string]string{
	"\n  Service: %s\n": "\n  服務：%s\n",
	"  Press i to install the background service.\n":              "  按 i 安裝背景服務。\n",
	"  Start or install the service on the machine running it.\n": "  請在執行服務的主機上啟動或安裝服務。\n",
	"\n  First connect to your Rillway service\n  Close this page and press o to choose its HTTPS address, token file and certificate.\n  Once connected, press ? again to see your actual proxy addresses.\n  Enter / Esc Close   Ctrl+L Language\n": "\n  先連到 Rillway 服務\n  關閉這頁，按 o 填寫服務的 HTTPS 網址、權杖檔案與憑證。\n  連線成功後，再按 ? 查看實際 Proxy 位址。\n  Enter／Esc 關閉   Ctrl+L 切換語言\n",
	"  Ctrl+L Language: English / Traditional Chinese\n":        "  Ctrl+L 語言：English / 繁體中文\n",
	"\n  ≈ Rillway   Routing console\n\n":                       "\n  ≈ Rillway   網路分流控制台\n\n",
	"Connections":                                               "連線總覽",
	"Outbounds & VPNs":                                          "出口與 VPN",
	"Service settings":                                          "服務設定",
	"\n\n  Adaptive routing: %s   Configuration revision: %d\n": "\n\n  自適應分流：%s   設定版本：%d\n",
	"Enabled":  "啟用",
	"Disabled": "停用",
	"\n  Connecting to the management service…\n": "\n  正在連接管理服務…\n",
	"\n  HTTP proxy    %s\n  SOCKS5        %s\n  Management UI %s\n  PAC           %s\n\n  Use PAC bypass for company services on Mac to keep using local Tailscale.\n  Edit PAC and all routing rules in the Web UI.\n": "\n  HTTP Proxy    %s\n  SOCKS5        %s\n  管理介面      %s\n  PAC           %s\n\n  Mac 的公司服務請設為 PAC 略過，繼續使用本機 Tailscale。\n  PAC 與完整分流規則可在 Web UI 編輯。\n",
	"\n  Error: %s\n": "\n  錯誤：%s\n",
	"\n  Tab Switch tab   ↑↓ Select   a Toggle adaptive routing   r Refresh   q Quit\n": "\n  Tab 換頁   ↑↓ 選擇   a 切換自適應分流   r 更新   q 離開\n",
	"  L / Ctrl+L Language: English / Traditional Chinese\n":                            "  L / Ctrl+L 語言：English / 繁體中文\n",
	"  Enter Create routing rule; existing connections keep their outbound.\n":          "  Enter 建立分流規則；既有連線維持原出口。\n",
	"  c Connect   d Disconnect   v Verify   n Register   l WARP+ license key\n":        "  c 連線   d 中斷連線   v 驗證   n 註冊   l WARP+ 授權碼\n",
	"\n  No connections yet. Point your browser or Mac at the Rillway proxy.\n":         "\n  尚無連線。請將瀏覽器或 Mac 的 Proxy 指向 Rillway。\n",
	"Destination":                 "目的地",
	"Outbound":                    "出口",
	"Download":                    "下載",
	"Upload":                      "上傳",
	"IP not reported by upstream": "上游未提供實際 IP",
	"Active":                      "連線中",
	"Closed":                      "已結束",
	"\n  %s · %s · Routing rule %s · Connect %.1f ms\n  Downloaded %s / Uploaded %s\n": "\n  %s · %s · 分流規則 %s · 建連 %.1f ms\n  累計下載 %s / 上傳 %s\n",
	"\n  No outbounds configured. Add one in the Web UI.\n":                            "\n  尚無出口，請在 Web UI 新增。\n",
	"Waiting for status": "等待狀態",
	"disabled":           "停用",
	"stopped":            "已停止",
	"ready":              "就緒",
	"connected":          "已連線",
	"disconnected":       "已中斷",
	"error":              "錯誤",
	"unavailable":        "無法使用",
	"not-ready":          "尚未就緒",
	"Running":            "執行中",
	"Stopped":            "已停止",
	"Starting":           "啟動中",
	"NeedsLogin":         "需要登入",
	"NeedsMachineAuth":   "需要核准裝置",
	"NoState":            "無狀態",
	"direct":             "直連",
	"  Version %s · Mode %s · Proxy listener %t\n": "  版本 %s · 模式 %s · Proxy 監聽 %t\n",
	"  Sign in: %s\n": "  登入：%s\n",
	"\n  WARP+ license key · %s\n\n  %s▏\n\n  Enter Apply   Esc Cancel\n  The key is masked and is not saved by this TUI.\n": "\n  WARP+ 授權碼 · %s\n\n  %s▏\n\n  Enter 套用   Esc 取消\n  授權碼會遮罩顯示，且不會由 TUI 儲存。\n",
	"\n  Install the Rillway background service\n\n  This creates a system service.\n  Enter Install   Esc Cancel\n":         "\n  安裝 Rillway 背景服務\n\n  這會建立系統服務。\n  Enter 安裝   Esc 取消\n",
	"\n  Create routing rule for %s\n\n": "\n  為 %s 建立分流規則\n\n",
	"  %s Adaptive routing\n\n  IP version: %s\n\n  ↑↓ Select outbound   f Change IP version   Enter Save   Esc Cancel\n": "  %s 自適應分流\n\n  IP 版本：%s\n\n  ↑↓ 選出口   f 切換 IP 版本   Enter 儲存   Esc 取消\n",
	"Automatic (dual stack)":                                 "自動（雙棧）",
	"Configuration saved. Applies to new connections only.":  "設定已儲存，只影響新連線。",
	"Outbound action completed.":                             "出口操作已完成。",
	"Background service installed.":                          "背景服務已安裝。",
	"service installation command is unavailable":            "無法取得服務安裝指令",
	"this connection has no destination":                     "這條連線沒有可用的目的地",
	"set up adaptive routing candidates in the Web UI first": "請先在 Web UI 設定自適應分流的候選出口",
	"management API (%d): %s":                                "管理 API（%d）：%s",
	"Management HTTPS URL":                                   "管理介面的 HTTPS 網址",
	"Management token file":                                  "管理權杖檔案",
	"Trusted certificate file":                               "受信任的憑證檔案",
	"Type (Left/Right to choose)":                            "類型（←→ 選擇）",
	"Name / ID":                                              "名稱／ID",
	"Enabled (Left/Right to change)":                         "啟用出口（←→ 切換）",
	"Public Internet (Left/Right to change)":                 "公開網際網路（←→ 切換）",
	"WARP proxy address":                                     "WARP 代理位址",
	"warp-cli command":                                       "warp-cli 指令",
	"WireGuard file on the server":                           "伺服器上的 WireGuard 設定檔",
	"DNS servers (optional)":                                 "DNS 伺服器（可留白）",
	"Tailscale node name":                                    "Tailscale 節點名稱",
	"Private state folder on the server":                     "伺服器上的私有狀態資料夾",
	"Auth key file (optional)":                               "驗證金鑰檔案（可留白）",
	"\n  Connect to a running Rillway service\n  This terminal manages the service; it does not start a proxy by itself.\n  For a VM, enter its HTTPS address and local token/certificate file paths.\n\n": "\n  連到已啟動的 Rillway 服務\n  這個終端介面用來管理服務，開啟介面不會自行啟動 Proxy。\n  若服務在 VM，請填 VM 的 HTTPS 網址，以及這台電腦上的權杖與憑證檔案。\n\n",
	"\n  Add an outbound · common values are already filled in\n  File paths below belong to the Rillway server.\n\n":                                                                                      "\n  新增出口・常用值已填好\n  下方的檔案路徑指的是 Rillway 伺服器。\n\n",
	"\n  Tab / ↑↓ Next field   Ctrl+U Clear field   Enter Save / Connect   Esc Cancel\n":                                                                                                                   "\n  Tab／↑↓ 換欄位   Ctrl+U 清空   Enter 儲存／連線   Esc 取消\n",
	"  Save first, then select this outbound and press n Register, c Connect, v Verify.\n":                                                                                                                 "  先儲存，選取出口後按 n 註冊、c 連線、v 驗證。\n",
	"  Provide your WireGuard file before enabling. No keys are generated.\n":                                                                                                                              "  啟用前請提供自己的 WireGuard 設定檔，這裡不會產生金鑰。\n",
	"  Enable and save to get a sign-in link. Public Internet access stays off.\n":                                                                                                                         "  啟用並儲存後會顯示登入連結；公開網際網路功能維持關閉。\n",
	"  Saving…\n": "  正在儲存…\n",
	"\n  Service unavailable: %s\n  No configuration has been loaded.\n  Press o to choose the running service on your VM or this machine.\n": "\n  目前連不到服務：%s\n  尚未載入設定。\n  按 o 選擇 VM 或這台電腦上已啟動的服務。\n",
	"  Press s to start an installed local service, or i to install one.\n":                                                                   "  按 s 啟動已安裝的本機服務，或按 i 安裝。\n",
	"  o Service connection   ? How to use\n":                                                                                                 "  o 服務連線設定   ? 使用說明\n",
	"  + Add outbound with suggested values\n":                                                                                                "  + 新增出口，常用值已填好\n",
	"Background service started.":                                                                                                             "背景服務已啟動。",
	"\n  Start the installed local background service?\n  Enter Start   Esc Cancel\n":                                                         "\n  要啟動已安裝的本機背景服務嗎？\n  Enter 啟動   Esc 取消\n",
	"\n  How to use Rillway\n\n  1. Press o to connect to a running service. A VM uses its own HTTPS address.\n  2. Tab to Outbounds. Press + to add; common values are filled in.\n     Select WARP, then n Register, c Connect and v Verify.\n  3. Set your browser HTTP proxy to %s, or use the PAC URL below.\n     http://%s/proxy.pac\n  4. Connections shows only traffic sent through this proxy.\n     Select a connection and press Enter to choose its future route.\n\n  Company services should bypass Rillway on your Mac.\n  Enter / Esc Close   Ctrl+L Language\n": "\n  Rillway 使用方式\n\n  1. 按 o 連到已啟動的服務，VM 使用自己的 HTTPS 網址。\n  2. 按 Tab 切到出口頁，按 + 新增，常用值已填好。\n     選取 WARP，再按 n 註冊、c 連線、v 驗證。\n  3. 瀏覽器的 HTTP Proxy 設為 %s，或使用下方 PAC 網址。\n     http://%s/proxy.pac\n  4. 連線總覽只顯示經過這個 Proxy 的流量。\n     選取連線後按 Enter，選擇它之後使用的出口。\n\n  Mac 的公司服務應略過 Rillway。\n  Enter／Esc 關閉   Ctrl+L 切換語言\n",
}

func (m model) text(source string) string {
	return i18n.Translate(m.locale, catalog, source)
}

func (m model) languageHelp() string {
	if m.form == "license" {
		return m.text("  Ctrl+L Language: English / Traditional Chinese\n")
	}
	return m.text("  L / Ctrl+L Language: English / Traditional Chinese\n")
}

func (m model) errorText(err error) string {
	var apiError *control.APIError
	if errors.As(err, &apiError) {
		source := apiError.Source
		if source == "" {
			// Older servers may provide only the already-localized public message.
			source = apiError.Message
		}
		return i18n.Format(m.locale, catalog, "management API (%d): %s", apiError.Status, i18n.Message(m.locale, source))
	}
	return m.text(i18n.Message(m.locale, err.Error()))
}
