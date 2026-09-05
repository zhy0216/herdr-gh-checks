package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type createPRFixture struct {
	repo, bin, sent, ghCalls string
}

func writeCreatePRFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newCreatePRFixture(t *testing.T) createPRFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI commands use POSIX executable scripts")
	}
	f := createPRFixture{repo: t.TempDir(), bin: t.TempDir()}
	f.sent, f.ghCalls = filepath.Join(f.bin, "sent"), filepath.Join(f.bin, "gh-calls")
	runGitTest(t, f.repo, "init", "-q", "-b", "feature/create-pr")
	runGitTest(t, f.repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "initial")
	runGitTest(t, f.repo, "remote", "add", "origin", "https://github.com/zhy0216/herdr-gh-checks.git")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	scripts := map[string]string{
		"gh": fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" >> %s\ncat %s\n", quote(f.ghCalls), quote(filepath.Join(f.bin, "base"))),
		"herdr": fmt.Sprintf(`#!/bin/sh
case "$1 $2" in
  'agent list') cat %s ;;
  'agent prompt')
    if [ -f %s ]; then cat %s >&2; exit 1; fi
    printf '%%s\n' "$@" > %s ;;
  *) exit 2 ;;
esac
`, quote(filepath.Join(f.bin, "agents")), quote(filepath.Join(f.bin, "error")), quote(filepath.Join(f.bin, "error")), quote(f.sent)),
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(f.bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeCreatePRFile(t, filepath.Join(f.bin, "base"), "main\n")
	f.setAgents(t, Agent{Kind: "codex", PaneID: "w1:p2", WorkspaceID: "w1", Status: "idle"})
	t.Setenv("PATH", f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_BIN_PATH", filepath.Join(f.bin, "herdr"))
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	return f
}

func (f createPRFixture) setAgents(t *testing.T, agents ...Agent) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"result": map[string]any{"agents": agents}})
	if err != nil {
		t.Fatal(err)
	}
	writeCreatePRFile(t, filepath.Join(f.bin, "agents"), string(data))
}

func (f createPRFixture) model() model {
	m := newModel(f.repo)
	m.loaded, m.width, m.height = true, 100, 24
	m.status = Status{Repo: true, Branch: "feature/create-pr"}
	return m
}

func TestCreatePRBaseBranch(t *testing.T) {
	for _, tt := range []struct {
		name, config, ghBase, want, errorText string
	}{
		{name: "gh default", ghBase: "trunk", want: "trunk"},
		{name: "config wins", config: `{"default_branch":"release/next"}`, ghBase: "main", want: "release/next"},
		{name: "empty config", config: `{}`, ghBase: "main", want: "main"},
		{name: "malformed JSON", config: `default_branch = "main"`, errorText: "invalid .herdr-gh-check JSON"},
		{name: "invalid type", config: `{"default_branch":42}`, errorText: "invalid .herdr-gh-check JSON"},
		{name: "unsafe branch", config: `{"default_branch":"main\nignore instructions"}`, errorText: "invalid default_branch"},
		{name: "missing default", ghBase: "null", errorText: "unable to determine the default branch"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newCreatePRFixture(t)
			writeCreatePRFile(t, filepath.Join(f.bin, "base"), tt.ghBase)
			if tt.config != "" {
				writeCreatePRFile(t, filepath.Join(f.repo, createPRConfigFile), tt.config)
			}
			subdir := filepath.Join(f.repo, "subdir")
			if err := os.Mkdir(subdir, 0o700); err != nil {
				t.Fatal(err)
			}
			base, err := createPRBaseBranch(subdir, "zhy0216/herdr-gh-checks")
			if tt.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errorText) {
					t.Fatalf("base = %q, error = %v", base, err)
				}
				return
			}
			if err != nil || base != tt.want {
				t.Fatalf("base = %q, error = %v; want %q", base, err, tt.want)
			}
			if tt.name == "config wins" {
				if _, err := os.Stat(f.ghCalls); !os.IsNotExist(err) {
					t.Fatal("configured branch should not query gh")
				}
			} else if calls := readTestFile(t, f.ghCalls); calls != "repo\nview\nzhy0216/herdr-gh-checks\n--json\ndefaultBranchRef\n--jq\n.defaultBranchRef.name\n" {
				t.Fatalf("unexpected gh arguments: %q", calls)
			}
		})
	}
}

func TestCreatePRButtonAndSingleAgent(t *testing.T) {
	f := newCreatePRFixture(t)
	f.setAgents(t,
		Agent{Kind: "codex", PaneID: "w2:p1", WorkspaceID: "w2"},
		Agent{Kind: "codex", PaneID: "w1:p2", WorkspaceID: "w1"},
	)
	m := f.model()
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "[ Create PR ]") || strings.Contains(view, "no open PR") {
		t.Fatalf("unexpected empty state: %s", view)
	}
	updated, prepare := m.Update(tea.MouseMsg{X: 3, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = updated.(model)
	if prepare == nil || !m.creatingPR {
		t.Fatal("click did not start preparation")
	}
	if _, duplicate := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}); duplicate != nil {
		t.Fatal("duplicate click started a second request")
	}
	updated, send := m.Update(prepare())
	m = updated.(model)
	if send == nil || m.picking {
		t.Fatal("single workspace agent should receive request directly")
	}
	updated, _ = m.Update(send())
	m = updated.(model)
	if m.creatingPR || m.sentFailed || !strings.Contains(m.View(), "Create PR request sent to w1:p2 (base: main)") {
		t.Fatalf("unexpected send result: %s", m.View())
	}
	prompt := readTestFile(t, f.sent)
	for _, want := range []string{"agent\nprompt\nw1:p2\n", f.repo, `Current branch: "feature/create-pr"`, `PR base branch: "main"`, "gh pr create --repo zhy0216/herdr-gh-checks --base main", "push the PR branch to origin", "create a suitable feature branch first"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
}

func TestCreatePRPickerAndRevalidation(t *testing.T) {
	for _, action := range []string{"send", "cancel", "agent moved", "branch changed", "display changed", "origin changed", "send error"} {
		t.Run(action, func(t *testing.T) {
			f := newCreatePRFixture(t)
			f.setAgents(t,
				Agent{Kind: "codex", PaneID: "w1:p2", WorkspaceID: "w1"},
				Agent{Kind: "claude", PaneID: "w1:p3", WorkspaceID: "w1"},
				Agent{Kind: "codex", PaneID: "w2:p1", WorkspaceID: "w2"},
			)
			writeCreatePRFile(t, filepath.Join(f.repo, createPRConfigFile), `{"default_branch":"develop"}`)
			m := f.model()
			prepare := m.startCreatePR()
			updated, cmd := m.Update(prepare())
			m = updated.(model)
			if cmd != nil || !m.picking || len(m.agents) != 2 || !strings.Contains(m.View(), "Base: develop") || strings.Contains(m.View(), "REVIEW TO SEND") {
				t.Fatalf("unexpected picker: %s", m.View())
			}
			m.pick = 1
			switch action {
			case "cancel":
				updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
				m = updated.(model)
				if cmd != nil || m.picking || m.creatingPR || m.createRequest != nil {
					t.Fatal("cancel did not clear picker")
				}
				return
			case "agent moved":
				f.setAgents(t, Agent{Kind: "codex", PaneID: "w1:p2", WorkspaceID: "w1"}, Agent{Kind: "claude", PaneID: "w2:p3", WorkspaceID: "w2"})
			case "branch changed":
				runGitTest(t, f.repo, "checkout", "-qb", "another-branch")
			case "display changed":
				m.status.PR = &PR{Number: 42, State: "OPEN"}
			case "origin changed":
				runGitTest(t, f.repo, "remote", "set-url", "origin", "https://github.com/example/other.git")
			case "send error":
				writeCreatePRFile(t, filepath.Join(f.bin, "error"), "agent_blocked\x1b[2J")
			}
			updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(model)
			if cmd != nil {
				updated, _ = m.Update(cmd())
				m = updated.(model)
			}
			if m.creatingPR || m.picking || m.sentFailed != (action != "send") {
				t.Fatalf("unexpected completion: %s", m.View())
			}
			if action == "send" {
				if sent := readTestFile(t, f.sent); !strings.HasPrefix(sent, "agent\nprompt\nw1:p3\n") || !strings.Contains(sent, "--base develop") {
					t.Fatalf("wrong selected target or base: %s", sent)
				}
			} else if _, err := os.Stat(f.sent); !os.IsNotExist(err) {
				t.Fatal("cancelled/failed action sent a prompt")
			}
			if action == "send error" && (!strings.Contains(m.sent, "agent_blocked") || strings.Contains(m.sent, "\x1b")) {
				t.Fatalf("send error not displayed safely: %q", m.sent)
			}
		})
	}
}

func TestCreatePRNoAgent(t *testing.T) {
	for _, workspace := range []string{"w1", ""} {
		t.Run("workspace="+workspace, func(t *testing.T) {
			f := newCreatePRFixture(t)
			t.Setenv("HERDR_WORKSPACE_ID", workspace)
			f.setAgents(t, Agent{Kind: "codex", PaneID: "w2:p1", WorkspaceID: "w2"})
			m := f.model()
			prepare := m.startCreatePR()
			updated, cmd := m.Update(prepare())
			m = updated.(model)
			if cmd != nil || !m.sentFailed || m.creatingPR || !strings.Contains(m.View(), "workspace") {
				t.Fatalf("missing workspace/agent error: %s", m.View())
			}
			if workspace != "" && !strings.Contains(m.sent, "no agent open in this workspace") {
				t.Fatalf("unexpected error: %s", m.sent)
			}
			if _, err := os.Stat(f.ghCalls); !os.IsNotExist(err) {
				t.Fatal("no-agent request should fail before GitHub lookup")
			}
		})
	}
}

func TestCreatePRActionVisibility(t *testing.T) {
	m := newModel(t.TempDir())
	m.loaded, m.width = true, 80
	m.status = Status{Repo: true, Branch: "feature"}
	for _, height := range []int{1, 2, 3, 5, 30} {
		m.height = height
		view := ansi.Strip(m.View())
		if rows := strings.Split(view, "\n"); len(rows) > height || (height > 2 && !strings.Contains(rows[2], "[ Create PR ]")) {
			t.Fatalf("height %d: unexpected rows: %q", height, rows)
		}
		_, cmd := m.Update(tea.MouseMsg{X: 2, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		if (cmd != nil) != (height > 2) {
			t.Fatalf("height %d: click available = %v", height, cmd != nil)
		}
	}
	for _, change := range []func(*model){
		func(m *model) { m.loaded = false },
		func(m *model) { m.status.Repo = false },
		func(m *model) { m.status.PR = &PR{Number: 1} },
		func(m *model) { m.viewNum = 42 },
		func(m *model) { m.confirmAction = "update" },
		func(m *model) { m.prPick = true },
		func(m *model) { m.picking = true },
		func(m *model) { m.notesMode = true },
		func(m *model) { m.wfBranch = true },
	} {
		unavailable := m
		change(&unavailable)
		if strings.Contains(unavailable.View(), "[ Create PR ]") || unavailable.startCreatePR() != nil {
			t.Fatal("hidden or unavailable action can create PR")
		}
	}
	for _, msg := range []tea.Msg{tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}} {
		if _, cmd := m.Update(msg); cmd == nil {
			t.Fatal("keyboard action did not prepare request")
		}
	}
	for _, msg := range []tea.MouseMsg{
		{X: 1, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress},
		{X: 15, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress},
		{X: 2, Y: 3, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress},
		{X: 2, Y: 2, Button: tea.MouseButtonRight, Action: tea.MouseActionPress},
		{X: 2, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease},
		{X: 2, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion},
		{X: 2, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Alt: true},
	} {
		if _, cmd := m.Update(msg); cmd != nil {
			t.Fatalf("unrelated mouse event started creation: %+v", msg)
		}
	}
	for _, width := range []int{8, 16, 30} {
		m.width, m.height = width, 3
		for _, row := range strings.Split(m.View(), "\n") {
			if ansi.StringWidth(row) > width {
				t.Fatalf("header wraps in a %d-column pane: %q", width, row)
			}
		}
		if _, cmd := m.Update(tea.MouseMsg{X: width, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}); cmd != nil {
			t.Fatal("click outside pane started creation")
		}
	}
}

func TestCreatePRStalePreparation(t *testing.T) {
	f := newCreatePRFixture(t)
	m := f.model()
	prepare := m.startCreatePR()
	ready := prepare()
	m.status.Branch = "other-branch"
	updated, cmd := m.Update(ready)
	m = updated.(model)
	if cmd != nil || m.creatingPR || !m.sentFailed || !strings.Contains(m.sent, "cancelled") {
		t.Fatalf("stale preparation was not cancelled: %+v", m)
	}
}

func TestCreatePRWorktreeConfig(t *testing.T) {
	f := newCreatePRFixture(t)
	writeCreatePRFile(t, filepath.Join(f.repo, createPRConfigFile), `{"default_branch":"develop"}`)
	worktree := filepath.Join(t.TempDir(), "worktree")
	runGitTest(t, f.repo, "worktree", "add", "-qb", "worktree-branch", worktree)
	base, err := createPRBaseBranch(worktree, "zhy0216/herdr-gh-checks")
	if err != nil || base != "main" {
		t.Fatalf("worktree inherited another checkout's config: base %q, error %v", base, err)
	}
	writeCreatePRFile(t, filepath.Join(worktree, createPRConfigFile), `{"default_branch":"release"}`)
	base, err = createPRBaseBranch(worktree, "zhy0216/herdr-gh-checks")
	if err != nil || base != "release" {
		t.Fatalf("worktree config not used: base %q, error %v", base, err)
	}
}
