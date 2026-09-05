# Testing and release verification

This document describes the automated gates, manual acceptance paths, and the
two-stage release verification for GH Checks. Run mutation tests only against a
disposable repository and pull request: approve, review, workflow dispatch,
update-branch, and merge use the permissions of the authenticated `gh` account.

## Automated gates

CI runs on pushes, pull requests, and manual dispatches with read-only repository
permissions. The primary test matrix uses both the minimum supported Go release
(1.25.14) and the current patched toolchain (1.27.1).

| Gate | Coverage |
| --- | --- |
| Formatting and modules | `gofmt`, `go mod verify`, and `go mod tidy -diff` |
| Go correctness | unit tests, statement coverage, race detector, and `go vet` |
| Static analysis | Staticcheck v0.8.1 and Govulncheck v1.7.0 |
| Native tests | Go 1.27.1 tests on macOS and Windows, including platform-specific files |
| Cross-build | Linux, macOS, and Windows on amd64 and arm64 with CGO disabled |
| Scripts and workflows | POSIX parser, ShellCheck, Actionlint v1.7.12, and the Windows PowerShell parser |

Run the main Go gates locally:

```bash
gofmt -l .
go mod verify
go mod tidy -diff
go test -count=1 -cover ./...
go test -count=1 -race ./...
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
```

The Linux CI matrix runs these gates on Go 1.25.14 and 1.27.1. Separate macOS
and Windows jobs execute the tests natively on Go 1.27.1 so platform-specific
implementations and tests are exercised rather than merely cross-compiled.

Run the repository-format checks locally:

```bash
/bin/sh -n scripts/fetch-or-build.sh
shellcheck -x scripts/fetch-or-build.sh
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
```

On Windows, parse the installer without executing it:

```powershell
$tokens = $null
$parseErrors = $null
$scriptPath = (Resolve-Path "scripts/fetch-or-build.ps1").Path
[System.Management.Automation.Language.Parser]::ParseFile(
  $scriptPath,
  [ref]$tokens,
  [ref]$parseErrors
) | Out-Null
if ($parseErrors.Count -ne 0) { $parseErrors; exit 1 }
```

## Manual acceptance matrix

Use Herdr 0.7.0 or newer, an authenticated `gh`, Git, Neovim, and a Nerd Font.
Exercise at least macOS or Linux before every release. Exercise native Windows
with Git Bash or MSYS2 before claiming it as verified; WSL follows the Linux
path and does not replace a native-Windows check.

Record the OS, architecture, Herdr/Go/gh/Neovim versions, commit SHA, date, and
tester for every manual run.

### Install and launch

1. In a clean checkout, run `go build -o herdr-gh-checks .` and
   `herdr plugin link .`.
2. Open the `panel` entrypoint from a Herdr workspace whose cwd is a GitHub
   repository.
3. Confirm that a repository with no current PR shows the branch and its
   checked-out commit's GitHub Actions results, alongside the no-open-PR state.
   Check running, passing, failing, and empty results on `main`, and confirm
   that polling continues after completion and the CHECKS section folds.
   A new unpushed commit must not inherit a previous commit's passing result.
4. Confirm that an open PR displays its title, body, review decision, labels,
   assignees, changed files, and CI buckets. Check pending, passing, failing,
   closed, and merged states.
5. Resize the pane, filter files with `/`, navigate with arrows and `j`/`k`, and
   toggle all four fold sections.
6. Click the shortcut on the first row: `#<number>` opens the displayed PR,
   including when viewing another PR; without a PR, the branch name opens that
   branch on GitHub. Confirm that `o` does the same, the shortcut stays at the
   top in a short pane, and clicks in modal dialogs do not open a browser.

### Review and annotation

1. Open one changed file and use `ga` in Neovim. Verify that the notes manager
   shows the exact repository-relative `path:line` and note text.
2. Edit and delete notes, close and reopen the pane, and verify persistence.
3. Run the all-files diff and confirm that `:qa` advances and returns cleanly.
4. Send notes to an agent in the same Herdr workspace. Confirm that agents in
   other workspaces are not offered and that an empty review is not sent.
5. Repeat with spaces, Unicode, a leading dash, symlinks, and deleted/renamed
   files. Unsafe paths must be rejected and terminal control sequences must not
   alter the terminal.

### GitHub actions

Use a disposable repository and PR for this section.

1. Browse another PR with `p`; open it, review it, comment, and cancel an
   approval before confirming that approval works.
2. Select a dispatch-enabled workflow and branch. Verify both cancellation and
   explicit confirmation, then watch the resulting run to completion.
3. Cancel update-branch, then confirm it and verify the refreshed PR state.
4. Cancel a normal merge. Complete a normal merge on a disposable PR and verify
   the final GitHub state, including from a linked worktree.
5. Confirm that admin merge requires the exact text `ADMIN`. Do not complete the
   bypass unless the disposable repository is specifically configured for it.
6. While a confirmation is open, close or replace the PR externally. The final
   state check must cancel the stale operation.

### Sidebar

1. Configure all documented `ci_*` tokens and start the plugin normally.
2. Verify open, running, pass, fail, and merged glyphs on separate workspaces.
   Include running, passing, and failing CI on a branch without a PR.
3. Confirm that the running glyph animates, old state tokens are cleared, and
   token TTL expiry removes stale state after the daemon stops.

## Installer checks

Run these checks in disposable copies so the installer cannot replace a working
development binary.

- A missing allowlist entry must build from source and clearly say why.
- A matching entry must download the correct host asset and print
  `installed allowlisted prebuilt`.
- A missing download, digest mismatch, fork remote, dirty checkout, or local Git
  URL rewrite must fail closed to a source build.
- A symlink or non-regular destination must be rejected rather than overwritten.
- A source fallback without Go must give an actionable error.
- Test both Unix installers and native PowerShell, including amd64 and arm64.

## Two-stage release verification

Release publication and installer activation are deliberately separate. A newly
published asset is not trusted merely because its adjacent `.sha256` file exists.

### Stage 1: publish immutable assets

1. Confirm that `herdr-plugin.toml` contains the intended version, the release
   notes are ready, the worktree is clean, and every CI job is green.
2. Create and push the matching `v<version>` tag. The release workflow must
   publish exactly six binaries and six `.sha256` sidecars without replacing an
   existing release.
3. Confirm the workflow's build-provenance attestation and that each binary's
   embedded VCS revision points to the tagged commit.

### Stage 2: review and allowlist

1. Download the release into a new temporary directory with
   `gh release download v<version> --repo zhy0216/herdr-gh-checks`.
2. Verify the build attestation with `gh attestation verify` and independently
   calculate every binary SHA-256. Compare it with both GitHub's asset digest and
   the sidecar. Never copy a sidecar into the allowlist without this comparison.
3. Add one reviewed line per binary to `release-checksums.txt` using this format:

   ```text
   v<version>/herdr-gh-checks-<os>-<arch>[.exe] <64-lowercase-hex-digest>
   ```

4. Commit and push the allowlist as a separate reviewed change. Do not move the
   release tag or replace its assets.
5. On clean machines for each supported OS, run
   `herdr plugin install zhy0216/herdr-gh-checks`. Confirm that installation
   selects the allowlisted prebuilt binary, works without Go, launches the pane,
   and reports the expected behavior from the manual matrix.

Until Stage 2 is complete, source fallback is expected. Local pre-tag checksums
are useful for comparison but are not authoritative release digests.

## Test record

For each candidate, preserve a short record containing:

```text
Version / commit:
Date / tester:
OS / architecture:
Herdr / Go / gh / Neovim versions:
Automated CI run:
Manual sections completed:
Release asset and attestation evidence:
Known gaps or follow-up issues:
```
