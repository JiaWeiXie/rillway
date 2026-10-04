package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"rillway/internal/app"
	"rillway/internal/config"
	"rillway/internal/diagnostic"
	"rillway/internal/i18n"
	"rillway/internal/platform"
	"rillway/internal/tui"
	"runtime"
	"strings"
	"syscall"
)

var version = "0.1.0-dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "rillway:", err)
		os.Exit(1)
	}
}

func defaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ".local/config.json"
	}
	return filepath.Join(dir, "rillway", "config.json")
}

func flags(ctx context.Context, name string, out io.Writer) (*flag.FlagSet, *string) {
	fs := localizedFlags(ctx, name, out)
	return fs, fs.String("config", defaultPath(), cliText(ctx, "configuration file"))
}

func initialize(ctx context.Context, path string) (config.Config, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return config.Config{}, err
	}
	if _, err = os.Stat(path); err == nil {
		return config.Config{}, errors.New(cliFormat(ctx, "configuration already exists: %s", path))
	} else if !os.IsNotExist(err) {
		return config.Config{}, err
	}
	c := config.Default(filepath.Join(filepath.Dir(path), "state"))
	if err = config.Save(path, c); err != nil {
		return c, err
	}
	_, _, err = platform.EnsureCredentials(c)
	return c, err
}

func runLocalized(ctx context.Context, args []string, out io.Writer) error {
	command := "tui"
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}
	switch command {
	case "version", "--version":
		_, err := fmt.Fprintln(out, "Rillway", version)
		return err
	case "help", "--help", "-h":
		_, err := io.WriteString(out, cliText(ctx, usage))
		return err
	case "init":
		fs, path := flags(ctx, command, out)
		if err := fs.Parse(args); err != nil {
			return err
		}
		c, err := initialize(ctx, *path)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, cliText(ctx, "Configuration: %s\nManagement token: %s\nWARP is disabled until explicitly enabled and connected.\n"), *path, c.Security.AdminTokenFile)
		return err
	case "serve":
		fs, path := flags(ctx, command, out)
		if err := fs.Parse(args); err != nil {
			return err
		}
		c, err := config.Load(*path)
		if os.IsNotExist(err) {
			c, err = initialize(ctx, *path)
		}
		if err != nil {
			return err
		}
		return app.Serve(ctx, *path, c, func(s string) { _, _ = fmt.Fprintln(out, s) })
	case "tui":
		return terminal(ctx, args, out)
	case "service":
		if len(args) == 0 {
			return errors.New("service requires install, start, stop, restart, status or uninstall")
		}
		action := args[0]
		fs, path := flags(ctx, command, out)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		absolute, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		result, err := platform.Service(ctx, action, absolute)
		if result != "" {
			_, _ = fmt.Fprintln(out, result)
		}
		return err
	case "client":
		return clientCommand(ctx, args, out)
	case "docker":
		return dockerCommand(ctx, args, out)
	case "pac":
		fs, path := flags(ctx, command, out)
		if err := fs.Parse(args); err != nil {
			return err
		}
		c, err := config.Load(*path)
		if err != nil {
			return err
		}
		body, err := platform.PAC(c.PAC)
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, body)
		return err
	case "diagnose":
		fs, path := flags(ctx, command, out)
		selected := fs.String("outbound", "direct", cliText(ctx, "configured outbound ID"))
		family := fs.String("family", "auto", cliText(ctx, "auto, ipv4 or ipv6"))
		download := fs.String("download-url", "", cliText(ctx, "explicit HTTPS download test URL (max 4 MiB, no redirects)"))
		if err := fs.Parse(args); err != nil {
			return err
		}
		c, err := config.Load(*path)
		if err != nil {
			return err
		}
		report, err := diagnostic.Run(ctx, c, *selected, *family, *download)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	default:
		return errors.New(cliFormat(ctx, "unknown command %q; use rillway help", command))
	}
}

func terminal(ctx context.Context, args []string, out io.Writer) error {
	fs, path := flags(ctx, "tui", out)
	base := fs.String("url", "", cliText(ctx, "management HTTPS URL"))
	tokenPath := fs.String("token-file", "", cliText(ctx, "management token file"))
	ca := fs.String("ca", "", cliText(ctx, "trusted server certificate PEM"))
	if err := fs.Parse(args); err != nil {
		return err
	}
	local := *base == ""
	c, err := config.Load(*path)
	if local && os.IsNotExist(err) {
		c, err = initialize(ctx, *path)
	}
	if local && err != nil {
		return err
	}
	if local {
		host, port, _ := net.SplitHostPort(c.Listeners.Admin)
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		*base = "https://" + net.JoinHostPort(host, port)
		if *tokenPath == "" {
			*tokenPath = c.Security.AdminTokenFile
		}
		if *ca == "" {
			*ca = c.Security.TLSCertFile
		}
	}
	if *tokenPath == "" {
		return errors.New("--token-file is required for remote management")
	}
	token, err := os.ReadFile(*tokenPath)
	if err != nil {
		return err
	}
	options := tui.Options{BaseURL: *base, Token: strings.TrimSpace(string(token)), CAFile: *ca, Locale: i18n.FromContext(ctx)}
	if local {
		options.InstallService = func(ctx context.Context) error {
			absolute, e := filepath.Abs(*path)
			if e != nil {
				return e
			}
			_, e = platform.Service(ctx, "install", absolute)
			return e
		}
		if runtime.GOOS == "linux" && os.Geteuid() != 0 {
			absolute, e := filepath.Abs(*path)
			if e != nil {
				return e
			}
			binary, e := os.Executable()
			if e != nil {
				return e
			}
			options.InstallCommand = func() *exec.Cmd {
				return exec.CommandContext(ctx, "sudo", binary, "--lang", string(i18n.FromContext(ctx)), "service", "install", "--config", absolute)
			}
		}
	}
	return tui.Run(ctx, options)
}

func clientCommand(ctx context.Context, args []string, out io.Writer) error {
	if runtime.GOOS != "darwin" {
		return errors.New("client network settings are available on macOS only")
	}
	if len(args) == 0 {
		return errors.New("client requires list, apply or restore")
	}
	action := args[0]
	fs := localizedFlags(ctx, "client", out)
	service := fs.String("service", "Wi-Fi", cliText(ctx, "macOS network service name"))
	pacURL := fs.String("pac-url", "", cliText(ctx, "Ubuntu PAC URL"))
	backup := fs.String("backup", "rillway-proxy-backup.json", cliText(ctx, "restoration snapshot"))
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch action {
	case "list":
		b, err := platform.Run(ctx, "/usr/sbin/networksetup", "-listallnetworkservices")
		if err == nil {
			_, err = out.Write(b)
		}
		return err
	case "apply":
		return platform.ApplyPAC(ctx, platform.Run, *service, *pacURL, *backup)
	case "restore":
		return platform.RestoreProxy(ctx, platform.Run, *backup)
	default:
		return errors.New("unknown client action")
	}
}

const usage = `Rillway — observable split proxy

  rillway [--lang en|zh-Hant] COMMAND [options]
  Language defaults to RILLWAY_LANG, or English. TUI: L / Ctrl+L switches language.
  While entering text, use Ctrl+L.

  rillway init [--config FILE]              Create private local configuration
  rillway serve [--config FILE]             Run HTTP, SOCKS5, HTTPS management UI and PAC
  rillway tui [--config FILE]               Terminal management; i installs service
  rillway tui --url URL --token-file FILE --ca PEM
  rillway service install|start|stop|restart|status|uninstall [--config FILE]
  rillway client list                      List macOS network services
  rillway client apply --service Wi-Fi --pac-url URL --backup FILE
  rillway client restore --backup FILE
  rillway pac --config FILE                Export PAC to stdout
  rillway docker export --target daemon|client|env|compose --config FILE
  rillway docker export --target client --proxy-url http://HOST:PORT [--input FILE]
  rillway diagnose --config FILE --outbound direct [--family ipv4|ipv6]
  rillway diagnose --config FILE --outbound warp --download-url HTTPS_URL
  rillway version

Linux service installation requires sudo; the daemon runs as the rillway user.
macOS service installation uses the current user's LaunchAgent.
WARP accounts and existing host VPN routes are never changed by init or serve.
`
