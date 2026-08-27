package main

// The side-effecting command layer: every tea.Cmd that shells out to gh/git/nvim/herdr.
// Kept separate from the TUI (model/Update/View in watch.go) so the render logic stays pure
// over state and the process-spawning surface lives in one place.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func fetchCmd(cwd string, num int) tea.Cmd {
	return func() tea.Msg { return fetchMsg(prStatusNum(cwd, num)) }
}
func workflowsCmd(cwd string) tea.Cmd {
	return func() tea.Msg { return workflowsMsg(listWorkflows(cwd)) }
}
func refetchCmd(cwd string, num int) tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return fetchMsg(prStatusNum(cwd, num)) })
}

func runsCmd(cwd string) tea.Cmd   { return func() tea.Msg { return runsMsg(listRuns(cwd)) } }
func prListCmd(cwd string) tea.Cmd { return func() tea.Msg { return prListMsg(listPRs(cwd)) } }

// approvePR approves without a body (async).
func (m model) approvePR(number int) tea.Cmd {
	cwd := m.cwd
	return func() tea.Msg {
		if !validPRNumber(number) {
			return sentMsg("invalid pull request number")
		}
		if st := prStatusNum(cwd, number); st.PR == nil || st.PR.State != "OPEN" || st.PR.Number != number {
			return sentMsg(fmt.Sprintf("PR #%d is no longer open; approve cancelled", number))
		}
		// #nosec G204 -- the PR number is an integer and no shell is invoked.
		c, ok := ghCommand(cwd, "pr", "review", strconv.Itoa(number), "--approve")
		if !ok {
			return sentMsg("approve cancelled: repository remote is not GitHub")
		}
		c.Dir = cwd
		if out, err := limitedCombinedOutput(c); err != nil {
			return sentMsg(fmt.Sprintf("PR #%d approve failed: %s", number, sanitizeTerminalText(strings.TrimSpace(string(out)))))
		}
		return sentMsg(fmt.Sprintf("approved PR #%d", number))
	}
}

// prRefShell emits a shell snippet setting $base and $head for a PR review.
// num==0 = current branch (head=HEAD); num>0 fetches pull/$NUM/head into a
// per-process temporary ref supplied in REVIEW_REF.
func prRefShell(num int) string {
	gitSafe := `git_safe() { git -c protocol.allow=never -c protocol.http.allow=always -c protocol.https.allow=always -c protocol.ssh.allow=always -c protocol.ext.allow=never -c protocol.file.allow=never -c protocol.git.allow=never -c core.hooksPath=/dev/null -c credential.helper= -c core.fsmonitor=false -c core.gitProxy= -c core.sshCommand=ssh -c ssh.variant=ssh -c http.proxy= -c http.https://github.com/.proxy= -c http.extraHeader= -c http.https://github.com/.extraHeader= -c http.sslVerify=true -c core.pager=cat -c core.alternateRefsCommand= -c remote.origin.uploadpack=git-upload-pack "$@"; }; reject_git_rewrites() { if git_safe config --local --includes --get-regexp '^url\..*\.(insteadof|pushinsteadof)$' >/dev/null 2>&1; then echo "unsupported Git URL rewrite" >&2; exit 1; else rc=$?; [ "$rc" -eq 1 ] || { echo "unable to inspect Git URL rewrites" >&2; exit 1; }; fi; }; validate_origin() { origin=${origin%.git}; origin=${origin%/}; case "$origin" in https://github.com/*) repo_name=${origin#https://github.com/} ;; ssh://git@github.com/*) repo_name=${origin#ssh://git@github.com/} ;; git@github.com:*) repo_name=${origin#git@github.com:} ;; *) echo "unsupported origin" >&2; exit 1 ;; esac; printf '%s' "$repo_name" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || { echo "invalid GitHub repository" >&2; exit 1; }; }`
	common := gitSafe + `; origin=$(git_safe config --local --get remote.origin.url 2>/dev/null) || exit 1; reject_git_rewrites; validate_origin; `
	if num == 0 {
		return common + `base=$(gh pr view --repo "$repo_name" --json baseRefName -q .baseRefName 2>/dev/null); [ -z "$base" ] && base=main; git_safe check-ref-format --branch "$base" >/dev/null 2>&1 || { echo "invalid base ref" >&2; exit 1; }; case "$base" in -*|+*|/*|*..*|*@\{*) echo "invalid base ref" >&2; exit 1;; esac; git_safe fetch -q --no-tags --upload-pack=git-upload-pack "$origin" -- "$base:refs/remotes/origin/$base" 2>/dev/null || exit 1; head=HEAD`
	}
	return common + `base=$(gh pr view "$NUM" --repo "$repo_name" --json baseRefName -q .baseRefName 2>/dev/null); [ -z "$base" ] && base=main; cleanup_review_ref() { if [ -n "${REVIEW_REF:-}" ]; then git_safe update-ref -d "$REVIEW_REF" 2>/dev/null || :; fi; }; git_safe check-ref-format --branch "$base" >/dev/null 2>&1 || { echo "invalid base ref" >&2; exit 1; }; case "$base" in -*|+*|/*|*..*|*@\{*) echo "invalid base ref" >&2; exit 1;; esac; printf '%s' "${REVIEW_REF:-}" | grep -Eq '^refs/ci/herdr-gh-checks/[0-9a-f]{32}$' || { echo "invalid review ref" >&2; exit 1; }; trap cleanup_review_ref EXIT; git_safe fetch -q --no-tags --upload-pack=git-upload-pack "$origin" -- "$base:refs/remotes/origin/$base" 2>/dev/null || exit 1; git_safe fetch -q --no-tags --upload-pack=git-upload-pack "$origin" -- "pull/$NUM/head:$REVIEW_REF" 2>/dev/null || exit 1; head=$REVIEW_REF`
}

func prRefCleanup(num int) string {
	if num == 0 {
		return ""
	}
	return `; cleanup_review_ref`
}

// diffAll opens the whole PR side-by-side (base...head) in nvimdiff. num==0 = current branch.
func (m model) diffAll(num int) tea.Cmd {
	if num < 0 || (num > 0 && !validPRNumber(num)) {
		return func() tea.Msg { return sentMsg("invalid pull request number") }
	}
	// --extcmd bypasses Git's built-in nvimdiff helper, whose historical command
	// line did not put `--` before filenames. Git supplies LOCAL/REMOTE as quoted
	// arguments to this fixed command, so a PR filename cannot become an nvim option.
	env := secureShellEnvForDir(m.cwd)
	if num > 0 {
		ref, err := newReviewRef()
		if err != nil {
			return func() tea.Msg { return sentMsg("could not allocate temporary review ref") }
		}
		env = append(env, "REVIEW_REF="+ref)
	}
	script := prRefShell(num) + `; git_safe difftool -y --no-ext-diff --no-textconv --extcmd='nvim -u NONE --noplugin -d --' -- "origin/$base...$head"` + prRefCleanup(num)
	// #nosec G204 -- script is a fixed template; the only dynamic ref is
	// validated by git and all path values are passed through quoted env vars.
	c := commandWithEnv("sh", env, "-c", script)
	c.Dir = m.cwd
	c.Env = append(env, "NUM="+strconv.Itoa(num))
	return tea.ExecProcess(c, func(error) tea.Msg { return reloadMsg{} })
}

// annotateFile reviews ONE file side-by-side with `ga` line annotation. num==0 diffs base vs the
// REAL working file (checked out) so review.vim reads the real path; num>0 diffs base vs a temp of
// the PR head and passes the path via CI_REVIEW_PATH.
func (m model) annotateFile(path string, num int) tea.Cmd {
	if num < 0 || (num > 0 && !validPRNumber(num)) || (num == 0 && func() bool { _, err := validateRepoPath(m.cwd, path, true); return err != nil }()) || (num > 0 && func() bool { _, err := validateRepoPath(m.cwd, path, false); return err != nil }()) {
		return func() tea.Msg { return sentMsg("refused unsafe or unavailable file path") }
	}
	notes := notesFileFor(m.status.Branch)
	if err := seedNotes(notes, m.status); err != nil {
		return func() tea.Msg { return sentMsg("notes unavailable: " + sanitizeTerminalText(err.Error())) }
	}
	vimrc := reviewVimPath()
	if vimrc == "" {
		return func() tea.Msg { return sentMsg("review script unavailable") }
	}
	env := append(secureShellEnvForDir(m.cwd), "CI_NOTES="+notes, "NUM="+strconv.Itoa(num), "FILE="+path, "VIMRC="+vimrc)
	if num > 0 {
		ref, err := newReviewRef()
		if err != nil {
			return func() tea.Msg { return sentMsg("could not allocate temporary review ref") }
		}
		env = append(env, "REVIEW_REF="+ref)
	}
	var body string
	if num == 0 {
		body = `a=$(mktemp); trap 'rm -f "$a"' EXIT; git_safe show "origin/$base:$FILE" > "$a" 2>/dev/null || exit 1; nvim -u NONE --noplugin -d -c 'set nomodeline noexrc' -c 'wincmd l' -S "$VIMRC" -- "$a" "$FILE"`
	} else {
		env = append(env, "CI_REVIEW_PATH="+path)
		body = `a=$(mktemp); b=$(mktemp); trap 'rm -f "$a" "$b"; cleanup_review_ref' EXIT; git_safe show "origin/$base:$FILE" > "$a" 2>/dev/null || exit 1; git_safe show "$head:$FILE" > "$b" 2>/dev/null || exit 1; nvim -u NONE --noplugin -d -c 'set nomodeline noexrc' -c 'wincmd l' -S "$VIMRC" -- "$a" "$b"`
	}
	script := prRefShell(num) + "; " + body + prRefCleanup(num)
	// #nosec G204 -- path is canonicalized/validated above and every shell
	// expansion is double-quoted; nvim receives `--` before file arguments.
	c := commandWithEnv("sh", env, "-c", script)
	c.Dir = m.cwd
	c.Env = env
	return tea.ExecProcess(c, func(error) tea.Msg { return reloadMsg{} })
}

// ghInteractive hands the terminal to gh's own review/comment flow (prompts + $EDITOR body).
func (m model) ghInteractive(kind string, number int) tea.Cmd {
	if !validPRNumber(number) {
		return func() tea.Msg { return sentMsg("invalid pull request number") }
	}
	sub := "review"
	switch kind {
	case "review":
	case "comment":
		sub = "comment"
	default:
		return func() tea.Msg { return sentMsg("invalid review action") }
	}
	// #nosec G204 -- sub is an internal review/comment allowlist and number is an integer.
	c, ok := ghCommand(m.cwd, "pr", sub, strconv.Itoa(number))
	if !ok {
		return func() tea.Msg { return sentMsg("review cancelled: repository remote is not GitHub") }
	}
	c.Dir = m.cwd
	return tea.ExecProcess(c, func(error) tea.Msg { return reloadMsg{} })
}

// openNotes edits the worktree's review file in nvim.
func (m model) openNotes() tea.Cmd {
	path := notesFileFor(m.status.Branch)
	if err := seedNotes(path, m.status); err != nil {
		return func() tea.Msg { return sentMsg("notes unavailable: " + sanitizeTerminalText(err.Error())) }
	}
	// #nosec G204 -- notes path is generated inside the private state directory
	// and `--` prevents it from being parsed as an nvim option.
	c := commandWithEnv("nvim", secureShellEnvForDir(m.cwd), "-u", "NONE", "--noplugin", "-c", "set nomodeline noexrc", "--", path)
	c.Dir = m.cwd
	return tea.ExecProcess(c, func(error) tea.Msg { return reloadMsg{} })
}

// sendReview posts the annotations (minus # guide lines) to the chosen agent pane.
func (m model) sendReview(target string) tea.Cmd {
	branch, num := m.status.Branch, 0
	if m.status.PR != nil {
		num = m.status.PR.Number
	}
	return func() tea.Msg {
		body := readNotes(notesFileFor(branch))
		if body == "" {
			return sentMsg("no annotations — press a to write some")
		}
		text := fmt.Sprintf("Code review for PR #%d (%s):\n\n%s", num, sanitizeTerminalText(branch), sanitizeTerminalText(body))
		if strings.TrimSpace(target) == "" {
			return sentMsg("send failed: no target pane")
		}
		if !safeIdentifier(target) {
			return sentMsg("send failed: invalid target pane")
		}
		// #nosec G204 -- target is selected from same-workspace agent metadata,
		// and exec.Command does not invoke a shell.
		if err := commandWithEnv(herdrBin(), secureShellEnvForDir(m.cwd), "agent", "prompt", target, text).Run(); err != nil {
			return sentMsg("send failed")
		}
		return sentMsg("sent to " + target)
	}
}

// updateBranch merges the latest base branch into the PR branch (GitHub's "Update branch").
func (m model) updateBranch(expectedNumber int) tea.Cmd {
	cwd := m.cwd
	return func() tea.Msg {
		if !validPRNumber(expectedNumber) {
			return sentMsg("update cancelled: invalid pull request number")
		}
		if st := prStatus(cwd); st.PR == nil || st.PR.State != "OPEN" || st.PR.Number != expectedNumber {
			return sentMsg("update cancelled: no open pull request")
		}
		c, ok := ghCommand(cwd, "pr", "update-branch")
		if !ok {
			return sentMsg("update cancelled: repository remote is not GitHub")
		}
		c.Dir = cwd
		if out, err := limitedCombinedOutput(c); err != nil {
			return sentMsg("update failed: " + sanitizeTerminalText(strings.TrimSpace(string(out))))
		}
		return sentMsg("branch updated with base")
	}
}

// watchRun streams the workflow's latest run (gh run watch) until it completes.
func (m model) watchRun(id int) tea.Cmd {
	if id <= 0 {
		return func() tea.Msg { return sentMsg("invalid workflow id") }
	}
	repo, ok := githubRepoForCwd(m.cwd)
	if !ok {
		return func() tea.Msg { return sentMsg("watch cancelled: repository remote is not GitHub") }
	}
	script := `rid=$(gh run list --workflow="$1" --repo "$2" -L1 --json databaseId -q '.[0].databaseId' 2>/dev/null); if printf '%s' "$rid" | grep -Eq '^[0-9]+$'; then gh run watch "$rid" --repo "$2"; else echo "no valid run found — trigger one first (⏎)"; sleep 1; fi`
	// #nosec G204 -- the script is constant and the only positional value is a
	// decimal workflow ID; the returned run ID is accepted only if numeric.
	env := secureShellEnvForDir(m.cwd)
	c := commandWithEnv("sh", env, "-c", script, "sh", strconv.Itoa(id), repo)
	c.Dir = m.cwd
	c.Env = env
	out := newTerminalSanitizer(os.Stdout)
	errOut := newTerminalSanitizer(os.Stderr)
	c.Stdout, c.Stderr = out, errOut
	return tea.ExecProcess(c, func(error) tea.Msg {
		_ = out.Flush()
		_ = errOut.Flush()
		return reloadMsg{}
	})
}

func reviewVimPath() string {
	return embeddedReviewScriptPath()
}
