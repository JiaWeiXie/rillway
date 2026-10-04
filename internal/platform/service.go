package platform

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"rillway/internal/config"
	"runtime"
	"strconv"
	"strings"
)

type Runner func(context.Context, string, ...string) ([]byte, error)

func Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	b, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return b, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func SystemdUnit(binary, configPath string) (string, error) {
	for _, p := range []string{binary, configPath} {
		if !filepath.IsAbs(p) || strings.ContainsAny(p, "\n\r\x00") {
			return "", errors.New("service paths must be absolute")
		}
	}
	quote := func(s string) string { return strconv.Quote(strings.ReplaceAll(s, "%", "%%")) }
	return fmt.Sprintf(`[Unit]
Description=Rillway multi-outbound proxy
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=rillway
Group=rillway
ExecStart=%s serve --config %s
Restart=on-failure
RestartSec=5
StateDirectory=rillway
StateDirectoryMode=0700
WorkingDirectory=/var/lib/rillway
UMask=0077
NoNewPrivileges=yes
CapabilityBoundingSet=
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/rillway

[Install]
WantedBy=multi-user.target
`, quote(binary), quote(configPath)), nil
}

func LaunchAgent(binary, configPath, stateDir string) (string, error) {
	for _, p := range []string{binary, configPath, stateDir} {
		if !filepath.IsAbs(p) {
			return "", errors.New("launchd paths must be absolute")
		}
	}
	esc := func(s string) string { var b strings.Builder; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>io.rillway.daemon</string>
<key>ProgramArguments</key><array><string>%s</string><string>serve</string><string>--config</string><string>%s</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>ThrottleInterval</key><integer>5</integer>
<key>WorkingDirectory</key><string>%s</string>
<key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, esc(binary), esc(configPath), esc(stateDir), esc(filepath.Join(stateDir, "daemon.log")), esc(filepath.Join(stateDir, "daemon-error.log"))), nil
}

func Service(ctx context.Context, action, configPath string) (string, error) {
	path, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "linux":
		return linuxService(ctx, action, path)
	case "darwin":
		return macService(ctx, action, path)
	default:
		return "", errors.New("service management supports Linux and macOS")
	}
}

func linuxService(ctx context.Context, action, source string) (string, error) {
	switch action {
	case "install":
		if os.Geteuid() != 0 {
			return "", errors.New("run service install with sudo to create the dedicated rillway service account")
		}
		c, err := config.Load(source)
		if err != nil {
			return "", err
		}
		requiredFiles := []string{c.Security.ProxyPasswordFile}
		for _, o := range c.Outbounds {
			requiredFiles = append(requiredFiles, o.ConfigFile, o.AuthKeyFile)
		}
		for _, path := range requiredFiles {
			if path != "" {
				if _, e := os.Stat(path); e != nil {
					return "", fmt.Errorf("credential file unavailable: %w", e)
				}
			}
		}
		if _, err := Run(ctx, "id", "-u", "rillway"); err != nil {
			if _, err = Run(ctx, "useradd", "--system", "--user-group", "--home-dir", "/var/lib/rillway", "--shell", "/usr/sbin/nologin", "rillway"); err != nil {
				return "", err
			}
		}
		state := "/var/lib/rillway"
		if err = os.MkdirAll(state, 0o700); err != nil {
			return "", err
		}
		copySecret := func(src, name string) (string, error) {
			dst := filepath.Join(state, name)
			if src == "" {
				return "", nil
			}
			if filepath.Clean(src) == dst {
				return dst, nil
			}
			b, e := os.ReadFile(src)
			if os.IsNotExist(e) {
				return dst, nil
			}
			if e != nil {
				return "", e
			}
			return dst, config.WritePrivate(dst, b)
		}
		c.Security.AdminTokenFile, err = copySecret(c.Security.AdminTokenFile, "admin.token")
		if err != nil {
			return "", err
		}
		c.Security.TLSCertFile, err = copySecret(c.Security.TLSCertFile, "admin.crt")
		if err != nil {
			return "", err
		}
		c.Security.TLSKeyFile, err = copySecret(c.Security.TLSKeyFile, "admin.key")
		if err != nil {
			return "", err
		}
		c.Security.ProxyPasswordFile, err = copySecret(c.Security.ProxyPasswordFile, "proxy.password")
		if err != nil {
			return "", err
		}
		for i := range c.Outbounds {
			o := &c.Outbounds[i]
			if o.ConfigFile != "" {
				o.ConfigFile, err = copySecret(o.ConfigFile, o.ID+".conf")
				if err != nil {
					return "", err
				}
			}
			if o.AuthKeyFile != "" {
				o.AuthKeyFile, err = copySecret(o.AuthKeyFile, o.ID+".auth")
				if err != nil {
					return "", err
				}
			}
			if o.StateDir != "" {
				dst := filepath.Join(state, "outbounds", o.ID)
				if filepath.Clean(o.StateDir) != dst {
					if err = copyDirectory(o.StateDir, dst); err != nil {
						return "", err
					}
				}
				o.StateDir = dst
			}
		}
		installedConfig := filepath.Join(state, "config.json")
		if err = config.Save(installedConfig, c); err != nil {
			return "", err
		}
		if _, _, err = EnsureCredentials(c); err != nil {
			return "", err
		}
		if _, err = Run(ctx, "chown", "-R", "rillway:rillway", state); err != nil {
			return "", err
		}
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		target := "/usr/local/lib/rillway/rillway"
		if err = os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err = os.Chmod(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err = copyExecutable(exe, target); err != nil {
			return "", err
		}
		unit, err := SystemdUnit(target, installedConfig)
		if err != nil {
			return "", err
		}
		if err = config.WritePrivate("/etc/systemd/system/rillway.service", []byte(unit)); err != nil {
			return "", err
		}
		if _, err = Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return "", err
		}
		if _, err = Run(ctx, "systemctl", "enable", "rillway.service"); err != nil {
			return "", err
		}
		out, err := Run(ctx, "systemctl", "restart", "rillway.service")
		return string(out), err
	case "start", "stop", "restart", "status":
		args := []string{action, "rillway.service"}
		if action == "status" {
			args = append(args, "--no-pager")
		}
		out, err := Run(ctx, "systemctl", args...)
		return string(out), err
	case "uninstall":
		if os.Geteuid() != 0 {
			return "", errors.New("service uninstall requires sudo")
		}
		if _, err := Run(ctx, "systemctl", "disable", "--now", "rillway.service"); err != nil {
			return "", err
		}
		if err := os.Remove("/etc/systemd/system/rillway.service"); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		out, err := Run(ctx, "systemctl", "daemon-reload")
		return string(out) + "\nService removed; account and /var/lib/rillway retained.", err
	default:
		return "", fmt.Errorf("unknown service action %q", action)
	}
}

func macService(ctx context.Context, action, source string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	state := filepath.Join(home, "Library", "Application Support", "Rillway")
	agent := filepath.Join(home, "Library", "LaunchAgents", "io.rillway.daemon.plist")
	domain := "gui/" + strconv.Itoa(os.Getuid())
	target := domain + "/io.rillway.daemon"
	switch action {
	case "install":
		if _, err = config.Load(source); err != nil {
			return "", err
		}
		if err = os.MkdirAll(state, 0o700); err != nil {
			return "", err
		}
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		binary := filepath.Join(state, "rillway")
		if err = copyExecutable(exe, binary); err != nil {
			return "", err
		}
		s, err := LaunchAgent(binary, source, state)
		if err != nil {
			return "", err
		}
		if err = config.WritePrivate(agent, []byte(s)); err != nil {
			return "", err
		}
		_, _ = Run(ctx, "launchctl", "bootout", target)
		out, err := Run(ctx, "launchctl", "bootstrap", domain, agent)
		return string(out), err
	case "start":
		out, err := Run(ctx, "launchctl", "bootstrap", domain, agent)
		return string(out), err
	case "restart":
		out, err := Run(ctx, "launchctl", "kickstart", "-k", target)
		return string(out), err
	case "stop":
		out, err := Run(ctx, "launchctl", "bootout", target)
		return string(out), err
	case "status":
		out, err := Run(ctx, "launchctl", "print", target)
		return string(out), err
	case "uninstall":
		_, _ = Run(ctx, "launchctl", "bootout", target)
		if err := os.Remove(agent); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		return "LaunchAgent removed; configuration and state retained.", nil
	default:
		return "", fmt.Errorf("unknown service action %q", action)
	}
}

func copyExecutable(src, dst string) error {
	if filepath.Clean(src) == filepath.Clean(dst) {
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err = config.WritePrivate(dst, b); err != nil {
		return err
	}
	return os.Chmod(dst, 0o755)
}

func copyDirectory(src, dst string) error {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return os.MkdirAll(dst, 0o700)
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("state directory must not contain symlinks")
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(in, (64<<20)+1))
		_ = in.Close()
		if err != nil {
			return err
		}
		if len(b) > 64<<20 {
			return errors.New("state file exceeds 64 MiB; refusing a truncated copy")
		}
		return config.WritePrivate(target, b)
	})
}
