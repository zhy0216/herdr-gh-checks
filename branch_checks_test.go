package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBranchChecksWithoutPR(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh uses a POSIX executable script")
	}
	repo := t.TempDir()
	runGitTest(t, repo, "init", "-q", "-b", "main")
	runGitTest(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "initial")
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/zhy0216/herdr-gh-checks.git")
	head := gitOutputTest(t, repo, "rev-parse", "HEAD")
	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	installGH := func(view, runs string, exitCode int) {
		t.Helper()
		script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
case "$1 $2" in
  'pr view') printf '%%s\n' %s; [ -n %s ]; exit $? ;;
  'pr checks') printf '%%s\n' '[{"name":"PR-only","bucket":"pass"}]' ;;
  'run list') printf '%%s\n' %s; exit %d ;;
  *) exit 99 ;;
esac
`, quote(logPath), quote(view), quote(view), quote(runs), exitCode)
		if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, logPath, "")
	}

	for _, tt := range []struct {
		name, status, conclusion, want string
	}{
		{"passing", "completed", "success", "pass"},
		{"failed", "completed", "failure", "fail"},
		{"timeout", "completed", "timed_out", "fail"},
		{"cancelled", "completed", "cancelled", "fail"},
		{"skipped", "completed", "skipped", "pass"},
		{"neutral", "completed", "neutral", "pass"},
		{"running", "in_progress", "", "run"},
		{"waiting", "waiting", "", "run"},
		{"unknown", "completed", "future-result", "run"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runs := fmt.Sprintf(`[{"workflowDatabaseId":1,"workflowName":"Build","status":%q,"conclusion":%q},{"workflowDatabaseId":1,"workflowName":"Build","status":"completed","conclusion":"failure"}]`, tt.status, tt.conclusion)
			installGH("", runs, 0)
			st := prStatus(repo)
			if !st.Repo || st.PR != nil || st.Branch != "main" || len(st.Checks) != 1 || stateOf(st) != tt.want {
				t.Fatalf("status = %+v, checks = %+v, state = %q; want %q", st, st.Checks, stateOf(st), tt.want)
			}
			if settled(st) {
				t.Fatal("branch checks must continue polling for new runs")
			}
			calls := readTestFile(t, logPath)
			for _, want := range []string{"--commit " + head, "--branch main", "--repo zhy0216/herdr-gh-checks"} {
				if !strings.Contains(calls, want) {
					t.Fatalf("missing filter %q in calls: %s", want, calls)
				}
			}
		})
	}

	for _, raw := range []string{"[]", "invalid JSON"} {
		installGH("", raw, 0)
		if st := prStatus(repo); len(st.Checks) != 0 || stateOf(st) != "" {
			t.Fatalf("empty/invalid response produced checks: %+v", st)
		}
	}
	installGH("", `[{"workflowDatabaseId":1,"workflowName":"Build","status":"completed","conclusion":"success"},{"workflowDatabaseId":2,"status":"completed","conclusion":"failure"}]`, 0)
	if st := prStatus(repo); len(st.Checks) != 2 || stateOf(st) != "fail" || st.Checks[1].Name != "Workflow 2" {
		t.Fatalf("different workflows must all contribute, even without a name: %+v", st.Checks)
	}
	installGH("", `[{"workflowDatabaseId":1,"status":"completed","conclusion":"success"}]`, 1)
	if st := prStatus(repo); len(st.Checks) != 0 {
		t.Fatalf("failed query produced checks: %+v", st)
	}

	installGH("", "[]", 0)
	if st := prStatusNum(repo, 42); st.PR != nil || len(st.Checks) != 0 {
		t.Fatalf("missing explicit PR status = %+v", st)
	}
	if calls := readTestFile(t, logPath); strings.Contains(calls, "run list") {
		t.Fatalf("explicit PR lookup fell back to branch CI: %s", calls)
	}

	for _, state := range []string{"OPEN", "MERGED", "CLOSED"} {
		installGH(fmt.Sprintf(`{"number":42,"state":%q,"headRefName":"feature"}`, state), "[]", 0)
		st := prStatusNum(repo, 42)
		if st.PR == nil || st.PR.State != state || st.Branch != "feature" {
			t.Fatalf("PR status changed: %+v", st)
		}
		if state == "OPEN" && (len(st.checks()) != 1 || st.checks()[0].Name != "PR-only") {
			t.Fatalf("PR checks lost: %+v", st.checks())
		}
		if calls := readTestFile(t, logPath); strings.Contains(calls, "run list") {
			t.Fatalf("existing PR fell back to branch CI: %s", calls)
		}
	}

	runGitTest(t, repo, "checkout", "--detach", "-q")
	installGH("", "[]", 0)
	prStatus(repo)
	if calls := readTestFile(t, logPath); !strings.Contains(calls, "--commit "+head) || strings.Contains(calls, "--branch") {
		t.Fatalf("detached HEAD should query by commit: %s", calls)
	}
}

func TestBranchCIViewAndRefresh(t *testing.T) {
	for _, tt := range []struct{ bucket, headline string }{
		{"pass", "CI passing"}, {"fail", "CI failing"}, {"pending", "CI running"},
	} {
		t.Run(tt.bucket, func(t *testing.T) {
			m := newModel(t.TempDir())
			m.width = 90
			m.wfLoaded = true
			st := Status{Repo: true, Branch: "main", Checks: []Check{{Name: "Branch build", Bucket: tt.bucket}}}
			updated, cmd := m.Update(fetchMsg{status: st, generation: m.fetchGeneration})
			m = updated.(model)
			if cmd == nil {
				t.Fatal("branch update did not schedule refresh")
			}
			view := m.View()
			for _, want := range []string{"main", "Create PR", tt.headline, "CHECKS", "Branch build"} {
				if !strings.Contains(view, want) {
					t.Fatalf("view missing %q: %s", want, view)
				}
			}
			m.folds[1] = true
			if strings.Contains(m.View(), "Branch build") {
				t.Fatal("folded checks still shown")
			}
		})
	}
	m := newModel(t.TempDir())
	m.loaded = true
	m.status = Status{Repo: true, Branch: "main"}
	if view := m.View(); !strings.Contains(view, "no checks reported") || strings.Contains(view, "CI passing") {
		t.Fatalf("empty checks view = %s", view)
	}
}
