package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestLimitedCommandOutputCapsData(t *testing.T) {
	var direct cappedBuffer
	direct.limit = maxCommandBytes
	_, _ = direct.Write(bytes.Repeat([]byte{'x'}, maxCommandBytes+1))
	if !direct.truncated || direct.Len() != maxCommandBytes {
		t.Fatalf("direct cap failed: truncated=%v len=%d", direct.truncated, direct.Len())
	}
	cmd := exec.Command("sh", "-c", "printf '%*s' 9000000 ''")
	got, err := limitedCommandOutput(cmd)
	if err == nil || !errors.Is(err, errCommandOutputTooLarge) {
		t.Fatalf("oversized output error = %v (len=%d)", err, len(got))
	}
	if len(got) != maxCommandBytes {
		t.Fatalf("captured %d bytes, want cap %d", len(got), maxCommandBytes)
	}
}

func TestCappedBufferConcurrentWriters(t *testing.T) {
	var b cappedBuffer
	b.limit = 4096
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = b.Write(bytes.Repeat([]byte{'x'}, 2048))
		}()
	}
	wg.Wait()
	if b.Len() != b.limit || !b.truncated {
		t.Fatalf("concurrent cap failed: len=%d truncated=%v", b.Len(), b.truncated)
	}
}

func TestSanitizeTerminalText(t *testing.T) {
	in := "ok\x1b[31m red\x1b[0m \x1b]52;c;ZXZpbA==\aend\r\u202eevil\nnext"
	got := sanitizeTerminalText(in)
	if strings.ContainsAny(got, "\x1b\r\a") || strings.ContainsRune(got, '\u202e') {
		t.Fatalf("terminal controls survived: %q", got)
	}
	if !strings.Contains(got, "ok red end") || !strings.Contains(got, "\nnext") {
		t.Fatalf("printable text was lost: %q", got)
	}
}

func TestNormalizeRepoPath(t *testing.T) {
	for _, good := range []string{"pkg/file.go", ".github/workflows/test.yml", "dir/file name.md"} {
		if got, err := normalizeRepoPath(good); err != nil || got != good {
			t.Fatalf("valid path %q rejected: got=%q err=%v", good, got, err)
		}
	}
	for _, bad := range []string{"", "../../etc/passwd", "foo/../secret", "/etc/passwd", "--cmd=quit", `dir\\file`, "name:with-colon", ".git/config", "a//b", "a\nfile", "NUL", "CONIN$", "CONOUT$", "CLOCK$", "dir/CON.txt", "bad|name"} {
		if _, err := normalizeRepoPath(bad); err == nil {
			t.Fatalf("unsafe path %q accepted", bad)
		}
	}
}

func TestReadRepoFileRejectsTraversalAndSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "safe.go"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := codeLineAt(dir, "src/safe.go:2  note"); got != "two" {
		t.Fatalf("safe source lookup failed: %q", got)
	}
	if got := codeLineAt(dir, "../secret:1  note"); got != "" {
		t.Fatalf("traversal was read: %q", got)
	}
	if err := os.Symlink(filepath.Join("src", "safe.go"), filepath.Join(dir, "link.go")); err == nil {
		if got := codeLineAt(dir, "link.go:1  note"); got != "" {
			t.Fatalf("symlink was read: %q", got)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "src"), filepath.Join(dir, "linkdir")); err == nil {
		if got := codeLineAt(dir, "linkdir/safe.go:1  note"); got != "" {
			t.Fatalf("symlinked directory was read: %q", got)
		}
	}
}

func TestPrivateNotesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(dir, "state"))
	status := Status{Repo: true, Branch: "../../feature/secret", PR: &PR{Number: 7}}
	path := notesFileFor(dir, status)
	if strings.Contains(path, "feature") || strings.Contains(path, "secret") {
		t.Fatalf("branch leaked into notes filename: %s", path)
	}
	seedNotes(path, Status{Repo: true, Branch: "feature/x", PR: &PR{Number: 7}})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("notes permissions = %o, want 600", info.Mode().Perm())
	}
	if err := writeNotes(dir, status, []string{"src/a.go:1  note"}); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(path, bytes.Repeat([]byte{'x'}, maxNotesBytes+1)); !errors.Is(err, errUnsafePath) {
		t.Fatalf("oversized notes write error = %v", err)
	}
	if got := readNotes(path); got != "src/a.go:1  note" {
		t.Fatalf("notes round trip = %q", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside"), path); err == nil {
		if err := seedNotes(path, status); err == nil {
			t.Fatal("symlink notes target was accepted as an existing note")
		}
		if err := writeNotes(dir, status, []string{"should not write"}); err == nil {
			t.Fatal("symlink notes target was accepted")
		}
	}
}

func TestTerminalSanitizerWriter(t *testing.T) {
	var out bytes.Buffer
	w := newTerminalSanitizer(&out)
	if _, err := w.Write([]byte("safe\x1b[31")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("mred\x1b[0m\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(out.String(), '\x1b') || !strings.Contains(out.String(), "safered\n") {
		t.Fatalf("sanitized output = %q", out.String())
	}
}

func TestSafeGitRef(t *testing.T) {
	for _, ref := range []string{"main", "feature/safe-1", "release/v1.2.3"} {
		if !safeGitRef(ref) {
			t.Fatalf("valid ref rejected: %q", ref)
		}
	}
	for _, ref := range []string{"", "-evil", "../main", "feature//x", "x@{1}", "x;touch-pwn", "x.lock"} {
		if safeGitRef(ref) {
			t.Fatalf("unsafe ref accepted: %q", ref)
		}
	}
}

func TestValidGitHubRemote(t *testing.T) {
	for _, remote := range []string{
		"https://github.com/owner/repo",
		"https://github.com/owner/repo.git",
		"ssh://git@github.com/owner/repo.git",
		"git@github.com:owner/repo.git",
	} {
		if !validGitHubRemote(remote) {
			t.Fatalf("valid GitHub remote rejected: %q", remote)
		}
	}
	for _, remote := range []string{
		"https://evil.example/owner/repo",
		"https://github.com.evil.example/owner/repo",
		"https://github.com:443/owner/repo",
		"https://github.com/[::1]/owner/repo",
		"https://github.com/owner/repo#fragment",
		"https://github.com/owner/repo?redirect=evil",
		"ssh://evil@github.com/owner/repo",
		"git@evil.example:owner/repo",
		"https://github.com/owner/repo/../../secret",
	} {
		if validGitHubRemote(remote) {
			t.Fatalf("unsafe GitHub remote accepted: %q", remote)
		}
	}
}

func TestGitHubRemoteRepo(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/owner/repo.git": "owner/repo",
		"ssh://git@github.com/owner/repo":   "owner/repo",
		"git@github.com:owner/repo.git":     "owner/repo",
	} {
		if got := githubRemoteRepo(remote); got != want {
			t.Fatalf("remote repo %q = %q, want %q", remote, got, want)
		}
	}
}

func TestLocalGitURLRewritesFailClosed(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	if found, ok := localGitURLRewrites(dir); !ok || found {
		t.Fatalf("clean local config rewrite result = found %v ok %v", found, ok)
	}
	cmd := exec.Command("git", "-C", dir, "config", "url.https://evil.example/.insteadOf", "https://github.com/")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("set rewrite: %v (%s)", err, out)
	}
	if found, ok := localGitURLRewrites(dir); !ok || !found {
		t.Fatalf("direct local rewrite result = found %v ok %v", found, ok)
	}
}

func TestSafeIdentifier(t *testing.T) {
	for _, value := range []string{"pane-123", "workspace_1", "agent/name@host"} {
		if !safeIdentifier(value) {
			t.Fatalf("safe identifier rejected: %q", value)
		}
	}
	for _, value := range []string{"", "--help", "pane id", "pane\n1", "pane\x1b[31m"} {
		if safeIdentifier(value) {
			t.Fatalf("unsafe identifier accepted: %q", value)
		}
	}
}

func TestSecureShellEnvDropsRelativePathEntries(t *testing.T) {
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", "."+sep+"relative/bin"+sep+"/usr/bin"+sep+""+sep+"/bin")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_0", "!touch /tmp/pwn")
	t.Setenv("GH_REPO", "attacker/repo")
	t.Setenv("GH_CONFIG_DIR", "/tmp/attacker-gh")
	t.Setenv("BASH_ENV", "/tmp/attacker-shell")
	t.Setenv("LD_PRELOAD", "/tmp/attacker.so")
	var got string
	var entries []string
	for _, entry := range secureShellEnv() {
		entries = append(entries, entry)
		if strings.HasPrefix(entry, "PATH=") {
			got = strings.TrimPrefix(entry, "PATH=")
		}
	}
	if got == "" || strings.Contains(got, ".") || strings.Contains(got, "relative/bin") {
		t.Fatalf("insecure PATH survived: %q", got)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry, "GIT_CONFIG_COUNT=") || strings.HasPrefix(entry, "GIT_CONFIG_KEY_") || strings.HasPrefix(entry, "GIT_CONFIG_VALUE_") {
			t.Fatalf("inherited Git config override survived: %q", entry)
		}
	}
	env := secureShellEnv()
	joined := strings.Join(env, "\x00")
	if !strings.Contains(joined, "GIT_CONFIG_GLOBAL="+os.DevNull) || !strings.Contains(joined, "GIT_CONFIG_SYSTEM="+os.DevNull) {
		t.Fatalf("global/system Git config was not disabled")
	}
	if strings.Contains(joined, "GH_REPO=attacker/repo") || strings.Contains(joined, "GH_CONFIG_DIR=/tmp/attacker-gh") {
		t.Fatalf("inherited gh routing/config override survived")
	}
	if strings.Contains(joined, "BASH_ENV=/tmp/attacker-shell") || strings.Contains(joined, "LD_PRELOAD=/tmp/attacker.so") {
		t.Fatalf("inherited shell/loader override survived")
	}
	if !strings.Contains(joined, "NO_COLOR=1") || !strings.Contains(joined, "CLICOLOR=0") {
		t.Fatalf("color output was not disabled for subprocesses")
	}
}

func TestSecureShellEnvForDirDropsWorktreePath(t *testing.T) {
	dir := t.TempDir()
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", filepath.Join(dir, "bin")+sep+"/usr/bin")
	env := secureShellEnvForDir(dir)
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			pathValue := strings.TrimPrefix(entry, "PATH=")
			if strings.Contains(pathValue, dir) {
				t.Fatalf("worktree PATH entry survived: %q", pathValue)
			}
			return
		}
	}
	t.Fatal("secure environment has no PATH")
}

func TestNewReviewRefIsUniqueAndScoped(t *testing.T) {
	a, err := newReviewRef()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newReviewRef()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !regexp.MustCompile(`^refs/ci/herdr-gh-checks/[0-9a-f]{32}$`).MatchString(a) || !regexp.MustCompile(`^refs/ci/herdr-gh-checks/[0-9a-f]{32}$`).MatchString(b) {
		t.Fatalf("unexpected temporary refs: %q, %q", a, b)
	}
}

func TestReviewVimPathUsesEmbeddedScript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_ROOT", dir)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(dir, "state"))
	got := reviewVimPath()
	if got == "" {
		t.Fatal("embedded review script path is empty")
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, embeddedReviewVim) {
		t.Fatal("cached review script differs from embedded content")
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cached review script permissions = %o, want 600", info.Mode().Perm())
	}
	// A writable adjacent file or HERDR_PLUGIN_ROOT override cannot change the
	// script selected by the binary.
	if err := os.WriteFile(filepath.Join(dir, "review.vim"), []byte("echoerr 'pwn'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if again := reviewVimPath(); again != got {
		t.Fatalf("embedded script path changed after adjacent file write: %q vs %q", again, got)
	}
}
