package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The installer writes this exact command format. Prefer its selected config
// over stray files at other default locations; never execute the unit contents.
func systemdConfigPath(unit []byte) string {
	section, path := "", ""
	for line := range strings.SplitSeq(string(unit), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if section != "[Service]" || !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		path = ""
		binary, configPath, ok := strings.Cut(strings.TrimPrefix(line, "ExecStart="), " serve --config ")
		if !ok {
			continue
		}
		decode := func(value string) string {
			value = strings.TrimSpace(value)
			if strings.HasPrefix(value, `"`) {
				var err error
				value, err = strconv.Unquote(value)
				if err != nil {
					return ""
				}
			} else if strings.ContainsAny(value, " \t\"'") {
				return ""
			}
			value = strings.ReplaceAll(value, "%%", "%")
			if !filepath.IsAbs(value) || strings.ContainsAny(value, "\n\r\x00") {
				return ""
			}
			return value
		}
		if decode(binary) != "" {
			path = decode(configPath)
		}
	}
	return path
}

func installedServiceConfigPath(goos, unitPath string) string {
	if goos != "linux" {
		return ""
	}
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		return ""
	}
	return systemdConfigPath(unit)
}

func userConfigPath(goos, dir string) string {
	return userConfigFilePath(goos, dir, "config.json")
}

func userConfigFilePath(goos, dir, file string) string {
	if dir == "" {
		return filepath.Join(".local", file)
	}
	name := "rillway"
	if goos == "darwin" {
		name = "Rillway"
	}
	fallback := filepath.Join(dir, name, file)
	if goos == "darwin" {
		return installedConfigPath(goos, []string{fallback, filepath.Join(dir, "rillway", file)}, fallback)
	}
	return fallback
}

func defaultConfigPath(goos, userDir string, linuxPaths []string) string {
	fallback := userConfigPath(goos, userDir)
	candidates := linuxPaths
	if goos == "darwin" {
		candidates = nil
	}
	return installedConfigPath(goos, candidates, fallback)
}
