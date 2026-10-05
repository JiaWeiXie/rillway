# 公開專案與發布流程

[English](public-release.md) · [繁體中文](public-release.zh-Hant.md)

## 公開前先檢查

Git 只放原始碼、假資料範例、開發設定與測試。正式設定、Token、金鑰、VPN state、備份與部署紀錄放在 repository 外。`.local/` 雖已忽略，仍應限制權限；它不是秘密保管庫。GitHub 自動提供的 source archive 也會包含所有追蹤檔案。

```sh
mise run security
mise run check
mise run release
```

`security:source` 檢查追蹤檔案、所有可達的歷史 blob 與 commit metadata，拒絕私人／產物路徑、個人家目錄及私人 commit 信箱。`security:secrets` 使用 Gitleaks 內建規則及 VPN／license 補充規則，掃描所有可達的 Git 紀錄，輸出會遮蔽命中值。

工具不能辨識所有個人資料、任意主機名稱、偽裝的秘密或圖片內容。截圖與範例必須人工檢查；CI 在 push 後才執行，不能收回已公開的資料。第一次上傳前先啟用 push protection。

正式環境使用者可在 Git 外建立私人清單，每行一個實際姓名、信箱、IP、主機名稱或秘密值，再執行：

```sh
RILLWAY_PRIVACY_PATTERNS=/path/outside/repository/private-denylist.txt \
  mise run security:source
```

不要提交這份清單，也不要把秘密寫進命令參數。檢查不會印出命中值；沒有私人清單的一般 clone 仍會執行結構檢查。除了 `main`，也要檢查其他 refs／tags。不要上傳私人 Git bundle、整個工作目錄、部署紀錄或診斷日誌。

## GitHub 必須另外設定

目前 checkout 沒有 GitHub remote。Workflow 不會自動啟用 repository 保護，第一次 push／公開前應完成：

1. 先建立私有 repository，確認 LICENSE；啟用 secret scanning、push protection、Dependabot alerts、私下漏洞回報。帳號使用 2FA／passkey，Git 使用 noreply 信箱。
2. 保護 `main`：要求 PR 審查、對話已解決、`security`、`check (ubuntu-24.04)`、`check (macos-15)`、`cross-build`、`service-acceptance` 檢查通過；禁止刪除與 force push。限制 bypass 權限。若啟用 merge queue，先補上對應 CI trigger 並驗證。
3. 保護 `v*` tags，限制建立、修改與刪除權限。Workflow、驗證、發布與安全政策變更須經審查；CODEOWNERS 使用實際的公開維護者身分，不能填不存在的帳號。
4. 建立 `release` environment，設定維護者審核及只允許 `v*` tags。必須在第一次推 tag 前設定；YAML 引用 environment 本身不代表已有審核保護。
5. Actions 預設唯讀、禁止自動核准 PR、要求外部貢獻者執行前審核，限制允許使用的 action。不要用能接觸家用網路的 self-hosted runner，也不要加入部署／VPN 秘密。
6. 確認可公開內容與 refs，只推送指定 branch／tag；依帳號方案在私有 repository 跑 CI，再公開。公開時使用的方案也必須支援已設定的保護。

這些 workflows 不需要長效 repository secrets。公開 PR 沒有私人憑證，使用唯讀 Token，不會建立 Release，也不使用 `pull_request_target`／`workflow_run`。Dependabot 每週提出 Go／Actions 更新，維護者仍需審查與保留固定 action SHA。

## GitHub Release

維護者在審查完成的 `main` 上推送 `vMAJOR.MINOR.PATCH` tag，可帶 prerelease suffix。例如：

```sh
git tag -a v0.1.0 -m 'Rillway v0.1.0'
git push origin v0.1.0
```

這會公開 tag，請只在 repository 設定與發布核准完成後執行。建置工作先確認 tag 格式、commit 屬於 `main`，再執行安全檢查、race tests、fuzz 與四平台建置。CLI 版本使用 tag。產物以明確檔案清單上傳。

有寫入權限的工作另外隔離，使用受保護的 `release` environment，只下載本次 workflow 的產物，核對 checksum、產生來源證明，再建立 **Draft Release**。不執行 repository 腳本或 binary。檢查 draft 與來源證明後，維護者才正式發布。

發布內容為 Linux／macOS 的 amd64／arm64 單一 binary、SHA256SUMS、Go module 清單、LICENSE 與 THIRD_PARTY.md。依賴與字體授權已內嵌，可用 `rillway licenses` 查看。VM 不需 Go、mise 或 Node。macOS 產物尚未經 Developer ID 簽章／notarization；跨平台編譯與版本 tag 都不能代替實機／VPN 驗證或程式簽章。

下載後可驗證：

```sh
sha256sum --check SHA256SUMS
gh attestation verify ./rillway-linux-amd64 --repo OWNER/REPO \
  --signer-workflow OWNER/REPO/.github/workflows/release.yml
```

`OWNER/REPO` 改成實際 repository。來源證明使用 GitHub OIDC／Sigstore，不必保存私人簽章金鑰；它確認產出來源，沒有承諾程式完全沒有漏洞。發布權限與 environment 必須保持受控。

## 發生外洩

先撤銷／輪替秘密，再停止發送受影響的 Release、移除產物及檢查存取紀錄。必要時改寫 refs、重跑掃描、私下通知受影響的人，並向 GitHub 協調移除快取。不要把診斷資料再次公開。Git 歷史改寫無法刪除他人的 clone 或已下載的檔案。

官方參考：[Actions 安全設定](https://docs.github.com/en/actions/reference/security/secure-use)、[產物來源證明](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations)、[Gitleaks](https://github.com/gitleaks/gitleaks)。
