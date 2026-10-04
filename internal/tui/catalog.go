package tui

import (
	"errors"
	"rillway/internal/control"
	"rillway/internal/i18n"
)

// English messages are stable keys; dynamic names and IDs are never translated.
var catalog = map[string]string{
	"  Ctrl+L Language: English / Traditional Chinese\n":        "  Ctrl+L 語言：English / 繁體中文\n",
	"\n  ≈ Rillway   Routing console\n\n":                       "\n  ≈ Rillway   網路分流控制台\n\n",
	"Connections":                                               "連線總覽",
	"Outbounds & VPNs":                                          "出口與 VPN",
	"Service settings":                                          "服務設定",
	"\n\n  Adaptive routing: %s   Configuration revision: %d\n": "\n\n  自適應分流：%s   設定版本：%d\n",
	"Enabled":  "啟用",
	"Disabled": "停用",
	"\n  Connecting to the management service…\n": "\n  正在連接管理服務…\n",
	"\n  HTTP proxy    %s\n  SOCKS5        %s\n  Management UI %s\n  PAC           %s\n\n  Use PAC bypass for company services on Mac to keep using local Tailscale.\n  Press i to install the background service.\n  Edit PAC and all routing rules in the Web UI.\n": "\n  HTTP Proxy    %s\n  SOCKS5        %s\n  管理介面      %s\n  PAC           %s\n\n  Mac 的公司服務請設為 PAC 略過，繼續使用本機 Tailscale。\n  按 i 安裝背景服務。\n  PAC 與完整分流規則可在 Web UI 編輯。\n",
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
