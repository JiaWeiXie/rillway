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
	"rillway/internal/notices"
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
	return installedConfigPath(runtime.GOOS, []string{"/etc/rillway/config.json", "/var/lib/rillway/config.json"}, filepath.Join(dir, "rillway", "config.json"))
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
	case "setup":
		return setupCommand(ctx, args, os.Stdin, out, setupDependencies{check: platform.CheckNewInstallation, validate: platform.CheckListeners, install: installFromSetup})
	case "licenses":
		fs := localizedFlags(ctx, command, out)
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("licenses does not accept positional arguments")
		}
		_, err := io.WriteString(out, notices.Text)
		return err
	case "version", "--version":
		fs := localizedFlags(ctx, "version", out)
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("version does not accept positional arguments")
		}
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
	clientPath := fs.String("client-config", defaultClientPath(), cliText(ctx, "remembered TUI connection file"))
	if err := fs.Parse(args); err != nil {
		return err
	}
	explicitConfig := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			explicitConfig = true
		}
	})
	if *base == "" && !explicitConfig {
		remembered, e := loadClientProfile(*clientPath)
		if e == nil {
			*base = remembered.BaseURL
			if *tokenPath == "" {
				*tokenPath = remembered.TokenFile
			}
			if *ca == "" {
				*ca = remembered.CAFile
			}
		} else if !os.IsNotExist(e) {
			return fmt.Errorf("read TUI connection profile: %w", e)
		}
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
	if !local && *tokenPath == "" {
		*tokenPath = filepath.Join(filepath.Dir(*clientPath), "remote-admin.token")
	}
	token, tokenErr := os.ReadFile(*tokenPath)
	if tokenErr != nil {
		tokenErr = tokenReadError(*tokenPath, tokenErr)
	}
	options := tui.Options{BaseURL: *base, Token: strings.TrimSpace(string(token)), TokenFile: *tokenPath, CAFile: *ca, Locale: i18n.FromContext(ctx), InitialError: tokenErr, RememberConnection: func(settings tui.ConnectionSettings) error { return saveClientProfile(*clientPath, settings) }}
	if local {
		binary, e := os.Executable()
		if e != nil {
			return e
		}
		absolute, e := filepath.Abs(*path)
		if e != nil {
			return e
		}
		options.StartCommand = func() *exec.Cmd {
			args := []string{binary, "--lang", string(i18n.FromContext(ctx)), "service", "start", "--config", absolute}
			if runtime.GOOS == "linux" && os.Geteuid() != 0 {
				return exec.CommandContext(ctx, "sudo", args...)
			}
			return exec.CommandContext(ctx, args[0], args[1:]...)
		}
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
  rillway setup [--config FILE]             Guided first installation
  rillway setup --yes --listen IP --allow-client IP_OR_CIDR
  rillway setup --no-install --config FILE  Prepare without installing a service
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
  rillway licenses                         Print embedded third-party notices
  rillway version

Linux service installation requires sudo; the daemon runs as the rillway user.
macOS service installation uses the current user's LaunchAgent.
WARP accounts and existing host VPN routes are never changed by init or serve.
`
