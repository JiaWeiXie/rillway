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
	"rillway/internal/i18n"
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
ConfigurationDirectory=rillway
ConfigurationDirectoryMode=0700
WorkingDirectory=/var/lib/rillway
UMask=0077
NoNewPrivileges=yes
CapabilityBoundingSet=
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/rillway /etc/rillway

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
		return installLinux(ctx, source, defaultLinuxPaths(), Run, os.Executable)
	case "memory-install":
		if os.Geteuid() != 0 {
			return "", errors.New("memory control installation requires sudo")
		}
		paths := defaultLinuxPaths()
		if _, err := config.Load(source); err != nil {
			return "", err
		}
		// A root controller must never execute a binary writable by its service
		// account. Validate the installed executable and every parent directory.
		for path := paths.binary; path != "/"; path = filepath.Dir(path) {
			info, err := os.Lstat(path)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || !rootOwned(info) {
				return "", errors.New("memory control requires a root-owned installed binary and directories")
			}
		}
		for _, path := range []string{paths.unit, filepath.Dir(paths.unit), filepath.Join(filepath.Dir(paths.unit), "rillway.service.d")} {
			info, err := os.Lstat(path)
			if os.IsNotExist(err) && strings.HasSuffix(path, "rillway.service.d") {
				continue
			}
			if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || !rootOwned(info) {
				return "", errors.New("memory control requires root-owned service registration")
			}
		}
		if err := writeMemoryUnits(paths, source); err != nil {
			return "", err
		}
		if _, err := Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return "", err
		}
		out, err := Run(ctx, "systemctl", "enable", "--now", "rillway-memory.socket")
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
		if _, err := os.Stat("/etc/systemd/system/rillway-memory.socket"); err == nil {
			if _, err = Run(ctx, "systemctl", "disable", "--now", "rillway-memory.socket"); err != nil {
				return "", err
			}
			if _, err = Run(ctx, "systemctl", "stop", "rillway-memory.service"); err != nil {
				return "", err
			}
			for _, name := range []string{"rillway-memory.socket", "rillway-memory.service"} {
				if err = os.Remove(filepath.Join("/etc/systemd/system", name)); err != nil && !os.IsNotExist(err) {
					return "", err
				}
			}
		}
		if err := os.Remove("/etc/systemd/system/rillway.service"); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		memoryLink := "/etc/systemd/system/rillway.service.d/zzzz-rillway-memory.conf"
		if target, err := os.Readlink(memoryLink); err == nil && target == "/var/lib/rillway-resource-control/memory-limit.conf" {
			if err = os.Remove(memoryLink); err != nil {
				return "", err
			}
		}
		out, err := Run(ctx, "systemctl", "daemon-reload")
		return string(out) + "\n" + i18n.Message(i18n.FromContext(ctx), "Service removed; account, /etc/rillway and /var/lib/rillway retained."), err
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
		if err = CheckNewInstallation(); err != nil {
			return "", err
		}
		cfg, err := config.Load(source)
		if err != nil {
			return "", err
		}
		if err = CheckListeners(ctx, cfg); err != nil {
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
		return i18n.Message(i18n.FromContext(ctx), "LaunchAgent removed; configuration and state retained."), nil
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
