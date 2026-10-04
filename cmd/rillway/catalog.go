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

  rillway init [--config FILE]              建立私有的本機設定
  rillway serve [--config FILE]             執行 HTTP、SOCKS5、HTTPS 管理介面與 PAC
  rillway tui [--config FILE]               終端管理介面；按 i 安裝服務
  rillway tui --url URL --token-file FILE --ca PEM
  rillway service install|start|stop|restart|status|uninstall [--config FILE]
  rillway client list                      列出 macOS 網路服務
  rillway client apply --service Wi-Fi --pac-url URL --backup FILE
  rillway client restore --backup FILE
  rillway pac --config FILE                將 PAC 輸出至標準輸出
  rillway diagnose --config FILE --outbound direct [--family ipv4|ipv6]
  rillway diagnose --config FILE --outbound warp --download-url HTTPS_URL
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
	locale, args, err := languageArguments(args, os.Getenv("RILLWAY_LANG"))
	ctx = i18n.WithLocale(ctx, locale)
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
		case "config", "outbound", "family", "download-url", "url", "token-file", "ca", "service", "pac-url", "backup":
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
