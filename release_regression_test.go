package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCIStatusBucketsNeverPromoteUnknownToPass(t *testing.T) {
	tests := []struct {
		name    string
		bucket  string
		overall string
	}{
		{name: "pass", bucket: "pass", overall: "pass"},
		{name: "skipped", bucket: "skipping", overall: "pass"},
		{name: "failure", bucket: "fail", overall: "fail"},
		{name: "cancelled", bucket: "cancel", overall: "fail"},
		{name: "pending", bucket: "pending", overall: "pending"},
		{name: "unknown", bucket: "future-bucket", overall: "pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ciSummary([]Check{{Bucket: tt.bucket}}).Overall; got != tt.overall {
				t.Fatalf("bucket %q overall = %q, want %q", tt.bucket, got, tt.overall)
			}
		})
	}
}

func TestStaleFetchCannotReplaceCurrentPR(t *testing.T) {
	m := newModel("")
	m.viewNum = 22
	m.fetchGeneration = 7
	m.status = Status{Repo: true, Branch: "current", PR: &PR{Number: 22, State: "OPEN"}}

	updated, cmd := m.Update(fetchMsg{
		status:     Status{Repo: true, Branch: "stale", PR: &PR{Number: 11, State: "OPEN"}},
		number:     11,
		generation: 6,
	})
	got := updated.(model)
	if cmd != nil {
		t.Fatal("stale fetch scheduled more work")
	}
	if got.status.PR == nil || got.status.PR.Number != 22 || got.status.Branch != "current" {
		t.Fatalf("stale fetch replaced current status: %+v", got.status)
	}

	updated, _ = got.Update(fetchMsg{
		status:     Status{Repo: true, Branch: "accepted", PR: &PR{Number: 22, State: "MERGED"}},
		number:     22,
		generation: 7,
	})
	got = updated.(model)
	if got.status.PR == nil || got.status.PR.Number != 22 || got.status.Branch != "accepted" {
		t.Fatalf("current fetch was not accepted: %+v", got.status)
	}
}

func TestReloadInvalidatesPendingFetch(t *testing.T) {
	m := newModel("")
	before := m.fetchGeneration
	updated, cmd := m.Update(reloadMsg{})
	got := updated.(model)
	if cmd == nil {
		t.Fatal("reload did not schedule an immediate fetch")
	}
	if got.fetchGeneration != before+1 {
		t.Fatalf("reload generation = %d, want %d", got.fetchGeneration, before+1)
	}
}

func TestActionResultSeverity(t *testing.T) {
	m := newModel("")
	updated, _ := m.Update(successMsg("done"))
	got := updated.(model)
	if got.sent != "done" || got.sentFailed {
		t.Fatalf("success result = text %q failed %v", got.sent, got.sentFailed)
	}
	updated, _ = got.Update(failureMsg("denied"))
	got = updated.(model)
	if got.sent != "denied" || !got.sentFailed {
		t.Fatalf("failure result = text %q failed %v", got.sent, got.sentFailed)
	}
	got.loaded = true
	got.width = 90
	got.status = Status{Repo: true, Branch: "main", PR: &PR{Number: 1, State: "OPEN"}}
	view := got.View()
	if !strings.Contains(view, "✗ denied") || strings.Contains(view, "✓ denied") {
		t.Fatalf("failure was rendered as success: %q", view)
	}
}

func TestNotesAreIsolatedByCheckoutAndPR(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", stateRoot)
	repoA := filepath.Join(t.TempDir(), "repo-a")
	repoB := filepath.Join(t.TempDir(), "repo-b")
	if err := os.MkdirAll(repoA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repoB, 0o700); err != nil {
		t.Fatal(err)
	}
	pr1 := Status{Repo: true, Branch: "main", PR: &PR{Number: 1}}
	pr2 := Status{Repo: true, Branch: "main", PR: &PR{Number: 2}}

	pathA := notesFileFor(repoA, pr1)
	if pathA == notesFileFor(repoB, pr1) {
		t.Fatal("same branch and PR number collided across repositories")
	}
	if pathA == notesFileFor(repoA, pr2) {
		t.Fatal("different PRs collided in the same repository")
	}
	if err := seedNotes(pathA, pr1); err != nil {
		t.Fatal(err)
	}
	if err := writeNotes(repoA, pr1, []string{"a.go:1  private note"}); err != nil {
		t.Fatal(err)
	}
	if got := readNotes(notesFileFor(repoB, pr1)); got != "" {
		t.Fatalf("notes leaked to second repository: %q", got)
	}
}

func TestCICwdUsesRepositoryRoot(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	subdir := filepath.Join(repo, "nested", "dir")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CI_CWD", subdir)
	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(ciCwd())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ciCwd = %q, want repository root %q", got, want)
	}
}

func TestPathWithin(t *testing.T) {
	root := t.TempDir()
	if !pathWithin(root, filepath.Join(root, "tools")) {
		t.Fatal("child path was not recognized as inside")
	}
	if pathWithin(root, filepath.Join(filepath.Dir(root), "peer")) {
		t.Fatal("peer path was recognized as inside")
	}
}
