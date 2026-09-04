package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Check struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Bucket string `json:"bucket"`
}
type File struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}
type Label struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}
type Assignee struct {
	Login string `json:"login"`
}
type PR struct {
	Number         int        `json:"number"`
	State          string     `json:"state"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	URL            string     `json:"url"`
	Assignees      []Assignee `json:"assignees"`
	Labels         []Label    `json:"labels"`
	Files          []File     `json:"files"`
	Additions      int        `json:"additions"`
	Deletions      int        `json:"deletions"`
	ChangedFiles   int        `json:"changedFiles"`
	ReviewDecision string     `json:"reviewDecision"`
	HeadRefName    string     `json:"headRefName"`
	Checks         []Check    `json:"-"`
}
type Status struct {
	Repo   bool
	Branch string
	PR     *PR
}

// run executes name in cwd, returning trimmed stdout and ok=false on any error.
func run(cwd, name string, args ...string) (string, bool) {
	return runWithEnv(cwd, name, nil, args...)
}

func runWithEnv(cwd, name string, processEnv []string, args ...string) (string, bool) {
	if processEnv == nil {
		processEnv = secureShellEnvForDir(cwd)
	}
	cmd := commandWithEnv(name, processEnv, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := limitedCommandOutput(cmd)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func githubRepoForCwd(cwd string) (string, bool) {
	rewritten, ok := localGitURLRewrites(cwd)
	if !ok || rewritten {
		return "", false
	}
	raw, ok := runGit(cwd, "config", "--local", "--get", "remote.origin.url")
	if !ok || !validGitHubRemote(raw) {
		return "", false
	}
	return githubRemoteRepo(raw), true
}

func localGitURLRewrites(cwd string) (found, ok bool) {
	cmd := commandWithEnv("git", secureShellEnvForDir(cwd), "config", "--local", "--includes", "--get-regexp", `^url\..*\.(insteadof|pushinsteadof)$`)
	cmd.Dir = cwd
	out, err := limitedCommandOutput(cmd)
	if err == nil {
		return len(strings.TrimSpace(string(out))) > 0, true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(out) == 0 {
		// git config uses status 1 for a normal no-match query.
		return false, true
	}
	return false, false
}

func validGitHubRemote(raw string) bool {
	if raw == "" || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\r\n\x00") {
		return false
	}
	_, ok := strictGitHubRemoteRepo(raw)
	return ok
}

func validGitHubRepoPath(repoPath string) bool {
	repoPath = strings.TrimSuffix(strings.TrimSuffix(repoPath, "/"), ".git")
	parts := strings.Split(repoPath, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("-_.", r) {
				continue
			}
			return false
		}
	}
	return true
}

func githubRemoteRepo(raw string) string {
	repo, _ := strictGitHubRemoteRepo(raw)
	return repo
}

// strictGitHubRemoteRepo intentionally avoids a general URL parser. The
// plugin only supports three exact GitHub remote spellings; accepting a broad
// URL grammar would expand the attack surface (and historically exposed
// standard-library URL parser bugs to untrusted repository configuration).
func strictGitHubRemoteRepo(raw string) (string, bool) {
	if raw == "" || !utf8.ValidString(raw) || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\x00") {
		return "", false
	}
	var repo string
	switch {
	case strings.HasPrefix(raw, "https://github.com/"):
		repo = strings.TrimPrefix(raw, "https://github.com/")
	case strings.HasPrefix(raw, "ssh://git@github.com/"):
		repo = strings.TrimPrefix(raw, "ssh://git@github.com/")
	case strings.HasPrefix(raw, "git@github.com:"):
		repo = strings.TrimPrefix(raw, "git@github.com:")
	default:
		return "", false
	}
	if strings.ContainsAny(repo, "?#\\:") || !validGitHubRepoPath(repo) {
		return "", false
	}
	repo = strings.TrimSuffix(strings.TrimSuffix(repo, "/"), ".git")
	return repo, true
}

func runGH(cwd string, args ...string) (string, bool) {
	repo, ok := githubRepoForCwd(cwd)
	if !ok {
		return "", false
	}
	args = append(args, "--repo", repo)
	return runWithEnv(cwd, "gh", secureShellEnvForDir(cwd), args...)
}

func ghCommand(cwd string, args ...string) (*exec.Cmd, bool) {
	repo, ok := githubRepoForCwd(cwd)
	if !ok {
		return nil, false
	}
	args = append(args, "--repo", repo)
	return commandWithEnv("gh", secureShellEnvForDir(cwd), args...), true
}

// runGit pins the Git settings that can turn an otherwise read-only helper
// into a hook or external-protocol execution point in a hostile checkout.
func runGit(cwd string, args ...string) (string, bool) {
	gitArgs := []string{
		"-c", "protocol.allow=never", "-c", "protocol.http.allow=always", "-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always",
		"-c", "protocol.ext.allow=never", "-c", "protocol.file.allow=never", "-c", "protocol.git.allow=never",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "credential.helper=", "-c", "core.fsmonitor=false", "-c", "core.gitProxy=", "-c", "core.sshCommand=ssh", "-c", "ssh.variant=ssh",
		"-c", "http.proxy=", "-c", "http.https://github.com/.proxy=", "-c", "http.extraHeader=", "-c", "http.https://github.com/.extraHeader=", "-c", "http.sslVerify=true",
		"-c", "core.pager=cat", "-c", "remote.origin.uploadpack=git-upload-pack",
	}
	gitArgs = append(gitArgs, args...)
	return runWithEnv(cwd, "git", secureShellEnvForDir(cwd), gitArgs...)
}

func prStatus(cwd string) Status { return prStatusNum(cwd, 0) }

// prStatusNum fetches the current branch's PR (num=0) or a specific PR by number.
func prStatusNum(cwd string, num int) Status {
	if num < 0 {
		return Status{Repo: false}
	}
	branch, ok := runGit(cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if !ok {
		return Status{Repo: false}
	}
	viewArgs := []string{"pr", "view"}
	checkArgs := []string{"pr", "checks"}
	if num > 0 {
		viewArgs = append(viewArgs, strconv.Itoa(num))
		checkArgs = append(checkArgs, strconv.Itoa(num))
	}
	viewArgs = append(viewArgs, "--json",
		"number,state,title,body,url,assignees,labels,files,additions,deletions,changedFiles,reviewDecision,headRefName")
	viewRaw, ok := runGH(cwd, viewArgs...)
	if !ok {
		return Status{Repo: true, Branch: branch}
	}
	var pr PR
	if json.Unmarshal([]byte(viewRaw), &pr) != nil {
		return Status{Repo: true, Branch: branch}
	}
	if !validPRNumber(pr.Number) || (num > 0 && pr.Number != num) {
		return Status{Repo: true, Branch: branch}
	}
	if pr.State == "OPEN" {
		checkArgs = append(checkArgs, "--json", "name,state,bucket")
		if raw, ok := runGH(cwd, checkArgs...); ok {
			if err := json.Unmarshal([]byte(raw), &pr.Checks); err != nil {
				pr.Checks = nil
			}
		}
	}
	if num > 0 && pr.HeadRefName != "" {
		branch = pr.HeadRefName
	}
	return Status{Repo: true, Branch: branch, PR: &pr}
}

type Summary struct {
	Pass, Fail, Pending int
	Overall             string
}

func ciSummary(checks []Check) Summary {
	s := Summary{}
	for _, c := range checks {
		switch c.Bucket {
		case "pass", "skipping":
			s.Pass++
		case "fail", "cancel":
			s.Fail++
		case "pending":
			s.Pending++
		default:
			// GitHub may add new buckets. Never turn an unknown check state
			// into a green result; keep watching until it is understood.
			s.Pending++
		}
	}
	switch {
	case len(checks) == 0:
		s.Overall = "none"
	case s.Fail > 0:
		s.Overall = "fail"
	case s.Pending > 0:
		s.Overall = "pending"
	default:
		s.Overall = "pass"
	}
	return s
}

// sidebar state: pass | fail | run | merged | open | "" (no PR)
func stateOf(st Status) string {
	if !st.Repo || st.PR == nil {
		return ""
	}
	switch st.PR.State {
	case "MERGED":
		return "merged"
	case "CLOSED":
		return "fail"
	}
	switch ciSummary(st.PR.Checks).Overall {
	case "fail":
		return "fail"
	case "pending":
		return "run"
	case "pass":
		return "pass"
	default:
		return "open"
	}
}

func settled(s Status) bool {
	if !s.Repo {
		return true
	}
	if s.PR == nil {
		return false
	}
	return s.PR.State == "MERGED" || s.PR.State == "CLOSED"
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---------- review: agents + notes ----------
type Agent struct {
	Kind        string `json:"agent"`
	Name        string `json:"name"`
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	Status      string `json:"agent_status"`
}

func listAgents() []Agent {
	out, ok := run("", herdrBin(), "agent", "list")
	if !ok {
		return nil
	}
	var m struct {
		Result struct {
			Agents []Agent `json:"agents"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(out), &m) != nil {
		return nil
	}
	return m.Result.Agents
}

func stateDir() string {
	if d := os.Getenv("HERDR_PLUGIN_STATE_DIR"); d != "" {
		// Treat the override as a parent, not as the data directory itself. This
		// prevents an accidental value such as /tmp from being chmodded private
		// and keeps notes in a plugin-specific namespace.
		if filepath.IsAbs(d) {
			return filepath.Join(filepath.Clean(d), "herdr-gh-checks")
		}
	}
	if d, err := os.UserCacheDir(); err == nil && d != "" {
		return filepath.Join(d, "herdr-gh-checks")
	}
	return filepath.Join(os.TempDir(), "herdr-gh-checks")
}

// One persistent review file per checkout and PR. Repository and PR identity
// prevent notes from a same-named branch in another checkout being sent by
// mistake, while the private state directory keeps the path itself opaque.
func notesFileFor(cwd string, s Status) string {
	number := 0
	if s.PR != nil {
		number = s.PR.Number
	}
	return filepath.Join(stateDir(), notesNameForReview(cwd, s.Branch, number))
}

func seedNotes(path string, s Status) error {
	if _, err := readPrivateFile(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	var b strings.Builder
	num := 0
	if s.PR != nil {
		num = s.PR.Number
	}
	fmt.Fprintf(&b, "# Review — #%d %s\n", num, sanitizeTerminalText(s.Branch))
	b.WriteString("# Annotate below, e.g.:  app/models/x.rb:42  handle nil here\n# (lines starting with # are not sent)\n#\n# Changed files:\n")
	if s.PR != nil {
		for _, f := range s.PR.Files {
			fmt.Fprintf(&b, "#   %s\n", sanitizeTerminalText(f.Path))
		}
	}
	b.WriteString("\n")
	// O_EXCL prevents an existing link or note file from being overwritten.
	err := createPrivateFile(path, []byte(b.String()))
	if os.IsExist(err) {
		// Another process may have won the O_EXCL race. Accept only a file
		// that passes the same private-file validation as an existing note.
		_, readErr := readPrivateFile(path)
		return readErr
	}
	return err
}

// loadNotes returns the annotation lines (no # guide/blank lines).
func loadNotes(cwd string, s Status) []string {
	var out []string
	for ln := range strings.SplitSeq(readNotes(notesFileFor(cwd, s)), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

func writeNotes(cwd string, s Status, lines []string) error {
	return writePrivateFile(notesFileFor(cwd, s), []byte(strings.Join(lines, "\n")+"\n"))
}

func readNotes(path string) string {
	data, err := readPrivateFile(path)
	if err != nil {
		return ""
	}
	var out []string
	for ln := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// ---------- GitHub Actions workflows ----------
type Workflow struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

func listWorkflows(cwd string) []Workflow {
	out, ok := runGH(cwd, "workflow", "list", "--json", "name,id,state")
	if !ok {
		return nil
	}
	var w []Workflow
	_ = json.Unmarshal([]byte(out), &w)
	return w
}

// ponytail: gh errors if the workflow lacks a workflow_dispatch trigger; we just surface that.
func runWorkflow(cwd string, id int, ref string) error {
	if id <= 0 || !safeGitRef(ref) {
		return fmt.Errorf("refused unsafe workflow ref")
	}
	// #nosec G204 -- id is positive and ref is restricted by safeGitRef.
	c, ok := ghCommand(cwd, "workflow", "run", strconv.Itoa(id), "--ref", ref)
	if !ok {
		return fmt.Errorf("repository remote is not a supported GitHub URL")
	}
	c.Dir = cwd
	return c.Run()
}

// active = most recently committed; capped so long-lived repos don't dump every stale branch.
type Run struct {
	WorkflowID int    `json:"workflowDatabaseId"`
	Status     string `json:"status"`     // queued, in_progress, completed
	Conclusion string `json:"conclusion"` // success, failure, ...
}

// latest run per workflow (gh run list is newest-first)
func listRuns(cwd string) map[int]Run {
	out, ok := runGH(cwd, "run", "list", "-L", "30", "--json", "workflowDatabaseId,status,conclusion")
	if !ok {
		return nil
	}
	var rs []Run
	if json.Unmarshal([]byte(out), &rs) != nil {
		return nil
	}
	m := map[int]Run{}
	for _, r := range rs {
		if _, seen := m[r.WorkflowID]; !seen {
			m[r.WorkflowID] = r
		}
	}
	return m
}

type PRItem struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	HeadRefName    string `json:"headRefName"`
	ReviewDecision string `json:"reviewDecision"`
	IsDraft        bool   `json:"isDraft"`
	Author         struct {
		Login string `json:"login"`
	} `json:"author"`
}

func listPRs(cwd string) []PRItem {
	out, ok := runGH(cwd, "pr", "list", "--limit", "30", "--json", "number,title,headRefName,reviewDecision,isDraft,author")
	if !ok {
		return nil
	}
	var ps []PRItem
	if json.Unmarshal([]byte(out), &ps) != nil {
		return nil
	}
	valid := ps[:0]
	for _, p := range ps {
		if validPRNumber(p.Number) {
			valid = append(valid, p)
		}
	}
	return valid
}

func listBranches(cwd string) []string {
	out, ok := runGit(cwd, "for-each-ref", "--sort=-committerdate", "--format=%(refname:short)", "refs/remotes/origin")
	if !ok {
		return nil
	}
	var b []string
	for ln := range strings.SplitSeq(out, "\n") {
		ln = strings.TrimPrefix(strings.TrimSpace(ln), "origin/")
		if ln == "" || ln == "HEAD" || !safeGitRef(ln) {
			continue
		}
		b = append(b, ln)
		if len(b) >= 30 {
			break
		}
	}
	return b
}

func herdrBin() string { return env("HERDR_BIN_PATH", "herdr") }
func pluginID() string { return env("HERDR_PLUGIN_ID", "herdr-gh-checks") }
func ciCwd() string {
	cwd := ""
	if c := os.Getenv("CI_CWD"); c != "" {
		cwd = c
	} else {
		cwd, _ = os.Getwd()
	}
	if root, ok := runGit(cwd, "rev-parse", "--show-toplevel"); ok {
		return root
	}
	return cwd
}
