package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/i18n"
	"rillway/internal/platform"
	"runtime"
	"strconv"
	"strings"
)

type setupDependencies struct {
	check    func() error
	validate func(context.Context, config.Config) error
	install  func(context.Context, string, io.Reader, io.Writer) error
}

func installFromSetup(ctx context.Context, path string, input io.Reader, out io.Writer) error {
	if runtime.GOOS == "linux" && os.Geteuid() != 0 {
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		command := exec.CommandContext(ctx, "sudo", binary, "--lang", string(i18n.FromContext(ctx)), "service", "install", "--config", path)
		command.Stdin = input
		command.Stdout = out
		command.Stderr = out
		return command.Run()
	}
	result, err := platform.Service(ctx, "install", path)
	if result != "" {
		_, _ = fmt.Fprintln(out, result)
	}
	return err
}

func setupCommand(ctx context.Context, args []string, input io.Reader, out io.Writer, deps setupDependencies) error {
	fs := localizedFlags(ctx, "setup", out)
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	path := fs.String("config", filepath.Join(dir, "rillway", "config.json"), cliText(ctx, "new setup configuration file"))
	listen := fs.String("listen", "127.0.0.1", cliText(ctx, "specific local IPv4 or IPv6 address; no wildcard"))
	clients := fs.String("allow-client", "", cliText(ctx, "allowed client IPs or CIDRs, comma-separated"))
	bypass := fs.String("bypass-domains", "local,ts.net,tailscale.com", cliText(ctx, "company bypass domains, comma-separated"))
	ports := []*int{
		fs.Int("http-port", 17890, cliText(ctx, "HTTP Proxy port")),
		fs.Int("socks-port", 17891, cliText(ctx, "SOCKS5 port")),
		fs.Int("admin-port", 17892, cliText(ctx, "HTTPS management port")),
		fs.Int("pac-port", 17893, cliText(ctx, "PAC port")),
	}
	yes := fs.Bool("yes", false, cliText(ctx, "use supplied options without prompts; new installations only"))
	noInstall := fs.Bool("no-install", false, cliText(ctx, "create configuration and credentials without installing a service"))
	if err = fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("setup does not accept positional arguments")
	}
	abs, err := filepath.Abs(*path)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(abs); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return errors.New(cliFormat(ctx, "configuration already exists: %s", abs))
	}
	if !*noInstall {
		if err = deps.check(); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintln(out, cliText(ctx, "Rillway setup — single binary, private configuration, background service."))
	reader := bufio.NewScanner(input)
	reader.Buffer(make([]byte, 1024), 8192)
	ask := func(label, current string) (string, error) {
		if *yes {
			return current, nil
		}
		_, _ = fmt.Fprintf(out, "%s [%s]: ", cliText(ctx, label), current)
		type answer struct {
			line string
			err  error
		}
		done := make(chan answer, 1)
		go func() {
			if reader.Scan() {
				done <- answer{reader.Text(), nil}
			} else {
				readErr := reader.Err()
				if readErr == nil {
					readErr = io.EOF
				}
				done <- answer{"", readErr}
			}
		}()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case a := <-done:
			if a.err != nil {
				return "", errors.New(cliText(ctx, "Setup input ended. Run interactively or supply --yes with explicit options."))
			}
			if len(a.line) > 8192 {
				return "", errors.New(cliText(ctx, "Setup input is too long."))
			}
			value := strings.TrimSpace(a.line)
			if value == "" {
				value = current
			}
			return value, nil
		}
	}
	if *listen, err = ask("Listener IP address", *listen); err != nil {
		return err
	}
	ip, err := netip.ParseAddr(*listen)
	if err != nil || ip.Zone() != "" || ip.Unmap().IsUnspecified() || ip.IsMulticast() {
		return errors.New(cliText(ctx, "Choose a specific local IPv4 or IPv6 address, not a wildcard."))
	}
	if *clients == "" {
		*clients = ip.String()
	}
	if *clients, err = ask("Allowed client IPs or CIDRs (comma-separated)", *clients); err != nil {
		return err
	}
	c := config.Default(filepath.Join(filepath.Dir(abs), "state"))
	c.Security.AllowedClients = []string{"127.0.0.0/8", "::1/128"}
	for _, value := range append(strings.Split(*clients, ","), ip.String()) {
		value = strings.TrimSpace(value)
		if client, parseErr := netip.ParseAddr(value); parseErr == nil {
			value = netip.PrefixFrom(client, client.BitLen()).String()
		}
		if _, parseErr := netip.ParsePrefix(value); parseErr != nil {
			return errors.New(cliText(ctx, "Allowed clients must be IP addresses or CIDRs."))
		}
		if !containsString(c.Security.AllowedClients, value) {
			c.Security.AllowedClients = append(c.Security.AllowedClients, value)
		}
	}
	labels := []string{"HTTP Proxy port", "SOCKS5 port", "HTTPS management port", "PAC port"}
	addresses := make([]string, len(ports))
	for i, port := range ports {
		value, askErr := ask(labels[i], strconv.Itoa(*port))
		if askErr != nil {
			return askErr
		}
		n, parseErr := strconv.Atoi(value)
		if parseErr != nil || n < 1024 || n > 65535 {
			return errors.New(cliText(ctx, "Setup ports must be between 1024 and 65535."))
		}
		addresses[i] = net.JoinHostPort(ip.String(), value)
	}
	c.Listeners = config.Listeners{HTTP: addresses[0], SOCKS5: addresses[1], Admin: addresses[2], PAC: addresses[3]}
	c.PAC.ProxyAddress = addresses[0]
	if *bypass, err = ask("Company bypass domains (comma-separated)", *bypass); err != nil {
		return err
	}
	for _, d := range strings.Split(*bypass, ",") {
		if d = strings.TrimSpace(d); d != "" {
			found := false
			for i := range c.PAC.BypassDomains {
				if strings.EqualFold(c.PAC.BypassDomains[i].Value, d) {
					c.PAC.BypassDomains[i].Enabled = true
					found = true
					break
				}
			}
			if !found {
				c.PAC.BypassDomains = append(c.PAC.BypassDomains, config.PACBypass{Value: d, Enabled: true, Note: "Company service"})
			}
		}
	}
	if err = config.Validate(c); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, cliText(ctx, "Review setup:"))
	_, _ = fmt.Fprintf(out, "  HTTP: %s\n  SOCKS5: %s\n  HTTPS: https://%s\n  PAC: http://%s/proxy.pac\n", c.Listeners.HTTP, c.Listeners.SOCKS5, c.Listeners.Admin, c.Listeners.PAC)
	_, _ = fmt.Fprintln(out, cliFormat(ctx, "Allowed clients: %s", strings.Join(c.Security.AllowedClients, ", ")))
	_, _ = fmt.Fprintln(out, cliFormat(ctx, "Configuration: %s", abs))
	if !*noInstall && runtime.GOOS == "linux" {
		_, _ = fmt.Fprintln(out, cliText(ctx, "Linux installation: /etc/rillway/config.json; /var/lib/rillway; /usr/local/bin/rillway; rillway.service (user rillway)."))
	}
	_, _ = fmt.Fprintln(out, cliText(ctx, "WARP stays disabled. Its fixed GitHub CDN rules fail until you configure WARP or explicitly change those rules."))
	confirm, err := ask("Create configuration and continue? Type yes", "no")
	if err != nil {
		return err
	}
	if !*yes && !strings.EqualFold(confirm, "yes") {
		_, err = fmt.Fprintln(out, cliText(ctx, "Setup cancelled; no files or services were changed."))
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if !*noInstall && deps.validate != nil {
		if err = deps.validate(ctx, c); err != nil {
			return err
		}
	}
	if err = config.Save(abs, c); err != nil {
		return err
	}
	_, fingerprint, err := platform.EnsureCredentials(c)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, cliFormat(ctx, "TLS certificate SHA-256: %s", fingerprint))
	if *noInstall {
		_, _ = fmt.Fprintln(out, cliFormat(ctx, "Management token file: %s", c.Security.AdminTokenFile))
		_, err = fmt.Fprintln(out, cliText(ctx, "Configuration ready. Use serve --config with this file to run in the foreground."))
		return err
	}
	if err = deps.install(ctx, abs, input, out); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		_, _ = fmt.Fprintln(out, cliText(ctx, "Installed. Effective configuration: /etc/rillway/config.json; token: /var/lib/rillway/admin.token."))
	} else {
		_, _ = fmt.Fprintln(out, cliFormat(ctx, "Management token file: %s", c.Security.AdminTokenFile))
	}
	_, err = fmt.Fprintln(out, cliFormat(ctx, "Open https://%s after verifying the certificate fingerprint. Read the private token file locally to sign in.", c.Listeners.Admin))
	return err
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
