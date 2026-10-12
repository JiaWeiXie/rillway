package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/resourcecontrol"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const ExistingInstallation = "Rillway is already installed or has retained files. Back up the existing installation and update its binary instead of reinstalling."

type linuxPaths struct{ state, configDir, binary, link, unit, root string }

func defaultLinuxPaths() linuxPaths {
	return linuxPaths{"/var/lib/rillway", "/etc/rillway", "/usr/local/lib/rillway/rillway", "/usr/local/bin/rillway", "/etc/systemd/system/rillway.service", ""}
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
	unitDir := filepath.Dir(p.unit)
	return refuseExisting(
		p.unit, p.binary, p.link, p.configDir, p.state,
		filepath.Join(unitDir, "rillway-resource-control.socket"),
		filepath.Join(unitDir, "rillway-resource-control.service"),
		filepath.Join(unitDir, "rillway-memory.socket"),
		filepath.Join(unitDir, "rillway-memory.service"),
	)
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
		if !o.Enabled {
			continue
		}
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
				if _, statErr := os.Stat(o.StateDir); statErr != nil {
					if os.IsNotExist(statErr) {
						o.StateDir = dst
						continue
					}
					return "", statErr
				}
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
	if err = reconcileResourceControl(ctx, paths, installedConfig, run); err != nil {
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

func checkSecureOwnership(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	if os.Geteuid() == 0 {
		return stat.Uid == 0
	}
	return stat.Uid == uint32(os.Getuid())
}

func validateSecureHierarchy(p, root string) error {
	for curr := p; curr != "" && curr != "/" && curr != root; curr = filepath.Dir(curr) {
		info, err := os.Lstat(curr)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("insecure symlink at %s", curr)
		}
		if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("insecure permissions (%04o) at %s", info.Mode().Perm(), curr)
		}
		if !checkSecureOwnership(info) {
			return fmt.Errorf("insecure ownership at %s", curr)
		}
	}
	return nil
}

func writeUnitFile(dst, content string) error {
	tmp := dst + ".tmp." + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func restoreUnitFile(dst string, prev []byte, existed bool) error {
	if existed {
		return os.WriteFile(dst, prev, 0o644)
	}
	err := os.Remove(dst)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

type resourceSocketState struct {
	enabled bool
	runtime bool
	active  bool
}

// Nonzero exit codes are expected only for the documented negative states.
// A transport/spawn error, empty output, or transitional state fails closed.
func readResourceSocketState(ctx context.Context, run Runner) (resourceSocketState, error) {
	state := resourceSocketState{}
	for _, query := range []string{"is-enabled", "is-active"} {
		out, err := run(ctx, "systemctl", query, "rillway-resource-control.socket")
		value := strings.TrimSpace(string(out))
		code := 0
		if err != nil {
			var exit interface{ ExitCode() int }
			if !errors.As(err, &exit) {
				return state, fmt.Errorf("could not inspect resource control socket %s: %w", query, err)
			}
			code = exit.ExitCode()
		}
		valid := false
		if query == "is-enabled" {
			switch value {
			case "enabled", "enabled-runtime":
				valid = code == 0
				state.enabled, state.runtime = true, value == "enabled-runtime"
			case "disabled":
				valid = code == 1
			case "static", "indirect":
				valid = code == 0
			}
		} else {
			switch value {
			case "active":
				valid, state.active = code == 0, true
			case "inactive", "failed":
				valid = code == 3
			case "unknown":
				valid = code == 4
			}
		}
		if !valid {
			return state, fmt.Errorf("could not determine resource control socket %s state", query)
		}
	}
	return state, nil
}

func managedWarpLogLevel(data []byte) (resourcecontrol.WarpLogLevel, error) {
	var level resourcecontrol.WarpLogLevel
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "LogLevelMax="); ok {
			found = true
			level = resourcecontrol.WarpLogLevel(strings.TrimSpace(value))
			if level == "" {
				level = resourcecontrol.WarpLogLevelInherit
			}
		}
	}
	if !found || !resourcecontrol.IsValidWarpLogLevel(level) {
		return "", errors.New("unsafe WARP log level state file")
	}
	return level, nil
}

func verifyManagedWarpLog(ctx context.Context, run Runner, level resourcecontrol.WarpLogLevel) bool {
	out, err := run(ctx, "systemctl", "show", "warp-svc.service", "--property=LogLevelMax", "--value")
	if err != nil {
		return false
	}
	effective := strings.TrimSpace(string(out))
	// Inherit can expose the unset D-Bus sentinel or an empty systemctl value.
	if level == resourcecontrol.WarpLogLevelInherit && (effective == "-1" || effective == "") {
		return true
	}
	priorities := [...]string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}
	if number, err := strconv.Atoi(effective); err == nil && number >= 0 && number < len(priorities) {
		effective = priorities[number]
	}
	for _, priority := range priorities {
		if effective == priority {
			return level == resourcecontrol.WarpLogLevelInherit || effective == string(level)
		}
	}
	return false
}

func reconcileResourceControl(ctx context.Context, paths linuxPaths, configPath string, run Runner) error {
	// =========================================================================
	// PASS 1: Strict read-only validation/planning pass.
	// NO files, directories, or symlinks may be created or modified in this pass.
	// If any preflight check fails, fail closed immediately.
	// =========================================================================

	// 1. Validate installed binary and parent directories.
	bInfo, err := os.Lstat(paths.binary)
	if err != nil || !bInfo.Mode().IsRegular() {
		return errors.New("resource control requires an installed binary")
	}
	if err := validateSecureHierarchy(paths.binary, paths.root); err != nil {
		return fmt.Errorf("resource control requires secure binary directory: %w", err)
	}

	// 2. Validate systemd directory and unit file if already present.
	unitDir := filepath.Dir(paths.unit)
	if err := validateSecureHierarchy(unitDir, paths.root); err != nil {
		return fmt.Errorf("resource control requires secure systemd directory: %w", err)
	}
	if uInfo, err := os.Lstat(paths.unit); err == nil {
		if uInfo.Mode()&os.ModeSymlink != 0 || uInfo.Mode().Perm()&0o022 != 0 || !checkSecureOwnership(uInfo) {
			return errors.New("resource control requires secure rillway.service unit file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	// 3. Resource state directory and parent hierarchy.
	resourceDir := filepath.Join(filepath.Dir(paths.state), "rillway-resource-control")
	if err := validateSecureHierarchy(filepath.Dir(resourceDir), paths.root); err != nil {
		return fmt.Errorf("unsafe parent directory for resource state: %w", err)
	}
	if rInfo, err := os.Lstat(resourceDir); err == nil {
		if !rInfo.IsDir() || rInfo.Mode()&os.ModeSymlink != 0 || rInfo.Mode().Perm()&0o022 != 0 || !checkSecureOwnership(rInfo) {
			return errors.New("unsafe resource control state directory")
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	// 4. Memory drop-in and state file preflight.
	memoryTarget := filepath.Join(resourceDir, "memory-limit.conf")
	if mInfo, err := os.Lstat(memoryTarget); err == nil {
		if !mInfo.Mode().IsRegular() || mInfo.Mode()&os.ModeSymlink != 0 || mInfo.Mode().Perm()&0o022 != 0 || !checkSecureOwnership(mInfo) {
			return errors.New("unsafe memory control state file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	dropinDir := filepath.Join(unitDir, "rillway.service.d")
	if dInfo, err := os.Lstat(dropinDir); err == nil {
		if !dInfo.IsDir() || dInfo.Mode()&os.ModeSymlink != 0 || dInfo.Mode().Perm()&0o022 != 0 || !checkSecureOwnership(dInfo) {
			return errors.New("unsafe rillway.service.d directory")
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	memoryLink := filepath.Join(dropinDir, "zzzz-rillway-memory.conf")
	if current, readErr := os.Readlink(memoryLink); readErr == nil {
		if current != memoryTarget {
			return errors.New("memory control drop-in already exists with another target")
		}
	} else if !os.IsNotExist(readErr) {
		return errors.New("memory control drop-in already exists with another target")
	}

	// 5. Cloudflare WARP drop-in preflight if warp-svc.service is loaded.
	warpLoaded := false
	warpTarget := filepath.Join(resourceDir, "warp-log-level.conf")
	warpDropinDir := filepath.Join(unitDir, "warp-svc.service.d")
	warpLink := filepath.Join(warpDropinDir, "zzzz-rillway-log-level.conf")

	warpOut, err := run(ctx, "systemctl", "show", "warp-svc.service", "--property=LoadState")
	if err == nil && strings.Contains(string(warpOut), "LoadState=loaded") {
		warpLoaded = true

		if wInfo, err := os.Lstat(warpTarget); err == nil {
			if !wInfo.Mode().IsRegular() || wInfo.Mode()&os.ModeSymlink != 0 || wInfo.Mode().Perm()&0o022 != 0 || !checkSecureOwnership(wInfo) {
				return errors.New("unsafe warp log level state file")
			}
		} else if !os.IsNotExist(err) {
			return err
		}

		if wdInfo, err := os.Lstat(warpDropinDir); err == nil {
			if !wdInfo.IsDir() || wdInfo.Mode()&os.ModeSymlink != 0 || wdInfo.Mode().Perm()&0o022 != 0 || !checkSecureOwnership(wdInfo) {
				return errors.New("unsafe warp-svc.service.d directory")
			}
		} else if !os.IsNotExist(err) {
			return err
		}

		if current, readErr := os.Readlink(warpLink); readErr == nil {
			if current != warpTarget {
				return errors.New("warp log level drop-in already exists with another target")
			}
		} else if !os.IsNotExist(readErr) {
			return errors.New("warp log level drop-in already exists with another target")
		}
	}

	// 6. Units generation preflight.
	socketContent, serviceContent, err := resourcecontrol.Units(paths.binary, configPath)
	if err != nil {
		return err
	}
	newSocket := filepath.Join(unitDir, "rillway-resource-control.socket")
	newService := filepath.Join(unitDir, "rillway-resource-control.service")
	for _, unitPath := range []string{newSocket, newService} {
		if uInfo, err := os.Lstat(unitPath); err == nil {
			if !uInfo.Mode().IsRegular() || uInfo.Mode()&os.ModeSymlink != 0 || uInfo.Mode().Perm()&0o022 != 0 || !checkSecureOwnership(uInfo) {
				return errors.New("unsafe existing resource control unit file")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	// Snapshot the previous generation before creating or modifying any assets.
	prevSocket, socketReadErr := os.ReadFile(newSocket)
	prevService, serviceReadErr := os.ReadFile(newService)
	if socketReadErr != nil && !os.IsNotExist(socketReadErr) {
		return socketReadErr
	}
	if serviceReadErr != nil && !os.IsNotExist(serviceReadErr) {
		return serviceReadErr
	}
	prior := resourceSocketState{}
	if socketReadErr == nil {
		prior, err = readResourceSocketState(ctx, run)
		if err != nil {
			return err
		}
	}
	warpStateMissing, warpLinkMissing, warpDirMissing := false, false, false
	warpLevel := resourcecontrol.WarpLogLevelInfo
	if warpLoaded {
		data, readErr := os.ReadFile(warpTarget)
		warpStateMissing = os.IsNotExist(readErr)
		if readErr != nil && !warpStateMissing {
			return readErr
		}
		if !warpStateMissing {
			warpLevel, err = managedWarpLogLevel(data)
			if err != nil {
				return err
			}
		}
		_, linkErr := os.Lstat(warpLink)
		_, dirErr := os.Lstat(warpDropinDir)
		warpLinkMissing, warpDirMissing = os.IsNotExist(linkErr), os.IsNotExist(dirErr)
	}

	// =========================================================================
	// PASS 2: Mutation pass.
	// Executed only after all read-only preflight checks succeed.
	// =========================================================================

	// 1. Create resource state directory.
	if err := os.MkdirAll(resourceDir, 0o755); err != nil {
		return err
	}

	// 2. Write initial memory-limit.conf if absent.
	if _, err := os.Stat(memoryTarget); os.IsNotExist(err) {
		if err = config.WritePrivate(memoryTarget, []byte("# Rillway memory limits are unchanged until saved.\n[Service]\n")); err != nil {
			return err
		}
		if err = os.Chmod(memoryTarget, 0o644); err != nil {
			return err
		}
	}

	// 3. Ensure memory drop-in link.
	if err := os.MkdirAll(dropinDir, 0o755); err != nil {
		return err
	}
	if _, readErr := os.Readlink(memoryLink); os.IsNotExist(readErr) {
		if err := os.Symlink(memoryTarget, memoryLink); err != nil {
			return err
		}
	}

	// 4. Ensure WARP drop-in if loaded.
	if warpLoaded {
		if _, err := os.Stat(warpTarget); os.IsNotExist(err) {
			if err = config.WritePrivate(warpTarget, []byte("[Service]\nLogLevelMax=info\n")); err != nil {
				return err
			}
			if err = os.Chmod(warpTarget, 0o644); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(warpDropinDir, 0o755); err != nil {
			return err
		}
		if _, readErr := os.Readlink(warpLink); os.IsNotExist(readErr) {
			if err := os.Symlink(warpTarget, warpLink); err != nil {
				return err
			}
		}
	}

	// 5. Units atomic writing and activation.

	if err = writeUnitFile(newSocket, socketContent); err != nil {
		return err
	}
	if err = writeUnitFile(newService, serviceContent); err != nil {
		_ = restoreUnitFile(newSocket, prevSocket, socketReadErr == nil)
		return err
	}

	rollbackNewUnits := func(activationAttempted bool) error {
		var cleanupErr error
		if activationAttempted {
			// enable --now may partially succeed even when systemctl returns an error.
			_, cleanupErr = run(ctx, "systemctl", "disable", "--now", "rillway-resource-control.socket")
		}
		socketErr := restoreUnitFile(newSocket, prevSocket, socketReadErr == nil)
		serviceErr := restoreUnitFile(newService, prevService, serviceReadErr == nil)
		// Only undo WARP assets created by this reconcile; existing managed values stay intact.
		for _, asset := range []struct {
			path    string
			created bool
		}{
			{warpLink, warpLoaded && warpLinkMissing},
			{warpTarget, warpLoaded && warpStateMissing},
			{warpDropinDir, warpLoaded && warpDirMissing},
		} {
			if asset.created {
				if removeErr := os.Remove(asset.path); removeErr != nil && !os.IsNotExist(removeErr) {
					cleanupErr = errors.Join(cleanupErr, removeErr)
				}
			}
		}
		_, reloadErr := run(ctx, "systemctl", "daemon-reload")
		if socketErr != nil || serviceErr != nil || reloadErr != nil {
			return errors.Join(cleanupErr, socketErr, serviceErr, reloadErr)
		}
		if activationAttempted && socketReadErr == nil {
			if prior.enabled {
				args := []string{"enable"}
				if prior.runtime {
					args = append(args, "--runtime")
				}
				args = append(args, "rillway-resource-control.socket")
				_, enableErr := run(ctx, "systemctl", args...)
				cleanupErr = errors.Join(cleanupErr, enableErr)
			}
			if prior.active {
				_, startErr := run(ctx, "systemctl", "start", "rillway-resource-control.socket")
				cleanupErr = errors.Join(cleanupErr, startErr)
			}
		}
		return cleanupErr
	}

	if _, err = run(ctx, "systemctl", "daemon-reload"); err != nil {
		return errors.Join(err, rollbackNewUnits(false))
	}
	if warpLoaded && !verifyManagedWarpLog(ctx, run, warpLevel) {
		if rollbackErr := rollbackNewUnits(false); rollbackErr != nil {
			return errors.New("Could not verify WARP service logging or restore the previous service state. Read the installed service state before retrying.")
		}
		return errors.New("Could not verify WARP service logging. The previous settings were restored.")
	}
	if _, err = run(ctx, "systemctl", "enable", "--now", "rillway-resource-control.socket"); err != nil {
		return errors.Join(err, rollbackNewUnits(true))
	}
	activeOut, err := run(ctx, "systemctl", "is-active", "rillway-resource-control.socket")
	if err != nil || strings.TrimSpace(string(activeOut)) != "active" {
		if err == nil {
			err = errors.New("resource control socket is not active")
		}
		return errors.Join(err, rollbackNewUnits(true))
	}

	// 6. Clean up legacy memory units if present.
	oldSocket := filepath.Join(unitDir, "rillway-memory.socket")
	oldService := filepath.Join(unitDir, "rillway-memory.service")
	oldFound := false
	if _, err := os.Stat(oldSocket); err == nil {
		oldFound = true
	}
	if _, err := os.Stat(oldService); err == nil {
		oldFound = true
	}
	if oldFound {
		if _, err := run(ctx, "systemctl", "disable", "--now", "rillway-memory.socket"); err != nil {
			return err
		}
		if _, err := run(ctx, "systemctl", "stop", "rillway-memory.service"); err != nil {
			return err
		}
		if err := os.Remove(oldSocket); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(oldService); err != nil && !os.IsNotExist(err) {
			return err
		}
		if _, err := run(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}

	return nil
}
