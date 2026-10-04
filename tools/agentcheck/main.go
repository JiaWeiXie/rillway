// Command agentcheck provides bounded, advisory checks after an agent edits files.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxInput   = 256 << 10
	maxOutput  = 12 << 10
	maxGitList = 1 << 20
	maxSource  = 8 << 20
	maxAsset   = 32 << 20
	cacheDir   = ".cache/agent-hooks"
)

type runner interface {
	Run(context.Context, string, int, ...string) ([]byte, error)
}

type commandRunner struct{}

// limitedBuffer consumes all output while retaining only its bounded prefix.
type limitedBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := b.limit - len(b.data); remaining < len(p) {
		b.data = append(b.data, p[:remaining]...)
		b.truncated = true
	} else {
		b.data = append(b.data, p...)
	}
	return n, nil
}

func (commandRunner) Run(ctx context.Context, root string, limit int, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = root
	cmd.Env = checkEnvironment(root)
	cmd.WaitDelay = time.Second
	output := &limitedBuffer{limit: limit}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	if output.truncated && args[0] == "git" {
		return nil, errors.New("git change list exceeded the size limit")
	}
	return output.data, err
}

func checkEnvironment(root string) []string {
	blocked := map[string]bool{
		"GOFLAGS": true, "GOENV": true, "GOWORK": true, "GOTOOLCHAIN": true,
		"GOCACHE": true, "GOMODCACHE": true, "GOPATH": true, "GOLANGCI_LINT_CACHE": true,
		"RILLWAY_LIVE_CONFIG": true, "GIT_DIR": true, "GIT_WORK_TREE": true,
		"GIT_INDEX_FILE": true, "GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !blocked[key] {
			env = append(env, entry)
		}
	}
	return append(env, "GOFLAGS=-mod=readonly", "GOENV=off", "GOWORK=off", "GOTOOLCHAIN=local",
		"GOCACHE="+filepath.Join(root, ".cache/go-build"),
		"GOMODCACHE="+filepath.Join(root, ".cache/gomod"),
		"GOPATH="+filepath.Join(root, ".cache/go"),
		"GOLANGCI_LINT_CACHE="+filepath.Join(root, ".cache/golangci-lint"))
}

type checker struct {
	root string
	run  runner
	now  func() time.Time
}

type record struct {
	Hash   string `json:"hash"`
	Status string `json:"status"`
}

func main() {
	if len(os.Args) != 2 || os.Args[1] != "post-edit" {
		return
	}
	root, err := os.Getwd()
	if err != nil {
		emit(os.Stdout, "Rillway 編輯後檢查無法取得專案位置；請從專案根目錄重試。")
		return
	}
	c := checker{root: root, run: commandRunner{}, now: time.Now}
	handle(os.Stdin, os.Stdout, c)
}

func handle(input io.Reader, output io.Writer, c checker) {
	event, err := readEvent(input)
	if err != nil {
		emit(output, "Rillway 編輯後檢查略過：事件資料格式錯誤或超過 256 KiB；請檢查 hook wrapper。")
		return
	}
	if event != "PostToolUse" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	emit(output, c.check(ctx))
}

func readEvent(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxInput+1))
	if err != nil || len(data) > maxInput {
		return "", errors.New("invalid event size")
	}
	var event struct {
		Name string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return "", err
	}
	return event.Name, nil
}

func emit(w io.Writer, message string) {
	if message == "" {
		return
	}
	message = boundedText(message)
	response := struct {
		Output struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}{}
	response.Output.Event, response.Output.Context = "PostToolUse", message
	_ = json.NewEncoder(w).Encode(response)
}

func boundedText(s string) string {
	if len(s) > maxOutput {
		s = s[:maxOutput-32]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "\n[output truncated]"
	}
	return s
}

func (c checker) check(ctx context.Context) string {
	changed, err := c.changes(ctx)
	if err != nil {
		return "Rillway 無法列出 Git 變更；請確認從 repository 根目錄執行，且 HEAD 已存在。"
	}
	paths, packages, err := c.selectChanges(changed)
	if err != nil {
		return "Rillway 略過不安全或無法讀取的變更路徑；請檢查符號連結及檔案權限。"
	}
	if len(paths) == 0 {
		return ""
	}
	unlock, err := c.lock()
	if err != nil {
		return "Rillway 檢查正在執行，或快取目錄無法安全使用；本次略過。"
	}
	defer unlock()
	hash, err := c.treeHash(ctx, paths)
	if err != nil {
		return "Rillway 無法安全讀取程式來源，或來源超過檢查上限；請手動執行 mise run check。"
	}
	if previous, ok := c.cached(); ok && previous.Hash == hash {
		if previous.Status == "failed" {
			return "Rillway：相同程式內容先前檢查未通過，本次不重跑。請修正先前回報的問題後再檢查。"
		}
		return ""
	}
	commands := [][]string{
		append([]string{"golangci-lint", "run", "--allow-serial-runners"}, packages...),
		append([]string{"go", "test", "-timeout=45s"}, packages...),
	}
	var failures []string
	for _, args := range commands {
		if ctx.Err() != nil {
			failures = append(failures, "檢查超過總共 90 秒時限；請手動執行 mise run check。")
			break
		}
		out, err := c.run.Run(ctx, c.root, maxOutput, args...)
		if err != nil {
			detail := strings.TrimSpace(string(out))
			if detail == "" {
				detail = err.Error()
			}
			failures = append(failures, fmt.Sprintf("%s 未通過：\n%s", args[0], detail))
		}
	}
	currentHash, err := c.treeHash(ctx, paths)
	if err != nil || currentHash != hash {
		return "Rillway：檢查期間程式內容已改變，或無法再次確認來源；本次結果未快取。請對目前內容重新執行檢查。"
	}
	status := "passed"
	message := "Rillway 編輯後檢查通過（lint 與相關 Go 測試）。"
	if len(failures) > 0 {
		status = "failed"
		message = strings.Join(failures, "\n\n") + "\n\n請修正上述問題；來源變更後會重新檢查。這是 advisory，不阻擋工具結果。"
	}
	if err := c.save(record{Hash: hash, Status: status}); err != nil {
		message += "\n檢查快取無法寫入。"
	}
	return boundedText(message)
}

func (c checker) changes(ctx context.Context) ([]string, error) {
	var paths []string
	for _, args := range [][]string{
		// Report renames as delete/add so removal of the old package is visible.
		{"git", "diff", "--no-ext-diff", "--no-renames", "HEAD", "--name-only", "-z", "--"},
		{"git", "ls-files", "--others", "--exclude-standard", "-z", "--"},
	} {
		out, err := c.run.Run(ctx, c.root, maxGitList, args...)
		if err != nil {
			return nil, err
		}
		for _, p := range strings.Split(string(out), "\x00") {
			if p != "" {
				paths = append(paths, p)
			}
		}
	}
	return paths, nil
}

func ignored(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		switch strings.ToLower(part) {
		case ".git", ".cache", ".local", ".state", "state", "secret", "secrets", "node_modules", "vendor", "run", "logs", "build", "dist", "bin", "tmp", "coverage":
			return true
		}
		if part == ".env" || strings.HasPrefix(part, ".env.") {
			return true
		}
	}
	return false
}

func relevant(path string) bool {
	if ignored(path) {
		return false
	}
	return strings.HasSuffix(path, ".go") || fullCheckFile(path) || hookScript(path) || path == "cliff.toml" || strings.HasPrefix(filepath.ToSlash(path), "internal/control/web/")
}

// Embedded fonts and images are hashed too: they change the shipped UI and
// must invalidate the cached check, but can legitimately exceed source limits.
func sourceLimit(path string) int64 {
	if strings.HasPrefix(filepath.ToSlash(path), "internal/control/web/") {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".ttf", ".woff2", ".png", ".webp", ".jpg", ".jpeg", ".ico":
			return maxAsset
		}
	}
	return maxSource
}

func hookScript(path string) bool {
	path = filepath.ToSlash(path)
	return path == ".githooks/pre-commit" || path == ".githooks/commit-msg" || path == "scripts/hooks/commit-cliff.toml" || (strings.HasPrefix(path, "scripts/hooks/") && strings.HasSuffix(path, ".sh"))
}

func fullCheckFile(path string) bool {
	switch path {
	case "go.mod", "go.sum", "go.work", "go.work.sum", ".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json", "mise.toml", ".mise.toml", "mise.lock":
		return true
	}
	return false
}

// safePath rejects traversal and every existing symlink component, including
// parent directories. Missing final paths are allowed for deleted source files.
func (c checker) safePath(path string) (fs.FileInfo, error) {
	if filepath.IsAbs(path) || path == "." || filepath.Clean(path) != path || strings.ContainsRune(path, '\x00') {
		return nil, errors.New("unsafe path")
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	var info fs.FileInfo
	for i, part := range parts {
		if part == ".." || part == "" {
			return nil, errors.New("unsafe path")
		}
		var err error
		info, err = os.Lstat(filepath.Join(c.root, filepath.Join(parts[:i+1]...)))
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("symlink or inaccessible path")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, errors.New("non-directory parent")
		}
	}
	return info, nil
}

func (c checker) selectChanges(changed []string) ([]string, []string, error) {
	paths, packages := map[string]bool{}, map[string]bool{}
	all := false
	for _, path := range changed {
		if !relevant(path) {
			continue
		}
		info, err := c.safePath(path)
		if err != nil {
			return nil, nil, err
		}
		if info != nil && !info.Mode().IsRegular() {
			return nil, nil, errors.New("non-regular source")
		}
		paths[path] = true
		if fullCheckFile(path) {
			all = true
			continue
		}
		if hookScript(path) {
			packages["./scripts/hooks"] = true
			continue
		}
		if path == "cliff.toml" {
			packages["./scripts/changelog"] = true
			continue
		}
		if strings.HasPrefix(filepath.ToSlash(path), "internal/control/web/") {
			packages["./internal/control"] = true
			continue
		}
		dir := filepath.Dir(path)
		if info == nil && !c.hasGo(dir) {
			all = true
		}
		if dir == "." {
			packages["."] = true
		} else {
			packages["./"+filepath.ToSlash(dir)] = true
		}
	}
	if all {
		return sortedKeys(paths), []string{"./..."}, nil
	}
	return sortedKeys(paths), sortedKeys(packages), nil
}

func sortedKeys(m map[string]bool) []string {
	result := make([]string, 0, len(m))
	for key := range m {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func (c checker) hasGo(dir string) bool {
	entries, err := os.ReadDir(filepath.Join(c.root, dir))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.HasSuffix(entry.Name(), ".go") {
			return true
		}
	}
	return false
}

func (c checker) treeHash(ctx context.Context, changed []string) (string, error) {
	root, err := os.OpenRoot(c.root)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	h := sha256.New()
	_, _ = io.WriteString(h, "rillway-agentcheck-v1\x00")
	for _, path := range changed {
		_, _ = fmt.Fprintf(h, "change:%s\x00", path)
	}
	total, count := int64(0), 0
	err = filepath.WalkDir(c.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		relative, err := filepath.Rel(c.root, path)
		if err != nil || relative == "." {
			return err
		}
		if ignored(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !relevant(relative) {
			return nil
		}
		info, err := c.safePath(relative)
		if err != nil || info == nil || !info.Mode().IsRegular() {
			return errors.New("unsafe source")
		}
		count++
		total += info.Size()
		limit := sourceLimit(relative)
		if info.Size() > limit || total > 128<<20 || count > 10000 {
			return errors.New("source size limit")
		}
		// Root also prevents an escaping symlink introduced between Lstat/Open.
		file, err := root.Open(relative)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(h, "source:%s\x00%d\x00", relative, info.Size())
		n, copyErr := io.Copy(h, io.LimitReader(file, limit+1))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || n > limit {
			return errors.New("source read failed")
		}
		_, _ = io.WriteString(h, "\x00")
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (c checker) lock() (func(), error) {
	if _, err := c.safePath(filepath.FromSlash(cacheDir)); err != nil {
		return nil, err
	}
	dir := filepath.Join(c.root, filepath.FromSlash(cacheDir))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, "lock")
	err := os.Mkdir(lockPath, 0o700)
	if os.IsExist(err) {
		info, statErr := os.Lstat(lockPath)
		if statErr == nil && info.IsDir() && c.now().Sub(info.ModTime()) > 3*time.Minute {
			if removeErr := os.Remove(lockPath); removeErr == nil {
				err = os.Mkdir(lockPath, 0o700)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(lockPath) }, nil
}

func (c checker) cached() (record, bool) {
	path := filepath.FromSlash(cacheDir + "/checked.json")
	info, err := c.safePath(path)
	if err != nil || info == nil || !info.Mode().IsRegular() || info.Size() > 1024 {
		return record{}, false
	}
	data, err := os.ReadFile(filepath.Join(c.root, path))
	var result record
	if err != nil || json.Unmarshal(data, &result) != nil || len(result.Hash) != 64 || (result.Status != "passed" && result.Status != "failed") {
		return record{}, false
	}
	return result, true
}

func (c checker) save(result record) error {
	path := filepath.FromSlash(cacheDir + "/checked.json")
	if _, err := c.safePath(path); err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	dir := filepath.Join(c.root, filepath.FromSlash(cacheDir))
	file, err := os.CreateTemp(dir, ".record-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	_, writeErr := io.Copy(file, bytes.NewReader(data))
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(c.root, path))
}
