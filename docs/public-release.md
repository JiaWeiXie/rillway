# Preparing a public repository and release

[English](public-release.md) · [繁體中文](public-release.zh-Hant.md)

## Publishable source

The repository contains only source, synthetic examples, development configuration
and tests. Runtime configuration, deployment records, account state, tokens, keys,
backups and local caches belong outside Git. `.local/` is an ignored convenience,
not a security vault. GitHub's automatic source archive contains tracked files,
so audit source and history before the first push.

```sh
mise run security
mise run check
mise run release
```

`security:source` scans tracked working files, all reachable historical blobs and
commit metadata. It rejects private/generated paths, personal home paths and private
commit emails. `security:secrets` scans all reachable Git commits with the built-in
Gitleaks rules plus VPN/license and home-path rules. Neither check can identify all
personal information, arbitrary hostnames, disguised secrets or image contents.
Review screenshots and examples manually. CI cannot retroactively prevent data
from being published by a push; enable push protection before uploading source.

For additional operator-specific names, emails, IPs and license/token values, put
one literal per line in a private file outside Git, then run:

```sh
RILLWAY_PRIVACY_PATTERNS=/path/outside/repository/private-denylist.txt \
  mise run security:source
```

Do not commit that list or put private values in a command line. The scanner does
not print matched values. Run this before every public push from a production
operator's checkout. A normal clone without this optional file still runs the
public structural checks. Review Git refs and tags as well as `main`; never upload
a private Git bundle, ignored deployment folder, diagnostic log or whole workspace.

## GitHub settings before publication

This checkout is not connected to a GitHub repository yet. The workflow files do
not configure repository settings by themselves. Before pushing or changing visibility:

1. Create the intended repository privately, confirm the license, and enable secret
   scanning, push protection, Dependabot alerts and private vulnerability reporting.
   Use account 2FA/passkeys and a GitHub noreply email.
2. Protect `main`: require PR review, resolved conversations, the CI `security`,
   `check (ubuntu-24.04)`, `check (macos-15)`, `cross-build` and `service-acceptance`
   checks, and disallow deletion/force pushes. Restrict bypasses to designated maintainers.
   If using a merge queue, add its trigger and validate CI before enabling it.
3. Protect `v*` tags against creation/update/deletion by non-maintainers. Restrict
   workflow/security/release changes through review and configure CODEOWNERS using
   actual public maintainer identities. Do not use invented owner handles.
4. Create the `release` environment with required maintainer approval and allowed
   deployment tags `v*`. Configure rules **before** any tag push: referencing an
   environment in YAML alone does not create approval protection.
5. Set default Actions permissions to read-only; disable Actions approving PRs,
   require approval for outside contributors, and restrict actions to the reviewed
   pinned sources. Do not use self-hosted runners or add private deployment/VPN secrets.
6. Audit all refs and publishable files. Push only intended branches/tags, run CI in
   the private repository if the account plan supports it, then make the source public.
   Configure repository protections in the plan that will apply to public visibility.

There are no required long-lived repository secrets. Public PRs never trigger
`pull_request_target`/`workflow_run`, do not receive private credentials, and do not
write releases. Automated dependency updates must retain immutable action SHAs.

## Release flow

Release is opt-in: a maintainer pushes a version tag reachable from reviewed `main`.
The tag must be `vMAJOR.MINOR.PATCH`, optionally with a prerelease suffix. Example:

```sh
git tag -a v0.1.0 -m 'Rillway v0.1.0'
git push origin v0.1.0
```

These commands publish a tag; only run them after repository setup and release
approval. The build job validates the tag/main ancestry, repeats security checks,
race tests and fuzzing, then builds four binaries with the tag as their CLI version.
It uploads an explicit file list. The separate `release` environment job downloads
only this run's artifact, verifies checksums, generates provenance attestations,
and creates a **draft** GitHub Release. It never executes repository scripts or
binaries with write privileges. Inspect the draft and attestations before publishing.

Assets are standalone Linux/macOS binaries for amd64/arm64, SHA256SUMS, the exact
Go module inventory, LICENSE and THIRD_PARTY.md. Full dependency/font notices are
embedded (`rillway licenses`). The VM does not need Go, mise or Node. macOS binaries
are not Developer ID signed/notarized; Linux/macOS cross-builds do not prove OS or
VPN compatibility. Version tags are not code signing.

Verify a downloaded Linux binary before installing:

```sh
sha256sum --check SHA256SUMS
# Replace OWNER/REPO with the actual repository, not an example account.
gh attestation verify ./rillway-linux-amd64 --repo OWNER/REPO \
  --signer-workflow OWNER/REPO/.github/workflows/release.yml
```

Provenance uses GitHub's OIDC/Sigstore service and does not need a private signing
key in the repository. Attestations authenticate the producing workflow, not the
absence of vulnerabilities. The publishing job attests binaries built by this same
workflow; its permissions and the release environment must remain protected.

## Incident handling

Immediately revoke/rotate exposed credentials. Stop public distribution of an
affected release, remove affected assets and inspect access logs without publishing
those logs. Rewrite affected refs where appropriate, re-run scans, notify affected
users privately, and coordinate cache removal with GitHub support. Existing clones
and downloaded artifacts cannot be erased by rewriting local Git history.

Official references: [GitHub Actions security](https://docs.github.com/en/actions/reference/security/secure-use),
[artifact attestations](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations),
[Gitleaks](https://github.com/gitleaks/gitleaks).
