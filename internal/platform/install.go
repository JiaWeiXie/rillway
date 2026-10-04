package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"runtime"
)

const ExistingInstallation = "Rillway is already installed or has retained files. Back up the existing installation and update its binary instead of reinstalling."

type linuxPaths struct{ state, configDir, binary, link, unit string }

func defaultLinuxPaths() linuxPaths {
	return linuxPaths{"/var/lib/rillway", "/etc/rillway", "/usr/local/lib/rillway/rillway", "/usr/local/bin/rillway", "/etc/systemd/system/rillway.service"}
}

func refuseExisting(paths ...string) error {
	for _, path := range paths {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			if err != nil && !os.IsPermission(err) {
				return err
			}
			return config.PublicError{Message: ExistingInstallation}
		}
	}
	return nil
}

func (p linuxPaths) check() error {
	return refuseExisting(p.unit, p.binary, p.link, p.configDir, p.state)
}

// CheckNewInstallation is a read-only preflight. The privileged installer checks
// again before touching files; saved configuration and VPN state are never reset.
func CheckNewInstallation() error {
	if runtime.GOOS == "linux" {
		return defaultLinuxPaths().check()
	}
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		return refuseExisting(filepath.Join(home, "Library", "LaunchAgents", "io.rillway.daemon.plist"), filepath.Join(home, "Library", "Application Support", "Rillway", "rillway"))
	}
	return errors.New("service management supports Linux and macOS")
}

func installLinux(ctx context.Context, source string, paths linuxPaths, run Runner, executable func() (string, error)) (string, error) {
	if err := paths.check(); err != nil {
		return "", err
	}
	c, err := config.Load(source)
	if err != nil {
		return "", err
	}
	if err = CheckListeners(ctx, c); err != nil {
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
	if _, err := run(ctx, "id", "-u", "rillway"); err != nil {
		if _, err = run(ctx, "useradd", "--system", "--user-group", "--home-dir", "/var/lib/rillway", "--shell", "/usr/sbin/nologin", "rillway"); err != nil {
			return "", err
		}
	}
	state := paths.state
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
	if err = os.MkdirAll(paths.configDir, 0o700); err != nil {
		return "", err
	}
	installedConfig := filepath.Join(paths.configDir, "config.json")
	if err = config.Save(installedConfig, c); err != nil {
		return "", err
	}
	if _, _, err = EnsureCredentials(c); err != nil {
		return "", err
	}
	if _, err = run(ctx, "chown", "-R", "rillway:rillway", state, paths.configDir); err != nil {
		return "", err
	}
	exe, err := executable()
	if err != nil {
		return "", err
	}
	target := paths.binary
	if err = os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err = os.Chmod(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err = copyExecutable(exe, target); err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(paths.link), 0o755); err != nil {
		return "", err
	}
	if err = os.Symlink(target, paths.link); err != nil {
		return "", err
	}
	unit, err := SystemdUnit(target, installedConfig)
	if err != nil {
		return "", err
	}
	if err = config.WritePrivate(paths.unit, []byte(unit)); err != nil {
		return "", err
	}
	if _, err = run(ctx, "systemctl", "daemon-reload"); err != nil {
		return "", err
	}
	if _, err = run(ctx, "systemctl", "enable", "rillway.service"); err != nil {
		return "", err
	}
	out, err := run(ctx, "systemctl", "start", "rillway.service")
	return string(out), err
}
