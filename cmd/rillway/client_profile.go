package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/tui"
	"runtime"
	"strings"
)

func defaultClientPath() string {
	dir, _ := os.UserConfigDir()
	return userConfigFilePath(runtime.GOOS, dir, "client.json")
}

func loadClientProfile(path string) (tui.ConnectionSettings, error) {
	var settings tui.ConnectionSettings
	f, err := os.Open(path)
	if err != nil {
		return settings, err
	}
	defer func() { _ = f.Close() }()
	d := json.NewDecoder(io.LimitReader(f, 64<<10))
	d.DisallowUnknownFields()
	if err = d.Decode(&settings); err != nil {
		return settings, err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return settings, errors.New("client profile must contain one JSON object")
	}
	if settings.BaseURL == "" || settings.TokenFile == "" {
		return settings, errors.New("client profile requires a URL and token file")
	}
	return settings, nil
}

func saveClientProfile(path string, settings tui.ConnectionSettings) error {
	if settings.BaseURL == "" || settings.TokenFile == "" {
		return errors.New("client profile requires a URL and token file")
	}
	var err error
	settings.TokenFile, err = filepath.Abs(settings.TokenFile)
	if err != nil {
		return err
	}
	if settings.CAFile != "" {
		settings.CAFile, err = filepath.Abs(settings.CAFile)
		if err != nil {
			return err
		}
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".client-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = tmp.Write(append(data, '\n')); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	return nil
}

func installedConfigPath(goos string, candidates []string, fallback string) string {
	if goos == "linux" || goos == "darwin" {
		for _, path := range candidates {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				return path
			}
		}
	}
	return fallback
}

func resolveTerminalConnection(ctx context.Context, path, clientPath string, explicitConfig, explicitClient bool, settings tui.ConnectionSettings) (tui.ConnectionSettings, bool, error) {
	// A discovered local configuration takes precedence over an implicit client
	// profile, including when permissions prevent reading the private file.
	if settings.BaseURL == "" && !explicitConfig {
		_, statErr := os.Stat(path)
		if !explicitClient && !os.IsNotExist(statErr) {
			// Local configuration or a filesystem error must be handled below.
			return localTerminalConnection(ctx, path, settings)
		}
		remembered, err := loadClientProfile(clientPath)
		if err == nil {
			settings.BaseURL = remembered.BaseURL
			if settings.TokenFile == "" {
				settings.TokenFile = remembered.TokenFile
			}
			if settings.CAFile == "" {
				settings.CAFile = remembered.CAFile
			}
		} else if explicitClient || !os.IsNotExist(err) {
			return settings, false, fmt.Errorf("read TUI connection profile: %w", err)
		}
	}
	local := settings.BaseURL == ""
	if local {
		return localTerminalConnection(ctx, path, settings)
	} else if settings.TokenFile == "" {
		settings.TokenFile = filepath.Join(filepath.Dir(clientPath), "remote-admin.token")
	}
	return settings, local, nil
}

func localTerminalConnection(ctx context.Context, path string, settings tui.ConnectionSettings) (tui.ConnectionSettings, bool, error) {
	c, err := config.Load(path)
	if os.IsNotExist(err) {
		c, err = initialize(ctx, path)
	}
	if err != nil {
		return settings, true, err
	}
	host, port, _ := net.SplitHostPort(c.Listeners.Admin)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	settings.BaseURL = "https://" + net.JoinHostPort(host, port)
	if settings.TokenFile == "" {
		settings.TokenFile = c.Security.AdminTokenFile
	}
	if settings.CAFile == "" {
		settings.CAFile = c.Security.TLSCertFile
	}
	return settings, true, nil
}

func tokenReadError(path string, err error) error {
	return fmt.Errorf("cannot read management token file %s: %w", strings.TrimSpace(path), err)
}
