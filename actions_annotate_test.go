package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnnotationShellBodyMaterializesMissingSides(t *testing.T) {
	repo := t.TempDir()
	runGitTest(t, repo, "init", "-q")
	runGitTest(t, repo, "config", "user.name", "Test User")
	runGitTest(t, repo, "config", "user.email", "test@example.invalid")
	writeTestFile(t, filepath.Join(repo, "deleted.txt"), "deleted\n")
	writeTestFile(t, filepath.Join(repo, "old.txt"), "renamed\n")
	runGitTest(t, repo, "add", "--", "deleted.txt", "old.txt")
	runGitTest(t, repo, "commit", "-qm", "base")
	baseCommit := gitOutputTest(t, repo, "rev-parse", "HEAD")
	const base = "base-test"
	runGitTest(t, repo, "update-ref", "refs/remotes/origin/"+base, baseCommit)

	writeTestFile(t, filepath.Join(repo, "added.txt"), "added\n")
	if err := os.Remove(filepath.Join(repo, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(repo, "old.txt"), filepath.Join(repo, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "-A")
	runGitTest(t, repo, "commit", "-qm", "head")
	head := gitOutputTest(t, repo, "rev-parse", "HEAD")

	tests := []struct {
		name      string
		path      string
		worktree  bool
		wantLeft  string
		wantRight string
	}{
		{name: "added", path: "added.txt", worktree: true, wantRight: "added\n"},
		{name: "deleted", path: "deleted.txt", wantLeft: "deleted\n"},
		{name: "renamed", path: "renamed.txt", worktree: true, wantRight: "renamed\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			captureDir := t.TempDir()
			left := filepath.Join(captureDir, "left")
			right := filepath.Join(captureDir, "right")
			script := `git_safe() { git "$@"; }; nvim() { previous=; current=; for argument do previous=$current; current=$argument; done; command cp "$previous" "$CAP_LEFT" && command cp "$current" "$CAP_RIGHT"; }; base=$BASE; head=$HEAD; ` + annotationShellBody(0)
			cmd := exec.CommandContext(t.Context(), "sh", "-c", script)
			cmd.Dir = repo
			cmd.Env = append(os.Environ(),
				"BASE="+base,
				"HEAD="+head,
				"FILE="+tt.path,
				"VIMRC=/dev/null",
				"CAP_LEFT="+filepath.ToSlash(left),
				"CAP_RIGHT="+filepath.ToSlash(right),
			)
			if tt.worktree {
				cmd.Env = append(cmd.Env, "USE_WORKTREE=1")
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("annotation script: %v (%s)", err, out)
			}
			if got := readTestFile(t, left); got != tt.wantLeft {
				t.Fatalf("left side = %q, want %q", got, tt.wantLeft)
			}
			if got := readTestFile(t, right); got != tt.wantRight {
				t.Fatalf("right side = %q, want %q", got, tt.wantRight)
			}
		})
	}
}

func TestPRRefCleanupPreservesCommandFailure(t *testing.T) {
	script := `cleanup_review_ref() { :; }; false` + prRefCleanup(1)
	if err := exec.CommandContext(t.Context(), "sh", "-c", script).Run(); err == nil {
		t.Fatal("cleanup masked the preceding command failure")
	}
}

func TestExecProcessResultReportsFailure(t *testing.T) {
	msg, ok := execProcessResult("review", errors.New("boom")).(sentMsg)
	if !ok || !msg.failed || !strings.Contains(msg.text, "review failed") {
		t.Fatalf("failure result = %#v", msg)
	}
	if _, ok := execProcessResult("review", nil).(reloadMsg); !ok {
		t.Fatal("successful process did not request a reload")
	}
}

func runGitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, out)
	}
}

func gitOutputTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

func writeTestFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
