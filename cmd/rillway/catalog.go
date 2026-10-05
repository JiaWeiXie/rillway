package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"rillway/internal/i18n"
	"strings"
)

var catalog = map[string]string{
	"Invalid language. Use en or zh-Hant.":                                                                                         "語言無效。請使用 en 或 zh-Hant。",
	"Could not write JSON output.":                                                                                                 "無法寫入 JSON 輸出。",
	"Management access denied. Check the token and allowed client address.":                                                        "管理介面拒絕存取。請確認權杖與允許的用戶端位址。",
	"Configuration changed elsewhere. Fetch it again and review a new plan.":                                                       "設定已被其他操作修改。請重新讀取並預覽新的變更。",
	"The server rejected the operation. Check settings in the Web UI.":                                                             "伺服器拒絕此操作。請在 Web UI 確認設定。",
	"Management connection failed. Check the service, address and trusted certificate. For writes, inspect state before retrying.": "管理連線失敗。請確認服務、位址與受信任憑證。寫入操作請先確認目前狀態，再決定是否重試。",
	"Use rillway agent schema to discover commands.":                                                                               "請用 rillway agent schema 查詢可用指令。",
	"Invalid arguments. Use rillway agent schema.":                                                                                 "參數無效。請用 rillway agent schema 查詢用法。",
	"This operation requires --yes. Review its effects first.":                                                                     "此操作需要 --yes。請先確認影響範圍。",
	"Choose an outbound ID and connect, disconnect or verify.":                                                                     "請指定出口 ID，並選擇 connect、disconnect 或 verify。",
	"Provide one configuration JSON object with --input FILE or --input - (maximum 256 KiB).":                                      "請用 --input FILE 或 --input - 提供一份設定 JSON 物件（上限 256 KiB）。",
	"Configuration validation failed. Check the documented configuration fields.":                                                  "設定驗證失敗。請依文件確認設定欄位。",
	"Edit listener and security settings on the server, then restart the service.":                                                 "請在伺服器上修改監聽與安全設定，再重新啟動服務。",
	"Cannot read local configuration. Select --config or an explicit remote --url and --token-file.":                               "無法讀取本機設定。請指定 --config，或明確指定遠端 --url 與 --token-file。",
	"Cannot read a valid management token from --token-file or the local configuration.":                                           "無法從 --token-file 或本機設定讀取有效的管理權杖。",
	"Invalid management URL or trusted certificate. Remote management requires HTTPS.":                                             "管理網址或受信任憑證無效。遠端管理必須使用 HTTPS。",
	"licenses does not accept positional arguments":                                                                                "licenses 不接受位置參數",
	"version does not accept positional arguments":                                                                                 "version 不接受位置參數",
	"new setup configuration file":                                                                                                 "首次安裝的暫存設定檔",
	"specific local IPv4 or IPv6 address; no wildcard":                                                                             "指定本機 IPv4 或 IPv6 位址；不可使用萬用位址",
	"allowed client IPs or CIDRs, comma-separated":                                                                                 "允許的用戶端 IP 或 CIDR，以逗號分隔",
	"company bypass domains, comma-separated":                                                                                      "公司略過網域，以逗號分隔",
	"HTTP Proxy port":       "HTTP Proxy 連接埠",
	"SOCKS5 port":           "SOCKS5 連接埠",
	"HTTPS management port": "HTTPS 管理介面連接埠",
	"PAC port":              "PAC 連接埠",
	"use supplied options without prompts; new installations only":                "使用指定選項，略過提示；僅適用首次安裝",
	"create configuration and credentials without installing a service":           "建立設定與憑證，不安裝服務",
	"setup does not accept positional arguments":                                  "setup 不接受位置參數",
	"Rillway setup — single binary, private configuration, background service.":   "Rillway 引導安裝 — 單一執行檔、私有設定與背景服務。",
	"Setup input ended. Run interactively or supply --yes with explicit options.": "安裝輸入已結束。請使用互動模式，或加上 --yes 並指定選項。",
	"Setup input is too long.":                                                    "安裝輸入過長。",
	"Listener IP address":                                                         "監聽 IP 位址",
	"Choose a specific local IPv4 or IPv6 address, not a wildcard.":               "請選擇指定的本機 IPv4 或 IPv6 位址，不可使用萬用位址。",
	"Allowed client IPs or CIDRs (comma-separated)":                               "允許的用戶端 IP 或 CIDR（以逗號分隔）",
	"Allowed clients must be IP addresses or CIDRs.":                              "允許的用戶端必須是 IP 位址或 CIDR。",
	"Setup ports must be between 1024 and 65535.":                                 "安裝連接埠必須介於 1024 與 65535 之間。",
	"Company bypass domains (comma-separated)":                                    "公司略過網域（以逗號分隔）",
	"Review setup:":       "確認安裝設定：",
	"Allowed clients: %s": "允許的用戶端：%s",
	"Configuration: %s":   "設定檔：%s",
	"Linux installation: /etc/rillway/config.json; /var/lib/rillway; /usr/local/bin/rillway; rillway.service (user rillway).": "Linux 安裝位置：/etc/rillway/config.json；/var/lib/rillway；/usr/local/bin/rillway；rillway.service（使用者 rillway）。",
	"WARP stays disabled. Its fixed GitHub CDN rules fail until you configure WARP or explicitly change those rules.":         "WARP 維持停用。請設定 WARP 或手動修改其固定 GitHub CDN 規則，否則這些規則的連線會失敗。",
	"Create configuration and continue? Type yes":                                                                                          "建立設定並繼續？請輸入 yes",
	"Setup cancelled; no files or services were changed.":                                                                                  "已取消安裝；未變更檔案或服務。",
	"TLS certificate SHA-256: %s":                                                                                                          "TLS 憑證 SHA-256：%s",
	"Management token file: %s":                                                                                                            "管理權杖檔案：%s",
	"Configuration ready. Use serve --config with this file to run in the foreground.":                                                     "設定已完成。請用 serve --config 指定此檔案，在前景執行。",
	"Installed. Effective configuration: /etc/rillway/config.json; token: /var/lib/rillway/admin.token.":                                   "安裝完成。有效設定：/etc/rillway/config.json；權杖：/var/lib/rillway/admin.token。",
	"Open https://%s after verifying the certificate fingerprint. Read the private token file locally to sign in.":                         "核對憑證指紋後開啟 https://%s。請在本機讀取私有權杖檔案，用於登入。",
	"Rillway is already installed or has retained files. Back up the existing installation and update its binary instead of reinstalling.": "Rillway 已安裝或有保留檔案。請備份現有安裝並更新執行檔，勿重新安裝。",
	"\nService removed; account, /etc/rillway and /var/lib/rillway retained.":                                                              "\n服務已移除；保留帳號、/etc/rillway 與 /var/lib/rillway。",
	"docker requires export":                                     "docker 需要指定 export",
	"export format: daemon, client, env or compose":              "匯出格式：daemon、client、env 或 compose",
	"Docker-reachable Rillway HTTP Proxy URL":                    "Docker 能連線的 Rillway HTTP Proxy 網址",
	"comma-separated Docker bypass domains, IPs or CIDRs":        "以逗號分隔的 Docker 略過網域、IP 或 CIDR",
	"existing Docker JSON to merge; source file is not modified": "要合併的現有 Docker JSON；來源檔案維持原狀",

	"remembered TUI connection file":   "記住 TUI 連線的檔案",
	"configuration file":               "設定檔",
	"configuration already exists: %s": "設定檔已存在：%s",
	"Configuration: %s\nManagement token: %s\nWARP is disabled until explicitly enabled and connected.\n": "設定檔：%s\n管理權杖：%s\nWARP 預設停用，請手動啟用並連線。\n",
	"service requires install, start, stop, restart, status or uninstall":                                 "service 需要指定 install、start、stop、restart、status 或 uninstall",
	"configured outbound ID": "已設定的出口 ID",
	"auto, ipv4 or ipv6":     "auto、ipv4 或 ipv6",
	"explicit HTTPS download test URL (max 4 MiB, no redirects)": "手動下載測試的 HTTPS 網址（上限 4 MiB，不跟隨重新導向）",
	"unknown command %q; use rillway help":                       "不明指令 %q；請執行 rillway help",
	"management HTTPS URL":                                       "管理介面的 HTTPS 網址",
	"management token file":                                      "管理權杖檔案",
	"trusted server certificate PEM":                             "受信任的伺服器憑證 PEM",
	"--token-file is required for remote management":             "遠端管理需要指定 --token-file",
	"client network settings are available on macOS only":        "用戶端網路設定僅支援 macOS",
	"client requires list, apply or restore":                     "client 需要指定 list、apply 或 restore",
	"macOS network service name":                                 "macOS 網路服務名稱",
	"Ubuntu PAC URL":                                             "Ubuntu 的 PAC 網址",
	"restoration snapshot":                                       "還原快照",
	"unknown client action":                                      "不明的用戶端操作",
	"language: en or zh-Hant (overrides RILLWAY_LANG)":           "語言：en 或 zh-Hant（優先於 RILLWAY_LANG）",
	"--lang requires en or zh-Hant":                              "--lang 需要指定 en 或 zh-Hant",
	"unsupported language %q; use en or zh-Hant":                 "不支援語言 %q；請使用 en 或 zh-Hant",
	"Usage: rillway %s [options]\n":                              "用法：rillway %s [選項]\n",
	" (default: %s)":                                             "（預設：%s）",
	"unknown option: %s":                                         "不明選項：%s",
	"option requires a value: %s":                                "選項需要指定值：%s",
	usage: `Rillway — 可觀察的網路分流 Proxy

  rillway [--lang en|zh-Hant] COMMAND [選項]
  語言預設採用 RILLWAY_LANG，未設定時使用英文。TUI：按 L 或 Ctrl+L 切換語言。
  輸入文字時請使用 Ctrl+L。

  rillway setup [--config FILE]             引導首次安裝
  rillway setup --yes --listen IP --allow-client IP_OR_CIDR
  rillway setup --no-install --config FILE  只建立設定，不安裝服務
  rillway init [--config FILE]              建立私有的本機設定
  rillway serve [--config FILE]             執行 HTTP、SOCKS5、HTTPS 管理介面與 PAC
  rillway tui [--config FILE]               終端管理介面；按 i 安裝服務
  rillway tui --url URL --token-file FILE --ca PEM
  rillway service install|start|stop|restart|status|uninstall [--config FILE]
  rillway client list                      列出 macOS 網路服務
  rillway client apply --service Wi-Fi --pac-url URL --backup FILE
  rillway client restore --backup FILE
  rillway pac --config FILE                將 PAC 輸出至標準輸出
  rillway agent schema                    查詢非互動 JSON 指令
  rillway agent status [--config FILE]     讀取目前服務狀態
  rillway docker export --target daemon|client|env|compose --config FILE
  rillway docker export --target client --proxy-url http://HOST:PORT [--input FILE]
  rillway diagnose --config FILE --outbound direct [--family ipv4|ipv6]
  rillway diagnose --config FILE --outbound warp --download-url HTTPS_URL
  rillway licenses                         輸出內建第三方授權說明
  rillway version

Linux 安裝服務需要 sudo；背景服務以 rillway 使用者執行。
macOS 安裝服務會使用目前使用者的 LaunchAgent。
init 與 serve 不會變更 WARP 帳號或主機既有的 VPN 路由。
`,
}

func cliText(ctx context.Context, source string) string {
	return i18n.Translate(i18n.FromContext(ctx), catalog, source)
}

func cliFormat(ctx context.Context, source string, args ...any) string {
	return i18n.Format(i18n.FromContext(ctx), catalog, source, args...)
}

type localizedError struct {
	error
	message string
}

func (e localizedError) Error() string { return e.message }
func (e localizedError) Unwrap() error { return e.error }

func run(ctx context.Context, args []string, out io.Writer) error {
	original := args
	locale, args, err := languageArguments(args, os.Getenv("RILLWAY_LANG"))
	ctx = i18n.WithLocale(ctx, locale)
	if err != nil && agentInvocation(original) {
		return writeAgentResult(ctx, nil, agentFail("invalid_language", "Invalid language. Use en or zh-Hant.", 2), out)
	}
	if err == nil {
		err = runLocalized(ctx, args, out)
	}
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err == nil {
		return nil
	}
	message := cliText(ctx, i18n.Message(locale, err.Error()))
	for prefix, source := range map[string]string{
		"flag provided but not defined: ": "unknown option: %s",
		"flag needs an argument: ":        "option requires a value: %s",
	} {
		if value, ok := strings.CutPrefix(err.Error(), prefix); ok {
			message = cliFormat(ctx, source, value)
		}
	}
	if message != err.Error() {
		return localizedError{error: err, message: message}
	}
	return err
}

func agentInvocation(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--lang" || arg == "-lang" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--lang=") || strings.HasPrefix(arg, "-lang=") {
			continue
		}
		return arg == "agent"
	}
	return false
}

// Language is global, including after a subcommand. Preserve all other values
// byte-for-byte so paths, names and IDs never become language options.
func languageArguments(args []string, preference string) (i18n.Locale, []string, error) {
	locale := i18n.Parse(preference)
	remaining := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			remaining = append(remaining, args[i:]...)
			break
		}
		if arg == "--lang" || arg == "-lang" || strings.HasPrefix(arg, "--lang=") || strings.HasPrefix(arg, "-lang=") {
			_, value, hasValue := strings.Cut(arg, "=")
			if !hasValue {
				if i+1 == len(args) {
					return locale, nil, errors.New(i18n.Translate(locale, catalog, "--lang requires en or zh-Hant"))
				}
				i++
				value = args[i]
			}
			if !strings.EqualFold(value, "en") && !strings.EqualFold(value, "zh-Hant") {
				return locale, nil, errors.New(i18n.Format(locale, catalog, "unsupported language %q; use en or zh-Hant", value))
			}
			locale = i18n.Parse(value)
			continue
		}
		remaining = append(remaining, arg)
		name := strings.TrimLeft(arg, "-")
		switch name {
		case "client-config", "config", "outbound", "family", "download-url", "url", "token-file", "ca", "service", "pac-url", "backup", "target", "proxy-url", "no-proxy", "input", "listen", "allow-client", "bypass-domains", "http-port", "socks-port", "admin-port", "pac-port", "timeout", "id", "action":
			if strings.HasPrefix(arg, "-") && i+1 < len(args) {
				i++
				remaining = append(remaining, args[i])
			}
		}
	}
	return locale, remaining, nil
}

func localizedFlags(ctx context.Context, name string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("lang", string(i18n.FromContext(ctx)), cliText(ctx, "language: en or zh-Hant (overrides RILLWAY_LANG)"))
	fs.Usage = func() {
		_, _ = fmt.Fprint(out, cliFormat(ctx, "Usage: rillway %s [options]\n", name))
		fs.VisitAll(func(f *flag.Flag) {
			value, description := flag.UnquoteUsage(f)
			_, _ = fmt.Fprintf(out, "  -%s %s\n      %s", f.Name, value, description)
			if f.DefValue != "" {
				_, _ = fmt.Fprint(out, cliFormat(ctx, " (default: %s)", f.DefValue))
			}
			_, _ = fmt.Fprintln(out)
		})
	}
	return fs
}
