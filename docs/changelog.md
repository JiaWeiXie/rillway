# Git hooks 與 changelog

Git hooks 使用 repository 的 `.githooks`，git-cliff 版本與四個 Linux／macOS 平台的下載校驗由 `mise.toml`／`mise.lock` 固定。首次 clone 後執行：

```sh
mise trust
mise install
mise run hooks:install
```

`pre-commit` 檢查真正暫存的程式碼與測試；`commit-msg` 使用 git-cliff 的官方 Conventional Commits 解析器檢查新訊息。後者只在會自動清除的暫存 repository 建立一筆訊息，既有歷史不參與格式檢查，專案的 index、refs 與訊息檔保持不變。詳細的程式碼檢查流程見 [Agent 工作流程](agent-workflow.md)。

## 提交訊息

使用 `type(scope): description`；scope 可省略。例如：

```text
feat(proxy): add a per-domain outbound override
fix(dns): keep private lookup failures inside the selected provider
docs: document WARP+ setup
chore(tooling): update development tools
```

不相容變更可使用 `feat(api)!: ...`，或在訊息結尾加入 `BREAKING CHANGE: ...` 段落。訊息可以使用中文。常用分類有 `feat`、`fix`、`perf`、`refactor`、`docs`、`test`、`build`、`ci`、`chore`、`style`、`revert`；其他符合 Conventional Commits 的 type 仍可提交，changelog 歸入 Other changes。

一般提交、合併與 revert 都遵守相同格式。例如合併時指定 `chore: merge feature branch`，revert 時將預設主旨改為 `revert: undo outbound change`。本機 hook 可被 Git 的跳過選項略過，因此程式碼品質仍由 CI 獨立檢查。

## 產生與預覽

```sh
# 只預覽。
mise run changelog:preview

# 依已提交的歷史重建 CHANGELOG.md。
mise run changelog

# 驗證設定與目前歷史，不改檔。
mise run changelog:check
```

`cliff.toml` 保留既有不符合新格式的提交，不改寫歷史。`vMAJOR.MINOR.PATCH` 及 prerelease／build metadata 標籤作為版本界線；最後一個版本之後的提交放入 Unreleased。無遠端 URL 時不產生無效的 commit 連結。

既有版本段落以已提交內容為準。squash 後的提交無法由目前歷史重建先前手動整理的項目，因此發布準備只取用新版本段落，不覆寫舊版本內容。

Changelog 以發布前更新為主，hooks 不會自動修改或暫存它。若要在建立提交前把該筆訊息納入，可使用 `--with-commit 'chore(release): prepare release notes'`，並以同樣訊息提交。

要預覽下一版的發布內容，可明確指定版本名稱：

```sh
mise exec -- git-cliff --offline --no-exec --config cliff.toml --tag v0.1.0
```

這只產生文字，不會建立 tag、commit、GitHub release 或部署。確認內容後才依當次發布需求執行後續動作。

所有 changelog 任務都使用 `--offline --no-exec`。CI 取得完整 Git 歷史，驗證設定，並用臨時 Git repository 測試分類、breaking changes 與版本分段。一般 `mise run check` 會一併執行這些測試。

設定依據：[git-cliff Git 設定](https://github.com/orhun/git-cliff/blob/main/website/docs/configuration/git.md)、[CLI 使用範例](https://github.com/orhun/git-cliff/blob/main/website/docs/usage/examples.md)。

## v0.10.0 升級前言與正式發布說明

`cliff.toml` 的版本模板在 `v0.10.0` 段落開頭加入英文與繁體中文前言，依序說明
立即生效的升級行為、指定來源與目的地的例外、切回 `open`，以及尚未驗證的範圍。
前言只對此版本輸出，不會套用至其他已發布版本或未來版本。

準備時先執行 `mise run changelog`，再使用既有的版本預覽方式產生
`v0.10.0` 段落；將新段落放入 `CHANGELOG.md`，保留已提交的舊版內容。
`--tag v0.10.0` 只是給產生器的版本名稱，不會建立 Git tag。

正式發布 workflow 不讀取 `CHANGELOG.md` 作為 GitHub Release 內文。它在
已核准版本 tag 的來源上執行以下現有指令：

```sh
mise exec -- git-cliff --offline --no-exec --config cliff.toml --latest --output dist/RELEASE_NOTES.md
```

這會把同一份版本前言放在依 history 分類的提交清單之前。產生的檔案隨
`verified-release` artifact 傳入 publish job，再由現有的 `gh release create`
以 `--notes-file release/dist/RELEASE_NOTES.md` 建立 draft Release。只編輯
`CHANGELOG.md` 不會改變這份重新產生的發布說明。建立 tag 與發布仍須另外核准。

The v0.10.0 introduction is part of the existing git-cliff version template.
The release workflow regenerates `dist/RELEASE_NOTES.md` from the tagged history
and that template, then passes the reviewed artifact to `gh release create`
with `--notes-file`. It does not use the committed `CHANGELOG.md` as the release
body. No workflow change is needed; preparing these documents creates no tag
or release.
