//go:build ignore

// Release-only helper. Explicit members prevent workspace/secret uploads.
package main

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if err := packageSkill(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func packageSkill() error {
	const root = "skills/rillway-ops"
	for _, dir := range []string{"skills", root, root + "/agents", root + "/references"} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill source directory is missing or unsafe")
		}
	}
	if err := os.MkdirAll("dist", 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp("dist", ".skill-*.zip")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()
	archive := zip.NewWriter(f)
	for _, member := range []string{"SKILL.md", "agents/openai.yaml", "references/agent-cli.md", "references/installation.md", "references/configuration.md", "references/troubleshooting.md", "LICENSE"} {
		path := filepath.Join(root, member)
		if member == "LICENSE" {
			path = "LICENSE"
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<10 {
			return fmt.Errorf("skill member is missing, oversized or not a regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		header := &zip.FileHeader{Name: "rillway-ops/" + member, Method: zip.Deflate}
		header.SetMode(0o644)
		header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err = writer.Write(data); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), "dist/rillway-ops.zip")
}
