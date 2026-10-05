// Command publiccheck audits publishable Git source without printing secret values.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var homePath = regexp.MustCompile(`/(Users|home)/[A-Za-z0-9._-]+`)

func git(args ...string) ([]byte, error) { return exec.Command("git", args...).Output() }
func privatePath(name string) bool {
	name = filepath.ToSlash(name)
	root := strings.SplitN(name, "/", 2)[0]
	for _, p := range []string{".local", ".cache", ".state", "secrets", "run", "logs", "coverage", "bin", "build", "dist", "tmp"} {
		if root == p {
			return true
		}
	}
	base := filepath.Base(name)
	return base == ".env" || strings.HasPrefix(base, ".env.") && base != ".env.example" || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".token") || strings.HasSuffix(base, ".password") || base == "mise.local.toml" || base == ".mise.local.toml" || strings.Contains(base, "proxy-backup.json")
}

func contentPrivate(data []byte, patterns []string) bool {
	if homePath.Match(data) {
		return true
	}
	for _, p := range patterns {
		if p != "" && bytes.Contains(bytes.ToLower(data), []byte(strings.ToLower(p))) {
			return true
		}
	}
	return false
}

func audit() error {
	var patterns []string
	if path := os.Getenv("RILLWAY_PRIVACY_PATTERNS"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("cannot read private denylist")
		}
		for _, line := range strings.Split(string(b), "\n") {
			if p := strings.TrimSpace(line); p != "" {
				patterns = append(patterns, p)
			}
		}
	}
	files, err := git("ls-files", "-z")
	if err != nil {
		return err
	}
	for _, name := range strings.Split(string(files), "\x00") {
		if name == "" {
			continue
		}
		if privatePath(name) || contentPrivate([]byte(name), patterns) {
			return fmt.Errorf("private/generated file is tracked; audit failed")
		}
		info, err := os.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("tracked source must be a readable regular file")
		}
		b, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if contentPrivate(b, patterns) {
			return fmt.Errorf("private data in tracked source; audit failed")
		}
	}
	metadata, err := git("log", "--all", "--format=%an%n%ae%n%cn%n%ce%n%B")
	if err != nil {
		return err
	}
	if contentPrivate(metadata, patterns) {
		return fmt.Errorf("private data in commit metadata; audit failed")
	}
	refs, err := git("for-each-ref", "--format=%(refname)%0a%(taggername)%0a%(taggeremail)%0a%(contents)")
	if err != nil {
		return err
	}
	if contentPrivate(refs, patterns) {
		return fmt.Errorf("private data in ref or tag metadata; audit failed")
	}
	emails, err := git("log", "--all", "--format=%ae%n%ce")
	if err != nil {
		return err
	}
	tagEmails, err := git("for-each-ref", "--format=%(taggeremail)", "refs/tags")
	if err != nil {
		return err
	}
	for _, email := range strings.Fields(string(emails) + "\n" + string(tagEmails)) {
		email = strings.Trim(email, "<>")
		switch {
		case strings.HasSuffix(email, ".invalid"), strings.HasSuffix(email, "@example.com"), strings.HasSuffix(email, "@users.noreply.github.com"), email == "noreply@github.com":
		default:
			return fmt.Errorf("non-public email in commit metadata; use a GitHub noreply or project identity")
		}
	}
	// Each reachable object is checked once, including deleted historical files.
	objects, err := git("rev-list", "--objects", "--all")
	if err != nil {
		return err
	}
	count := 0
	for _, line := range strings.Split(string(objects), "\n") {
		id, name, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		kind, err := git("cat-file", "-t", id)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(kind)) != "blob" {
			continue
		}
		if privatePath(name) || contentPrivate([]byte(name), patterns) {
			return fmt.Errorf("private/generated file exists in history; audit failed")
		}
		b, err := git("cat-file", "blob", id)
		if err != nil {
			return err
		}
		if contentPrivate(b, patterns) {
			return fmt.Errorf("private data in a reachable historical blob; audit failed")
		}
		count++
	}
	fmt.Printf("Public-source audit passed: %d reachable blobs and commit metadata; values not printed.\n", count)
	return nil
}

func main() {
	if err := audit(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
