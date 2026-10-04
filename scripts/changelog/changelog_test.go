package changelog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the real configuration against committed local history. These
// repositories have no remotes, credentials, hooks, or network requirements.
type fixture struct {
	root, git, cliff, hooks string
	env                     []string
	commits                 int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cliff, err := exec.LookPath("git-cliff")
	if err != nil {
		t.Skip("git-cliff is unavailable; run through the project's mise environment to verify changelog behavior")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("changelog integration tests require Git")
	}
	base := t.TempDir()
	f := &fixture{root: filepath.Join(base, "repository with spaces"), git: git, cliff: cliff, hooks: filepath.Join(base, "empty-hooks")}
	for _, path := range []string{f.root, f.hooks} {
		if err = os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	config, err := os.ReadFile(filepath.Join("..", "..", "cliff.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.root, "cliff.toml"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") || strings.HasPrefix(key, "CLIFF_") || key == "TZ" {
			continue
		}
		f.env = append(f.env, entry)
	}
	f.env = append(f.env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=Changelog Test", "GIT_AUTHOR_EMAIL=changelog@example.invalid",
		"GIT_COMMITTER_NAME=Changelog Test", "GIT_COMMITTER_EMAIL=changelog@example.invalid",
		"GIT_TERMINAL_PROMPT=0", "TZ=UTC")
	f.gitCommand(t, "init", "--initial-branch=main")
	return f
}

func (f *fixture) command(t *testing.T, binary string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir, cmd.Env = f.root, f.env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(binary), args, err, output)
	}
	return string(output)
}

func (f *fixture) gitCommand(t *testing.T, args ...string) string {
	t.Helper()
	flags := []string{"-c", "core.hooksPath=" + f.hooks, "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}
	return f.command(t, f.git, append(flags, args...)...)
}

func (f *fixture) commit(t *testing.T, message string) {
	t.Helper()
	f.commits++
	date := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(f.commits) * time.Minute).Format(time.RFC3339)
	original := f.env
	f.env = append(append([]string(nil), original...), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	defer func() { f.env = original }()
	f.gitCommand(t, "commit", "--allow-empty", "--no-verify", "-m", message)
}

func (f *fixture) tag(t *testing.T, tag string) {
	t.Helper()
	f.gitCommand(t, "tag", tag)
}

func (f *fixture) render(t *testing.T) string {
	t.Helper()
	return f.command(t, f.cliff, "--offline", "--no-exec", "--config", "cliff.toml")
}

// Select only a Markdown section's body, allowing unrelated template spacing
// and dates to evolve without a large generated-text golden file.
func section(t *testing.T, output, prefix, name string) string {
	t.Helper()
	var body []string
	found := false
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			title := strings.TrimPrefix(line, prefix)
			if title == name || strings.HasPrefix(title, name+" - ") {
				found = true
				continue
			}
			if found {
				break
			}
		}
		if found {
			body = append(body, line)
		}
	}
	if !found {
		t.Fatalf("missing section %q in:\n%s", prefix+name, output)
	}
	return strings.Join(body, "\n")
}

func contains(t *testing.T, output string, expected ...string) {
	t.Helper()
	for _, text := range expected {
		if !strings.Contains(output, text) {
			t.Errorf("missing %q in:\n%s", text, output)
		}
	}
}

func TestChangelogPreservesLegacyAndGroupsConventionalCommits(t *testing.T) {
	f := newFixture(t)
	for _, message := range []string{
		"Initial repository bootstrap",
		"fix an old thing",
		"feat(proxy): add observable flows",
		"fix(dns): retain complete response",
		"feat(api)!: replace management schema",
		"feat: change configuration layout\n\nBREAKING CHANGE: old configuration keys are no longer accepted",
	} {
		f.commit(t, message)
	}
	output := f.render(t)
	unreleased := section(t, output, "## ", "Unreleased")
	features := section(t, unreleased, "### ", "Features")
	fixes := section(t, unreleased, "### ", "Fixes")
	other := section(t, unreleased, "### ", "Other changes")
	contains(t, features,
		"**proxy:** add observable flows",
		"**api:** **BREAKING:** replace management schema",
		"**BREAKING:** change configuration layout")
	contains(t, fixes, "**dns:** retain complete response")
	contains(t, other, "Initial repository bootstrap", "fix an old thing")
	if strings.Contains(fixes, "fix an old thing") {
		t.Error("legacy prefix was incorrectly classified as a Conventional Commit fix")
	}
	if strings.Count(features, "**BREAKING:**") != 2 {
		t.Errorf("both ! and BREAKING CHANGE footer must mark breaking changes:\n%s", features)
	}
}

func TestChangelogSeparatesReleasedAndUnreleasedHistory(t *testing.T) {
	f := newFixture(t)
	f.commit(t, "feat: stable baseline")
	f.tag(t, "v1.2.3")
	f.commit(t, "fix: prepare candidate")
	f.tag(t, "nightly-build")
	f.commit(t, "feat: candidate addition")
	f.tag(t, "v1.3.0-rc.1")
	f.commit(t, "fix: pending correction")
	f.tag(t, "snapshot-latest")
	output := f.render(t)
	sections := []struct{ name, expected string }{
		{"Unreleased", "pending correction"},
		{"1.3.0-rc.1", "candidate addition"},
		{"1.2.3", "stable baseline"},
	}
	last := -1
	for _, item := range sections {
		body := section(t, output, "## ", item.name)
		contains(t, body, item.expected)
		for _, other := range sections {
			if item.name != other.name && strings.Contains(body, other.expected) {
				t.Errorf("%q leaked into release %q", other.expected, item.name)
			}
		}
		position := strings.Index(output, "## "+item.name)
		if position <= last {
			t.Errorf("release %q is out of newest-first order:\n%s", item.name, output)
		}
		last = position
	}
	contains(t, section(t, output, "## ", "1.3.0-rc.1"), "prepare candidate")
	if strings.Contains(output, "nightly-build") || strings.Contains(output, "snapshot-latest") {
		t.Errorf("non-version tags created release sections:\n%s", output)
	}
}

func TestChangelogNonVersionTagsDoNotChangeReleaseBoundaries(t *testing.T) {
	f := newFixture(t)
	f.commit(t, "feat: tagged baseline")
	f.tag(t, "v2.0.0")
	f.commit(t, "fix: pending after release")
	before := f.render(t)
	for i, tag := range []string{"nightly", "release-candidate", "2.1.0", "v2.1", "checkpoint/ready"} {
		f.tag(t, tag)
		after := f.render(t)
		if after != before {
			t.Fatalf("non-version tag %q changed generated history at fixture %d:\nbefore:\n%s\nafter:\n%s", tag, i, before, after)
		}
	}
}
