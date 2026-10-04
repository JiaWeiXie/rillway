package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"rillway/internal/tui"
	"strings"
)

func defaultClientPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ".local/client.json"
	}
	return filepath.Join(dir, "rillway", "client.json")
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
	if goos == "linux" {
		for _, path := range candidates {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				return path
			}
		}
	}
	return fallback
}

func tokenReadError(path string, err error) error {
	return fmt.Errorf("cannot read management token file %s: %w", strings.TrimSpace(path), err)
}
