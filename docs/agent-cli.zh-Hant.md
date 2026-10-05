# Agent CLI 與公開操作 Skill

[English](agent-cli.md) · [繁體中文](agent-cli.zh-Hant.md)

`rillway agent` 是同一支 binary 裡的非互動指令，沿用現有管理 API 的權限、TLS、設定驗證及版本檢查，不需要額外 MCP 或 SDK。

```sh
rillway agent schema
rillway agent status
rillway agent status --url https://192.0.2.20:17892 \
  --token-file /private/path/admin.token --ca /private/path/admin.crt
```

範例位址與路徑請換成自己的資料。本機預設載入 OS 的設定，也可指定 `--config FILE`；不會建立不存在的設定或自動選取 TUI 記住的服務。遠端必須明確指定網址與權杖檔案，並保留 TLS 驗證，沒有直接輸入 token 或關閉驗證的選項。

| 指令 | 用途 |
| --- | --- |
| `schema` | 離線查詢版本、指令、參數、JSON 格式及結束狀態 |
| `validate --input FILE` | 離線驗證設定；不啟動 VPN 或測試網路 |
| `status` | 生效版本、自適應開關與出口狀態；略過登入連結及原始說明 |
| `config` | 完整設定，含私人位址／路徑；秘密只保留檔案參照 |
| `stats` | 既有連線觀察資料，可能包含私人目的地 |
| `plan --input FILE` | 比對目前版本，列出有改變的欄位名稱 |
| `apply --input FILE --yes` | 依目前版本套用完整候選設定 |
| `outbound --id ID --action connect\|disconnect\|verify --yes` | 明確執行出口操作；驗證會產生網路流量 |
| `restart --yes` | 在 daemon 內重啟監聽與 VPN；現有連線會中斷 |

輸入可用 `--input -` 讀取單一 JSON 物件，上限 256 KiB。先將 `agent config` 的 **`data` 物件**取出成私人 `0600` 候選檔，保留所有欄位與 `revision`；不要提交整份 CLI 包裝或自行增加版本。驗證、預覽後才套用已授權的修改。遇到衝突，重新取得設定並重做指定修改；不要只更換版本強制覆蓋。無變更時不寫入或增加版本。

預覽不會保留版本、啟動 VPN 或驗證被參照的檔案，因此回報 `runtime_verified: false`；伺服器仍可能拒絕套用。監聽／安全設定仍需在伺服器修改。註冊、授權、瀏覽器登入與設定匯入沿用現有 UI。

連線指令接受 `--config`、`--url`、`--token-file`、`--ca`、`--timeout`。網路時限從讀完輸入後開始，預設 15 秒，最大 2 分鐘；每個 HTTP 請求另有 15 秒上限。離線指令不接受連線參數。系統不自動重試寫入，回覆遺失時請先讀取目前狀態。`agent restart` 不載入新 binary；更新執行檔後需重啟 OS 服務。`--yes` 表示命令意圖，不代表 Agent 可以修改其他服務。

成功與失敗都只輸出一份 JSON，沒有提示、色碼或進度文字。固定欄位為 `schema_version: 1`、`ok`，以及成功時的 `data` 或失敗時的 `error`（`code`、`message`、`exit_code`）。`--lang en|zh-Hant` 只改變說明文字；請用固定 `code` 判斷錯誤。錯誤不回顯輸入值、私人路徑或原始 API／VPN 回應。

結束狀態：0 成功、1 伺服器拒絕／輸出失敗、2 輸入／參數／缺少明確意圖、3 憑證／權限、4 版本衝突、5 連線／TLS／逾時。完整參考見 [Agent CLI contract](../skills/rillway-ops/references/agent-cli.md)。

## 安裝 Skill

[rillway-ops](../skills/rillway-ops/SKILL.md) 採 MIT 授權，包含通用安裝／還原、出口與分流、PAC／Docker 故障排除及 CLI 方法。以英文撰寫方便分享，可用中英文提問；Agent 應依使用者選擇的語言回覆。Skill 不含實際部署紀錄、私人帳號或憑證，未宣告額外工具相依。

從經過審閱的 checkout 複製 `skills/rillway-ops` 到 Agent 的 Skill 目錄。採用此本機 Skill 路徑慣例的 Codex 可使用：

```sh
mkdir -p "${CODEX_HOME:-$HOME/.codex}/skills"
# 目的地若已有同名 Skill，請先檢查。
cp -R skills/rillway-ops "${CODEX_HOME:-$HOME/.codex}/skills/"
```

也可下載 Release 的 **`rillway-ops.zip`** 與 `SHA256SUMS`，核對雜湊、檢查七個指定檔案，再解壓到 Agent 的探索目錄。以 `$rillway-ops` 呼叫，例如：「檢查 Docker 拉取失敗，先確認原因，再提出針對性的修改。」不同 Agent 的探索／信任要求仍適用；保留一般自動選取行為。開發者可用 `mise run skill:package` 重建封包，發布時會計算雜湊及產生來源證明。封包拒絕符號連結與非一般檔案，不收集執行資料或對話。

設計參考 [CLI Guidelines](https://clig.dev/)、[GitHub CLI JSON](https://cli.github.com/manual/gh_help_formatting)、[Hugging Face Agent CLI](https://huggingface.co/blog/hf-cli-for-agents)與 [Agent Skills 格式](https://agentskills.io/specification)。這些是設計依據，不宣稱有通用「Agent Native」認證。

`config`、`stats` 即使不含秘密內容，仍可能識別私人設備及目的地。管理權杖提供與 Web UI 相同的管理權限；將憑證及日誌留在 Git 外，只發布清理過的報告。掃描與來源證明不保證識別所有秘密或漏洞。
