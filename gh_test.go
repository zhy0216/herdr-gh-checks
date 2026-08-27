package main

import (
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPRRefShell(t *testing.T) {
	cur := prRefShell(0)
	if !strings.Contains(cur, "head=HEAD") || strings.Contains(cur, "pull/") || prRefCleanup(0) != "" {
		t.Fatalf("num=0 should use HEAD, no fetch of pull/, no cleanup: %q", cur)
	}
	pr := prRefShell(7)
	if !strings.Contains(pr, `pull/$NUM/head:$REVIEW_REF`) || !strings.Contains(pr, "head=$REVIEW_REF") || !strings.Contains(pr, "trap cleanup_review_ref EXIT") || !strings.Contains(prRefCleanup(7), "cleanup_review_ref") {
		t.Fatalf("num>0 should fetch pull head into a temporary ref and clean it up: %q", pr)
	}
}

func TestPRRefShellSyntax(t *testing.T) {
	for _, num := range []int{0, 7} {
		script := prRefShell(num) + `; printf '%s' "$head"` + prRefCleanup(num)
		if out, err := exec.Command("sh", "-n", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("num=%d shell syntax: %v (%s)", num, err, out)
		}
	}
}

func TestView(t *testing.T) {
	m := newModel("")
	m.width = 90
	m.loaded = true
	m.status = Status{Repo: true, Branch: "b", PR: &PR{
		Number: 1, State: "OPEN", Title: "t",
		Checks:       []Check{{Name: "a", Bucket: "pass"}, {Name: "b", Bucket: "fail"}},
		ChangedFiles: 1, Files: []File{{Path: "x", Additions: 1}},
	}}
	out := m.View()
	if !strings.Contains(out, "CI failing") {
		t.Fatal("missing headline")
	}
	if !strings.Contains(out, "pass") {
		t.Fatal("missing checks summary")
	}
}

func TestLogic(t *testing.T) {
	eq := func(got, want, msg string) {
		if got != want {
			t.Fatalf("%s: got %q want %q", msg, got, want)
		}
	}
	eq(ciSummary([]Check{{Bucket: "pass"}, {Bucket: "fail"}}).Overall, "fail", "fail wins")
	eq(ciSummary([]Check{{Bucket: "pass"}, {Bucket: "pending"}}).Overall, "pending", "pending over pass")
	eq(ciSummary([]Check{{Bucket: "pass"}}).Overall, "pass", "pass")
	eq(ciSummary(nil).Overall, "none", "none")
	eq(stateOf(Status{Repo: false}), "", "no repo")
	eq(stateOf(Status{Repo: true, PR: nil}), "", "no pr")
	eq(stateOf(Status{Repo: true, PR: &PR{State: "MERGED"}}), "merged", "merged")
	eq(stateOf(Status{Repo: true, PR: &PR{State: "OPEN", Checks: []Check{{Bucket: "fail"}}}}), "fail", "fail")
	eq(stateOf(Status{Repo: true, PR: &PR{State: "OPEN", Checks: []Check{{Bucket: "pending"}}}}), "run", "run")
	eq(trunc("hello", 3), "he…", "trunc")
	eq(truncTail("abcdef", 3), "…ef", "truncTail")
}

func TestWorkflowRequiresConfirmation(t *testing.T) {
	m := newModel("")
	m.loaded = true
	m.width = 80
	m.status = Status{Repo: true, Branch: "main", PR: &PR{Number: 1, State: "OPEN"}}
	m.workflows = []Workflow{{ID: 42, Name: "deploy", State: "active"}}
	m.branches = []string{"main"}
	m.focus = 1
	modelAfter, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := modelAfter.(model)
	// Enter first opens the branch picker; it must not dispatch yet.
	if !got.wfBranch || got.confirmAction != "" {
		t.Fatalf("workflow branch picker state = %+v", got)
	}
	modelAfter, _ = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = modelAfter.(model)
	if got.confirmAction != "workflow" || got.confirmNumber != 42 || got.confirmRef != "main" {
		t.Fatalf("workflow confirmation state = %+v", got)
	}
}
