//go:build ignore

// Release-only helper; not linked into Rillway.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type module struct {
	Path, Version, Dir string
	Main               bool
	Replace            *module
}

func main() {
	if err := generate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate() error {
	data, err := exec.Command("go", "list", "-m", "-json", "all").Output()
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var modules []module
	for {
		var m module
		if err = decoder.Decode(&m); err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if !m.Main {
			modules = append(modules, m)
		}
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	var index strings.Builder
	index.WriteString("Rillway module inventory; exact versions from go.mod/go.sum.\n\n")
	for _, m := range modules {
		source := m
		if m.Replace != nil {
			source = *m.Replace
		}
		fmt.Fprintf(&index, "%s %s\n", m.Path, m.Version)
		entries, err := os.ReadDir(source.Dir)
		if err != nil {
			fmt.Fprintf(&index, "  source directory unavailable (module graph only)\n")
			continue
		}
		found := false
		for _, entry := range entries {
			name := strings.ToLower(entry.Name())
			if entry.IsDir() || !(strings.HasPrefix(name, "license") || strings.HasPrefix(name, "copying") || strings.HasPrefix(name, "notice")) {
				continue
			}
			b, e := os.ReadFile(filepath.Join(source.Dir, entry.Name()))
			if e != nil {
				return e
			}
			target := filepath.Join("dist", "_licenses", m.Path+"@"+m.Version, entry.Name())
			if e = os.MkdirAll(filepath.Dir(target), 0o755); e != nil {
				return e
			}
			if e = os.WriteFile(target, b, 0o644); e != nil {
				return e
			}
			fmt.Fprintf(&index, "  %s\n", target)
			found = true
		}
		if !found {
			index.WriteString("  No root license file; consult upstream source before redistribution.\n")
		}
	}
	if err = os.WriteFile("dist/MODULES.txt", []byte(index.String()), 0o644); err != nil {
		return err
	}
	// Embedded fonts are distributed with every binary, independently of Go modules.
	for _, name := range []string{"OFL-NotoSansTC.txt", "OFL-NotoColorEmoji.txt"} {
		data, readErr := os.ReadFile(filepath.Join("internal", "control", "web", "fonts", name))
		if readErr != nil {
			return readErr
		}
		dir := filepath.Join("dist", "_licenses", "fonts")
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	var hashes strings.Builder
	binaries, err := filepath.Glob("dist/rillway-*")
	if err != nil {
		return err
	}
	for _, path := range binaries {
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		digest := sha256.New()
		_, e = io.Copy(digest, f)
		_ = f.Close()
		if e != nil {
			return e
		}
		fmt.Fprintf(&hashes, "%x  %s\n", digest.Sum(nil), filepath.Base(path))
	}
	return os.WriteFile("dist/SHA256SUMS", []byte(hashes.String()), 0o644)
}
