package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const createPRConfigFile = ".herdr-gh-check"

type createPRRequest struct {
	cwd, repo, branch, base, workspace string
}

type createPRReadyMsg struct {
	request createPRRequest
	agents  []Agent
}

type createPRResultMsg struct{ sentMsg }

func createPRFailure(err error) createPRResultMsg {
	return createPRResultMsg{failureMsg("create PR failed: " + err.Error())}
}

// Config belongs to the current worktree, including when the panel opens in a subdirectory.
func createPRBaseBranch(cwd, repo string) (string, error) {
	root, ok := runGit(cwd, "rev-parse", "--show-toplevel")
	if !ok {
		return "", errors.New("unable to find the repository root")
	}
	data, err := readRepoFile(root, createPRConfigFile)
	var config struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err == nil {
		if err := json.Unmarshal(data, &config); err != nil {
			return "", fmt.Errorf("invalid %s JSON: %w", createPRConfigFile, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", createPRConfigFile, err)
	}
	if config.DefaultBranch != "" {
		if !safeGitRef(config.DefaultBranch) || config.DefaultBranch == "HEAD" {
			return "", fmt.Errorf("invalid default_branch in %s", createPRConfigFile)
		}
		return config.DefaultBranch, nil
	}
	// repo view takes a positional repository, unlike gh pr's --repo flag.
	base, ok := run(cwd, "gh", "repo", "view", repo, "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name")
	if !ok || !safeGitRef(base) || base == "null" || base == "HEAD" {
		return "", fmt.Errorf("unable to determine the default branch with gh; set default_branch in %s", createPRConfigFile)
	}
	return base, nil
}

func workspaceAgents(workspace string) ([]Agent, error) {
	if !safeIdentifier(workspace) {
		return nil, errors.New("current Herdr workspace is unavailable")
	}
	out, ok := run("", herdrBin(), "agent", "list")
	if !ok {
		return nil, errors.New("unable to list Herdr agents")
	}
	var response struct {
		Result struct {
			Agents []Agent `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		return nil, fmt.Errorf("unable to read Herdr agents: %w", err)
	}
	var agents []Agent
	for _, a := range response.Result.Agents {
		if a.WorkspaceID == workspace && safeIdentifier(a.PaneID) && a.Kind != "" {
			agents = append(agents, a)
		}
	}
	if len(agents) == 0 {
		return nil, errors.New("no agent open in this workspace; open an agent and try again")
	}
	return agents, nil
}

func (m model) createPRAvailable() bool {
	return m.loaded && m.status.Repo && m.status.PR == nil && m.viewNum == 0 &&
		safeGitRef(m.status.Branch) && m.confirmAction == "" && !m.prPick &&
		!m.wfBranch && !m.notesMode && !m.picking && !m.filtering
}

func (m model) createPRLabel() string {
	if !m.createPRAvailable() {
		return ""
	}
	label := "[ Create PR ]"
	if m.creatingPR {
		label = "[ Sending… ]"
	}
	if m.width > 0 {
		label = ansi.Truncate(label, max(0, m.width-2), "…")
	}
	return label
}

func (m *model) startCreatePR() tea.Cmd {
	if !m.createPRAvailable() || m.creatingPR {
		return nil
	}
	m.creatingPR = true
	m.sent, m.sentFailed = "", false
	cwd, branch, workspace := m.cwd, m.status.Branch, os.Getenv("HERDR_WORKSPACE_ID")
	return func() tea.Msg {
		agents, err := workspaceAgents(workspace)
		if err != nil {
			return createPRFailure(err)
		}
		repo, ok := githubRepoForCwd(cwd)
		if !ok {
			return createPRFailure(errors.New("unable to determine the GitHub repository from origin"))
		}
		base, err := createPRBaseBranch(cwd, repo)
		if err != nil {
			return createPRFailure(err)
		}
		return createPRReadyMsg{createPRRequest{cwd: cwd, repo: repo, branch: branch, base: base, workspace: workspace}, agents}
	}
}

func (r createPRRequest) prompt() string {
	return fmt.Sprintf(`Create a pull request for the changes in this repository.

Repository directory: %q
GitHub repository: %q
Current branch: %q
PR base branch: %q

Work in the repository directory above and follow its AGENTS.md instructions. Inspect the changes, run the relevant checks, and prepare a clear PR title and description. If the current branch is the base branch or HEAD is detached, create a suitable feature branch first. Commit the intended changes if needed, verify that origin points to the GitHub repository above, and push the PR branch to origin. Check for an existing open PR before creating one to avoid duplicates. Use gh pr create --repo %s --base %s with an explicit --head for the PR branch. Return the PR URL when finished. Do not merge the PR.`, r.cwd, r.repo, r.branch, r.base, r.repo, r.base)
}

func sendCreatePR(r createPRRequest, target string) tea.Cmd {
	return func() tea.Msg {
		if os.Getenv("HERDR_WORKSPACE_ID") != r.workspace {
			return createPRFailure(errors.New("current Herdr workspace changed; try again"))
		}
		branch, ok := runGit(r.cwd, "rev-parse", "--abbrev-ref", "HEAD")
		if !ok || branch != r.branch {
			return createPRFailure(errors.New("current branch changed; refresh and try again"))
		}
		repo, ok := githubRepoForCwd(r.cwd)
		if !ok || repo != r.repo {
			return createPRFailure(errors.New("repository origin changed; try again"))
		}
		// Recheck membership after preparation or a picker delay: panes can move or close.
		agents, err := workspaceAgents(r.workspace)
		if err != nil {
			return createPRFailure(err)
		}
		if !slices.ContainsFunc(agents, func(a Agent) bool { return a.PaneID == target }) {
			return createPRFailure(errors.New("selected agent is no longer open in this workspace"))
		}
		cmd := commandWithEnv(herdrBin(), secureShellEnvForDir(r.cwd), "agent", "prompt", target, r.prompt())
		cmd.Dir = r.cwd
		if out, err := limitedCombinedOutput(cmd); err != nil {
			detail := cmp.Or(strings.TrimSpace(sanitizeTerminalText(string(out))), err.Error())
			return createPRFailure(fmt.Errorf("send to agent: %s", detail))
		}
		return createPRResultMsg{successMsg("Create PR request sent to " + target + " (base: " + r.base + ")")}
	}
}
