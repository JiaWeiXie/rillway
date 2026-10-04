# AI Agent 專案設定

`AGENTS.md` 是 Codex 與其他 coding agent 的共用專案規範。`CLAUDE.md` 只引用它，避免同一規則維護兩份。這些設定不指定模型、不替換使用者的全域權限，也不呼叫外部通知服務。

## 本機 hooks

| 時機 | 入口 | 行為 |
|---|---|---|
| Codex 完成編輯／Shell 工具 | `.codex/hooks.json` 的 `PostToolUse` | 呼叫共用的 `scripts/hooks/post-edit.sh` |
| Claude Code 完成編輯／Shell 工具 | `.claude/settings.json` 的 `PostToolUse` | 呼叫相同的檢查程式 |
| Git commit 前 | `.githooks/pre-commit` | 檢查即將提交的 staged snapshot |
| Git commit 訊息確認 | `.githooks/commit-msg` | 用 git-cliff 驗證 Conventional Commits 格式 |

編輯後檢查會找出修改、新增及刪除的 Go 原始碼，執行相關 package 的 Lint／格式檢查與一般測試。module 或工具設定變更時擴大到整個專案。Web UI 的 embedded assets 變更也會檢查所屬 package。

修改 hook 腳本時會執行 `scripts/hooks` 的回歸測試。檢查程式有 90 秒總時限；若檢查期間來源又被修改，本次結果不寫入快取，避免把結果套到錯誤的版本。

同一份變更內容檢查一次，避免每個讀取工具都重跑。紀錄放在忽略的 `.cache/agent-hooks/`，只有內容雜湊和結果狀態；不儲存工具輸入或對話內容。編輯中失敗會回饋給 Agent，讓它修正，不會撤回已完成的編輯。

Git hook 使用暫存目錄檢查 Git index 的完整副本，所以支援只暫存檔案的一部分。檢查失敗會阻止 commit。沒有 Go／工具設定變更時略過耗時的 Go 檢查。兩種 hooks 都不修改原始碼、不執行 `git add`，也不啟動真實 VPN、部署、帳號登入或下載測速。

編輯階段的相關 package 測試不能取代最終完整驗證；完成一批功能變更後仍執行 `mise run check`，包含 race detector。

## 啟用

開發工具依 `mise.toml` 和 lockfile 安裝：

```sh
mise trust
mise install
mise run hooks:install
```

安裝器只修改本 repository 的 `core.hooksPath=.githooks`。若原本有其他 hook manager 或啟用中的 Git hook，會保留原設定並說明需要整合的位置。每個新 clone 需執行一次；Git 不會從 repository 檔案自動安裝 hooks。

Codex 需在以 Rillway 為工作目錄的 session 中開啟 `/hooks`，檢查並信任此專案的 `PostToolUse` 定義。新 hook 或定義變更後，Codex 的信任機制會先略過，直到使用者完成審閱；安裝器不繞過此機制。這是 Codex 的產品要求。[官方 hooks 文件](https://developers.openai.com/codex/hooks)

Claude Code 從專案 `.claude/settings.json` 載入 hooks；重新開啟專案 session 後可在 `/hooks` 檢查。新增設定可能需要工具本身的 workspace trust。`settings.local.json` 留給個人調整，已從 Git 排除。[Claude Code hooks 文件](https://code.claude.com/docs/en/hooks)

本機 hooks 只適用 Linux／macOS 開發環境，Agent 的 PATH 必須能找到 `mise`。有安裝 RTK 時透過它執行工具；CI 或其他沒有 RTK 的環境則直接執行同樣命令。產品 binary 不依賴這些開發 hooks。

## 手動檢查與排錯

```sh
# 檢查當前 staged 內容，保持工作目錄與 index 不變。
mise run hooks:check

# 模擬編輯完成事件；程式只讀取事件種類，不執行傳入的命令。
printf '%s\n' '{"hook_event_name":"PostToolUse"}' | sh scripts/hooks/post-edit.sh

# 完整品質檢查。
mise run check
```

若編輯檢查沒有自動執行，先在 Agent 的 `/hooks` 看它是否已載入與信任，再確認工作目錄是 Rillway。不要用停用 sandbox 或 bypass hook trust 的方式處理。

Git hook 是本機開發回饋，仍可能被 Git 的跳過選項略過；GitHub Actions 的完整檢查是另一道獨立驗證。

Commit 訊息格式、git-cliff 分類與 changelog 任務見 [Git hooks 與 changelog](changelog.md)。
