package main

import "path/filepath"

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
