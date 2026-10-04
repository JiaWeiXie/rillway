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
	"runtime"
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
	if len(os.Args) > 2 || len(os.Args) == 2 && os.Args[1] != "--prepare" && os.Args[1] != "--hashes-only" {
		return fmt.Errorf("use --prepare or --hashes-only")
	}
	if len(os.Args) == 2 && os.Args[1] == "--hashes-only" {
		return writeHashes()
	}
	// Include the union of imported module dependencies for all release targets.
	// The complete module graph also contains unused modules with no source files.
	byPath := map[string]module{}
	for _, target := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"} {
		parts := strings.Split(target, "/")
		command := exec.Command("go", "list", "-deps", "-json", "./cmd/rillway")
		command.Env = append(os.Environ(), "GOOS="+parts[0], "GOARCH="+parts[1], "CGO_ENABLED=0")
		data, err := command.Output()
		if err != nil {
			return fmt.Errorf("list release dependencies for %s: %w", target, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		for {
			var pkg struct{ Module *module }
			if err := decoder.Decode(&pkg); err == io.EOF {
				break
			} else if err != nil {
				return err
			}
			if pkg.Module != nil && !pkg.Module.Main {
				byPath[pkg.Module.Path] = *pkg.Module
			}
		}
	}
	var modules []module
	for _, m := range byPath {
		modules = append(modules, m)
	}
	var err error
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	var notices strings.Builder
	notices.WriteString("Rillway third-party notices. Embedded in the standalone binary.\n\n")
	summary, err := os.ReadFile("THIRD_PARTY.md")
	if err != nil {
		return err
	}
	notices.Write(summary)
	notices.WriteString("\n\n")
	goLicense, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "LICENSE"))
	if err != nil {
		return err
	}
	notices.WriteString("\n===== Go standard library / LICENSE =====\n")
	notices.Write(goLicense)
	notices.WriteString("\n")
	var index strings.Builder
	index.WriteString("Rillway release dependency inventory; union of Linux/macOS imports, exact versions from go.mod/go.sum.\n\n")
	for _, m := range modules {
		source := m
		if m.Replace != nil {
			source = *m.Replace
		}
		fmt.Fprintf(&index, "%s %s\n", m.Path, m.Version)
		entries, err := os.ReadDir(source.Dir)
		if err != nil {
			return fmt.Errorf("release module %s source unavailable: %w", m.Path, err)
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
			fmt.Fprintf(&notices, "\n===== %s %s / %s =====\n", m.Path, m.Version, entry.Name())
			notices.Write(b)
			notices.WriteString("\n")
			found = true
		}
		if !found {
			return fmt.Errorf("release module %s has no root license/notice; review upstream before redistribution", m.Path)
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
		fmt.Fprintf(&notices, "\n===== Fonts / %s =====\n", name)
		notices.Write(data)
		notices.WriteString("\n")
		dir := filepath.Join("dist", "_licenses", "fonts")
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	if err = os.MkdirAll("internal/notices", 0o755); err != nil {
		return err
	}
	if err = os.WriteFile("internal/notices/NOTICE.txt", []byte(notices.String()), 0o644); err != nil {
		return err
	}
	if len(os.Args) == 2 {
		return nil
	}
	return writeHashes()
}

func writeHashes() error {
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
