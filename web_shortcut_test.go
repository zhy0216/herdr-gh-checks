package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestWebShortcutViewAndClick(t *testing.T) {
	for _, tt := range []struct {
		name   string
		branch string
		pr     *PR
		view   int
		label  string
	}{
		{"PR", "feature", &PR{Number: 1112, State: "OPEN"}, 0, "#1112 ↗"},
		{"other PR", "review", &PR{Number: 42, State: "OPEN"}, 42, "#42 ↗"},
		{"branch", "feature/测试", nil, 0, "feature/测试 ↗"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(t.TempDir())
			m.loaded = true
			m.status = Status{Repo: true, Branch: tt.branch, PR: tt.pr}
			m.viewNum = tt.view
			for _, height := range []int{60, 5, 1} {
				updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: height})
				m = updated.(model)
				view := ansi.Strip(m.View())
				first, _, _ := strings.Cut(view, "\n")
				if !strings.HasPrefix(first, "  "+tt.label+"  click") {
					t.Fatalf("height %d: first row = %q", height, first)
				}
				if rows := strings.Count(view, "\n") + 1; rows > height {
					t.Fatalf("rendered %d rows in a %d-row pane", rows, height)
				}
				for _, x := range []int{2, 1 + ansi.StringWidth(tt.label)} {
					_, cmd := m.Update(tea.MouseMsg{X: x, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
					if cmd == nil {
						t.Fatalf("height %d: click at %d did not open shortcut", height, x)
					}
				}
			}
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
			if cmd == nil {
				t.Fatal("o did not open the displayed target")
			}
			for _, msg := range []tea.MouseMsg{
				{X: 1, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress},
				{X: 2 + ansi.StringWidth(tt.label), Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress},
				{X: 2, Y: 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress},
				{X: 2, Y: 0, Button: tea.MouseButtonRight, Action: tea.MouseActionPress},
				{X: 2, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease},
				{X: 2, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion},
				{X: 2, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Shift: true},
			} {
				if _, cmd := m.Update(msg); cmd != nil {
					t.Fatalf("unrelated mouse event opened shortcut: %+v", msg)
				}
			}
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 8, Height: 5})
			m = updated.(model)
			if width := ansi.StringWidth(m.webShortcutLabel()); width > 6 {
				t.Fatalf("shortcut exceeds available width: %d", width)
			}
			if _, cmd := m.Update(tea.MouseMsg{X: 8, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}); cmd != nil {
				t.Fatal("click beyond clipped shortcut opened browser")
			}
		})
	}
}

func TestWebShortcutUnavailable(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*model)
	}{
		{"loading", func(m *model) { m.loaded = false }},
		{"not a repo", func(m *model) { m.status.Repo = false }},
		{"confirmation", func(m *model) { m.confirmAction = "update" }},
		{"PR picker", func(m *model) { m.prPick = true }},
		{"branch picker", func(m *model) { m.wfBranch = true }},
		{"notes", func(m *model) { m.notesMode = true }},
		{"agent picker", func(m *model) { m.picking = true }},
		{"invalid PR", func(m *model) { m.status.PR = &PR{Number: -1} }},
		{"missing viewed PR", func(m *model) { m.viewNum = 42 }},
		{"stale viewed PR", func(m *model) { m.viewNum = 42; m.status.PR = &PR{Number: 1} }},
		{"invalid branch", func(m *model) { m.status.Branch = "bad\x1b[2J" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(t.TempDir())
			m.loaded, m.width = true, 80
			m.status = Status{Repo: true, Branch: "main"}
			tt.change(&m)
			if strings.Contains(m.View(), "click · o open") {
				t.Fatal("unavailable shortcut was rendered")
			}
			if _, cmd := m.Update(tea.MouseMsg{X: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}); cmd != nil {
				t.Fatal("hidden shortcut opened browser")
			}
		})
	}
}

func TestWebShortcutOpensDisplayedTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh uses a POSIX executable script")
	}
	repo := t.TempDir()
	runGitTest(t, repo, "init", "-q", "-b", "main")
	runGitTest(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "initial")
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/zhy0216/herdr-gh-checks.git")
	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls")
	quotedLog := "'" + strings.ReplaceAll(logPath, "'", "'\"'\"'") + "'"
	installGH := func(exitCode int) {
		t.Helper()
		script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\nexit %d\n", quotedLog, exitCode)
		if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	installGH(0)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tt := range []struct {
		name   string
		branch string
		pr     *PR
		view   int
		want   []string
	}{
		{"PR", "main", &PR{Number: 1112, State: "OPEN", URL: "https://example.invalid/untrusted"}, 0, []string{"pr", "view", "1112", "--web"}},
		{"other PR", "feature", &PR{Number: 42, State: "CLOSED"}, 42, []string{"pr", "view", "42", "--web"}},
		{"default branch", "main", nil, 0, []string{"browse", "--branch", "main"}},
		{"feature branch", "feature/测试", nil, 0, []string{"browse", "--branch", "feature/测试"}},
		{"detached HEAD", "HEAD", nil, 0, []string{"browse", "--branch", gitOutputTest(t, repo, "rev-parse", "HEAD")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(repo)
			m.loaded, m.width, m.height = true, 80, 24
			m.status = Status{Repo: true, Branch: tt.branch, PR: tt.pr}
			m.viewNum = tt.view
			for _, msg := range []tea.Msg{
				tea.MouseMsg{X: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress},
				tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}},
			} {
				_, cmd := m.Update(msg)
				if cmd == nil {
					t.Fatal("missing open command")
				}
				if result := cmd(); result != nil {
					t.Fatalf("open failed: %v", result)
				}
				got := strings.Split(strings.TrimSpace(readTestFile(t, logPath)), "\n")
				want := append(slices.Clone(tt.want), "--repo", "zhy0216/herdr-gh-checks")
				if !slices.Equal(got, want) {
					t.Fatalf("gh args = %q, want %q", got, want)
				}
			}
		})
	}
	installGH(1)
	msg := openWebCmd(repo, 0, "main")()
	if _, ok := msg.(webOpenFailedMsg); !ok {
		t.Fatalf("failed command returned %T", msg)
	}
	m := newModel(repo)
	m.loaded, m.updating = true, true
	m.status = Status{Repo: true, Branch: "main"}
	updated, _ := m.Update(msg)
	m = updated.(model)
	if !m.updating || !m.sentFailed || !strings.Contains(m.View(), "open failed") {
		t.Fatal("open error must be visible without cancelling an in-flight branch update")
	}
}
