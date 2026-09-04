package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

var errUnsafePath = errors.New("unsafe path")

const (
	maxRepoFileBytes = 16 << 20
	maxNotesBytes    = 1 << 20
	maxCommandBytes  = 8 << 20
)

var errCommandOutputTooLarge = errors.New("command output too large")

// cappedBuffer keeps hostile CLI/API output from becoming an unbounded memory
// allocation. It consumes the remainder after the cap so the child process can
// exit normally, then callers treat truncation as a failed command.
type cappedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *cappedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *cappedBuffer) truncatedLocked() bool { return b.truncated }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit <= b.buf.Len() {
		b.truncated = true
		return len(p), nil
	}
	left := b.limit - b.buf.Len()
	if len(p) > left {
		_, _ = b.buf.Write(p[:left])
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func limitedCommandOutput(cmd *exec.Cmd) ([]byte, error) {
	var out cappedBuffer
	out.limit = maxCommandBytes
	cmd.Stdout = &out
	err := cmd.Run()
	out.mu.Lock()
	truncated := out.truncatedLocked()
	out.mu.Unlock()
	if truncated && err == nil {
		err = errCommandOutputTooLarge
	}
	return out.Bytes(), err
}

func limitedCombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	var out cappedBuffer
	out.limit = maxCommandBytes
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	out.mu.Lock()
	truncated := out.truncatedLocked()
	out.mu.Unlock()
	if truncated && err == nil {
		err = errCommandOutputTooLarge
	}
	return out.Bytes(), err
}

// commandWithEnv resolves a command against the exact sanitized PATH that will
// be given to the child. exec.Command performs its own LookPath call before a
// caller can assign Cmd.Env, so merely setting Env after construction would
// still allow a relative PATH entry in the parent process to shadow `git`,
// `gh`, or `sh`.
func commandWithEnv(name string, processEnv []string, args ...string) *exec.Cmd {
	if processEnv == nil {
		processEnv = secureShellEnv()
	}
	resolved := resolveExecutable(name, processEnv)
	// #nosec G204 -- `resolved` is selected only from the sanitized absolute
	// PATH supplied to the child; callers never pass untrusted shell text here.
	cmd := exec.Command(resolved, args...)
	cmd.Env = processEnv
	return cmd
}

func resolveExecutable(name string, processEnv []string) string {
	if name == "" || hasPathSeparator(name) {
		return name
	}
	pathValue := ""
	for _, entry := range processEnv {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if key == "PATH" || (runtime.GOOS == "windows" && strings.EqualFold(key, "path")) {
			pathValue = value
			break
		}
	}
	sep := string(os.PathListSeparator)
	var dirs []string
	for _, dir := range strings.Split(pathValue, sep) {
		if dir == "" || dir == "." || !filepath.IsAbs(dir) {
			continue
		}
		dirs = append(dirs, dir)
	}
	if len(dirs) == 0 {
		if runtime.GOOS == "windows" {
			dirs = []string{`C:\Windows\System32`}
		} else {
			dirs = []string{"/usr/bin"}
		}
	}
	for _, dir := range dirs {
		for _, candidate := range executableCandidates(dir, name) {
			if executableFile(candidate) {
				return candidate
			}
		}
	}
	// Keep command-not-found behavior without falling back to exec.Command's
	// lookup against the unsanitized parent PATH.
	return filepath.Join(dirs[0], name)
}

func hasPathSeparator(name string) bool {
	return strings.ContainsRune(name, '/') || (runtime.GOOS == "windows" && strings.ContainsRune(name, '\\'))
}

func executableCandidates(dir, name string) []string {
	base := filepath.Join(dir, name)
	if runtime.GOOS != "windows" || filepath.Ext(name) != "" {
		return []string{base}
	}
	return []string{base + ".exe", base + ".com", base + ".bat", base + ".cmd", base}
}

func executableFile(name string) bool {
	info, err := os.Stat(name)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

// sanitizeTerminalText removes terminal control sequences and direction-changing
// Unicode controls from data that came from GitHub, git, or another subprocess.
// Newlines and tabs are retained so normal descriptions and command output stay readable.
func sanitizeTerminalText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b { // ESC: CSI, OSC, DCS, APC, and two-byte escape sequences.
			i = skipEscapeSequence(s, i)
			continue
		}

		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
			i++
			continue
		}
		i += size

		switch r {
		case '\n', '\t':
			b.WriteRune(r)
			continue
		case 0x009b: // C1 CSI
			i = skipCSI(s, i)
			continue
		case 0x0090, 0x0098, 0x009d, 0x009e, 0x009f: // DCS/SOS/OSC/PM/APC
			i = skipStringControl(s, i)
			continue
		}

		if unicode.IsControl(r) {
			b.WriteRune(utf8.RuneError)
			continue
		}
		if isDirectionControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func skipEscapeSequence(s string, i int) int {
	i++
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		return skipCSI(s, i+1)
	case ']', 'P', 'X', '^', '_':
		return skipStringControl(s, i+1)
	default:
		for i < len(s) {
			c := s[i]
			i++
			if c >= 0x30 && c <= 0x7e {
				break
			}
		}
		return i
	}
}

func skipCSI(s string, i int) int {
	for i < len(s) {
		c := s[i]
		i++
		if c >= 0x40 && c <= 0x7e {
			break
		}
	}
	return i
}

func skipStringControl(s string, i int) int {
	for i < len(s) {
		if s[i] == 0x07 { // BEL terminates OSC.
			return i + 1
		}
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' { // ST
			return i + 2
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == 0x009c { // C1 ST
			return i + size
		}
		i += size
	}
	return i
}

func isDirectionControl(r rune) bool {
	return (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x200e || r == 0x200f || r == 0x061c
}

// terminalSanitizer buffers complete lines so escape sequences split across
// subprocess writes cannot reach the terminal piecemeal.
type terminalSanitizer struct {
	mu  sync.Mutex
	w   io.Writer
	buf []byte
}

func newTerminalSanitizer(w io.Writer) *terminalSanitizer {
	return &terminalSanitizer{w: w}
}

func (w *terminalSanitizer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := append([]byte(nil), w.buf[:i+1]...)
		w.buf = w.buf[i+1:]
		if _, err := io.WriteString(w.w, sanitizeTerminalText(string(line))); err != nil {
			return len(p), err
		}
	}
	if len(w.buf) > 32*1024 {
		if err := w.flushLocked(); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

func (w *terminalSanitizer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

func (w *terminalSanitizer) flushLocked() error {
	if len(w.buf) == 0 {
		return nil
	}
	_, err := io.WriteString(w.w, sanitizeTerminalText(string(w.buf)))
	w.buf = w.buf[:0]
	return err
}

// normalizeRepoPath accepts only a canonical, repository-relative Git path.
// In particular it rejects traversal, option-looking names, Windows path
// separators/volumes, git revision separators, control characters, and .git.
func normalizeRepoPath(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\\:") {
		return "", fmt.Errorf("%w: %q", errUnsafePath, name)
	}
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("%w: invalid UTF-8", errUnsafePath)
	}
	if strings.HasPrefix(name, "/") || filepath.IsAbs(filepath.FromSlash(name)) || filepath.VolumeName(filepath.FromSlash(name)) != "" {
		return "", fmt.Errorf("%w: %q", errUnsafePath, name)
	}
	for _, r := range name {
		if unicode.IsControl(r) || isDirectionControl(r) || unicode.Is(unicode.Cf, r) {
			return "", fmt.Errorf("%w: control character", errUnsafePath)
		}
		if strings.ContainsRune("*?<>|", r) {
			return "", fmt.Errorf("%w: unsupported filename character", errUnsafePath)
		}
	}
	clean := path.Clean(name)
	if clean != name || clean == "." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q", errUnsafePath, name)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return "", fmt.Errorf("%w: %q", errUnsafePath, name)
		}
		if strings.TrimRight(part, " .") != part || isWindowsDeviceName(part) {
			return "", fmt.Errorf("%w: reserved filename", errUnsafePath)
		}
	}
	return clean, nil
}

func isWindowsDeviceName(part string) bool {
	base := part
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToUpper(base)
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" || base == "CLOCK$" {
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		return base[3] >= '1' && base[3] <= '9'
	}
	return false
}

// validateRepoPath additionally verifies that an existing worktree file is a
// regular file and that none of its path components are symlinks.
func validateRepoPath(cwd, name string, requireRegular bool) (string, error) {
	rel, err := normalizeRepoPath(name)
	if err != nil || !requireRegular {
		return rel, err
	}
	rootPath, err := repoRootPath(cwd)
	if err != nil {
		return "", err
	}

	cur := ""
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		cur = filepath.Join(cur, filepath.FromSlash(part))
		info, err := os.Lstat(filepath.Join(rootPath, cur))
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: symlink %q", errUnsafePath, name)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("%w: non-directory component %q", errUnsafePath, name)
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return "", fmt.Errorf("%w: non-regular file %q", errUnsafePath, name)
		}
	}
	return rel, nil
}

func repoRootPath(cwd string) (string, error) {
	root, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", err
	}
	return filepath.Abs(root)
}

func readRepoFile(cwd, name string) ([]byte, error) {
	rel, err := validateRepoPath(cwd, name, true)
	if err != nil {
		return nil, err
	}
	rootPath, err := repoRootPath(cwd)
	if err != nil {
		return nil, err
	}
	f, err := openRepoNoFollow(rootPath, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxRepoFileBytes {
		return nil, fmt.Errorf("%w: non-regular file %q", errUnsafePath, name)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRepoFileBytes+1))
	if err != nil || int64(len(data)) > maxRepoFileBytes {
		return nil, fmt.Errorf("%w: file too large", errUnsafePath)
	}
	return data, err
}

// safeGitRef is deliberately narrower than all names Git permits. The plugin
// only needs normal branch names, and the restricted alphabet keeps refs safe
// across gh, git, shell, and Windows boundaries.
func safeGitRef(ref string) bool {
	if ref == "" || len(ref) > 255 || strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") {
		return false
	}
	for _, r := range ref {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || strings.ContainsRune("-._/", r) {
			continue
		}
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(strings.ToLower(part), ".lock") {
			return false
		}
	}
	return true
}

// safeIdentifier covers IDs returned by the local Herdr API. They are passed
// to a CLI as positional/option values, so reject option-looking and control
// characters even though no shell is involved.
func safeIdentifier(value string) bool {
	if value == "" || len(value) > 256 || strings.HasPrefix(value, "-") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) || strings.ContainsRune("-._:/@", r) {
			continue
		}
		return false
	}
	return true
}

func validPRNumber(number int) bool { return number > 0 }

// secureShellEnv removes empty and relative PATH entries before a shell is
// started in a repository. Otherwise a checkout containing a file named
// `git`, `gh`, or `nvim` could win command lookup when the user's PATH includes
// `.` (or another relative directory).
func secureShellEnv() []string {
	rawEnv := os.Environ()
	pathValue := ""
	pathFound := false
	for _, entry := range rawEnv {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || (key != "PATH" && !(runtime.GOOS == "windows" && strings.EqualFold(key, "path"))) {
			continue
		}
		if !pathFound {
			pathValue = value
			pathFound = true
		}
	}
	sep := string(os.PathListSeparator)
	var dirs []string
	for _, dir := range strings.Split(pathValue, sep) {
		if dir == "" || dir == "." || !filepath.IsAbs(dir) {
			continue
		}
		dirs = append(dirs, dir)
	}
	if len(dirs) == 0 {
		if runtime.GOOS == "windows" {
			dirs = []string{`C:\Windows\System32`, `C:\Windows`}
		} else {
			dirs = []string{"/usr/bin", "/bin"}
		}
	}
	safePath := strings.Join(dirs, sep)

	env := make([]string, 0, len(rawEnv)+1)
	seenPath := false
	for _, entry := range rawEnv {
		key, _, hasValue := strings.Cut(entry, "=")
		if unsafeInheritedEnvKey(key) {
			// Shell startup, dynamic-loader, interpreter, and Git/GitHub routing
			// variables can replace helpers or execute attacker-selected code.
			continue
		}
		if hasValue && (key == "PATH" || (runtime.GOOS == "windows" && strings.EqualFold(key, "path"))) {
			if seenPath {
				continue
			}
			seenPath = true
			env = append(env, "PATH="+safePath)
			continue
		}
		env = append(env, entry)
	}
	if !seenPath {
		env = append(env, "PATH="+safePath)
	}
	// Safe defaults for the Git subprocesses used by review shells. These are
	// deliberately explicit so an absent inherited variable cannot re-enable a
	// prompt or a custom transport/helper.
	env = append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"GIT_SSH_COMMAND=ssh",
		"GH_HOST=github.com",
		"GH_PAGER=cat",
		"PAGER=cat",
		"LESS=-FRX",
		"NO_COLOR=1",
		"CLICOLOR=0",
		"GH_DEBUG=",
		"DEBUG=",
	)
	return env
}

// secureShellEnvForDir additionally removes PATH entries that resolve inside
// the repository being inspected. This handles setups that put a worktree's
// `.venv/bin`, `node_modules/.bin`, or root directory on PATH using an absolute
// path; those entries are just as able to shadow git/gh/nvim as `.` is.
func secureShellEnvForDir(cwd string) []string {
	env := secureShellEnv()
	if cwd == "" {
		return env
	}
	root := resolvedAbsPath(cwd)
	if root == "" {
		return env
	}
	pathValue := ""
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (key == "PATH" || (runtime.GOOS == "windows" && strings.EqualFold(key, "path"))) {
			pathValue = value
			break
		}
	}
	sep := string(os.PathListSeparator)
	var dirs []string
	for _, dir := range strings.Split(pathValue, sep) {
		if dir == "" || dir == "." || !filepath.IsAbs(dir) || pathWithin(root, dir) {
			continue
		}
		dirs = append(dirs, dir)
	}
	if len(dirs) == 0 {
		if runtime.GOOS == "windows" {
			dirs = []string{`C:\Windows\System32`, `C:\Windows`}
		} else {
			dirs = []string{"/usr/bin", "/bin"}
		}
	}
	safePath := strings.Join(dirs, sep)
	for i, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (key == "PATH" || (runtime.GOOS == "windows" && strings.EqualFold(key, "path"))) {
			env[i] = "PATH=" + safePath
			break
		}
	}
	return env
}

func resolvedAbsPath(name string) string {
	abs, err := filepath.Abs(name)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	// The candidate PATH directory may not exist yet. Resolve its longest
	// existing parent (important on macOS, where /var is a symlink), then append
	// the non-existent suffix lexically.
	var suffix []string
	cur := abs
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		suffix = append(suffix, filepath.Base(cur))
		cur = parent
	}
	return filepath.Clean(abs)
}

func pathWithin(root, candidate string) bool {
	root = resolvedAbsPath(root)
	candidate = resolvedAbsPath(candidate)
	if root == "" || candidate == "" {
		return false
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		// Different Windows volumes cannot contain one another. Treat an
		// unrepresentable relative path as outside rather than dropping every
		// trusted tool from PATH for a cross-volume checkout.
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func unsafeInheritedEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	if strings.HasPrefix(upper, "GIT_") {
		return true
	}
	if strings.HasPrefix(upper, "LD_") || strings.HasPrefix(upper, "DYLD_") {
		return true
	}
	switch upper {
	case "BASH_ENV", "ENV", "IFS", "CDPATH", "SHELLOPTS", "BASHOPTS",
		"NODE_OPTIONS", "NODE_PATH", "PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP", "PYTHONINSPECT",
		"RUBYOPT", "RUBYLIB", "PERL5OPT", "PERL5LIB", "PERLLIB",
		"GH_REPO", "GH_HOST", "GH_CONFIG_DIR", "GH_PAGER", "PAGER", "LESS":
		return true
	}
	return runtime.GOOS == "windows" && upper == "PATHEXT"
}

func notesNameForReview(cwd, branch string, number int) string {
	repoIdentity := resolvedAbsPath(cwd)
	if runtime.GOOS == "windows" {
		repoIdentity = strings.ToLower(repoIdentity)
	}
	sum := sha256.Sum256([]byte(repoIdentity + "\x00" + branch + "\x00" + strconv.Itoa(number)))
	return fmt.Sprintf("review-%x.md", sum)
}

func newReviewRef() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return "refs/ci/herdr-gh-checks/" + hex.EncodeToString(token[:]), nil
}

func embeddedReviewScriptPath() string {
	if len(embeddedReviewVim) == 0 {
		return ""
	}
	sum := sha256.Sum256(embeddedReviewVim)
	base := fmt.Sprintf("review-script-%x.vim", sum)
	target, err := privateStateTarget(base)
	if err != nil {
		return ""
	}
	data, err := readPrivateStateFile(base, 64<<10)
	if err == nil {
		if bytes.Equal(data, embeddedReviewVim) {
			return target
		}
		// Never overwrite a same-named file whose contents do not match the
		// embedded script; it may have been replaced by another process.
		return ""
	}
	if !os.IsNotExist(err) {
		return ""
	}
	if err := createPrivateStateFile(base, embeddedReviewVim, 64<<10); err != nil && !os.IsExist(err) {
		return ""
	}
	data, err = readPrivateStateFile(base, 64<<10)
	if err != nil || !bytes.Equal(data, embeddedReviewVim) {
		return ""
	}
	return target
}

func ensurePrivateStateDir() (string, error) {
	dir, err := filepath.Abs(stateDir())
	if err != nil {
		return "", err
	}
	dir = filepath.Clean(dir)
	// Never turn a filesystem root (or another broad caller-selected directory)
	// into the plugin's private store. stateDir always appends the dedicated
	// component, but keep this invariant local to the security boundary too.
	if filepath.Base(dir) != "herdr-gh-checks" || filepath.Dir(dir) == dir {
		return "", fmt.Errorf("unsafe state directory: %s", dir)
	}
	if info, err := os.Lstat(dir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("state directory is not a real directory: %s", dir)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// #nosec G302 -- 0700 is intentional: this is a directory, not a data file.
	if err := chmodPrivateDir(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func privateNotesTarget(name string) (string, string, error) {
	dir, err := ensurePrivateStateDir()
	if err != nil {
		return "", "", err
	}
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", "", err
	}
	base := filepath.Base(abs)
	digest := strings.TrimSuffix(strings.TrimPrefix(base, "review-"), ".md")
	if filepath.Dir(abs) != dir || !strings.HasPrefix(base, "review-") || !strings.HasSuffix(base, ".md") || len(digest) != 64 {
		return "", "", fmt.Errorf("%w: notes path", errUnsafePath)
	}
	for _, r := range digest {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return "", "", fmt.Errorf("%w: notes filename", errUnsafePath)
		}
	}
	return dir, base, nil
}

func privateStateTarget(base string) (string, error) {
	dir, err := ensurePrivateStateDir()
	if err != nil {
		return "", err
	}
	if base == "" || filepath.Base(base) != base || strings.ContainsAny(base, `/\\:`) || strings.HasPrefix(base, ".") || strings.HasPrefix(base, "-") {
		return "", fmt.Errorf("%w: private state filename", errUnsafePath)
	}
	for _, r := range base {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", fmt.Errorf("%w: private state filename", errUnsafePath)
		}
	}
	return filepath.Join(dir, base), nil
}

func createPrivateStateFile(base string, data []byte, max int) error {
	if len(data) > max {
		return fmt.Errorf("%w: private state file too large", errUnsafePath)
	}
	name, err := privateStateTarget(base)
	if err != nil {
		return err
	}
	f, err := openNoFollow(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := ensureRegularDescriptor(f, 0); err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func readPrivateStateFile(base string, max int) ([]byte, error) {
	name, err := privateStateTarget(base)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: private state file", errUnsafePath)
	}
	f, err := openNoFollow(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info.Mode().Perm()&0o077 != 0 {
		if err := f.Chmod(0o600); err != nil {
			return nil, err
		}
	}
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(max) {
		return nil, fmt.Errorf("%w: private state file too large", errUnsafePath)
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil || len(data) > max {
		return nil, fmt.Errorf("%w: private state file too large", errUnsafePath)
	}
	return data, nil
}

func createPrivateFile(name string, data []byte) error {
	if len(data) > maxNotesBytes {
		return fmt.Errorf("%w: notes file too large", errUnsafePath)
	}
	dir, base, err := privateNotesTarget(name)
	if err != nil {
		return err
	}
	f, err := openNoFollow(filepath.Join(dir, base), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := ensureRegularDescriptor(f, 0); err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func writePrivateFile(name string, data []byte) error {
	if len(data) > maxNotesBytes {
		return fmt.Errorf("%w: notes file too large", errUnsafePath)
	}
	dir, base, err := privateNotesTarget(name)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(filepath.Join(dir, base)); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: notes file is not regular", errUnsafePath)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := openNoFollow(filepath.Join(dir, base), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := ensureRegularDescriptor(f, 0); err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func ensureRegularDescriptor(f *os.File, max int64) error {
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || (max >= 0 && info.Size() > max) {
		return fmt.Errorf("%w: private file is not regular", errUnsafePath)
	}
	return nil
}

func readPrivateFile(name string) ([]byte, error) {
	dir, base, err := privateNotesTarget(name)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(filepath.Join(dir, base))
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: notes file", errUnsafePath)
	}
	// The descriptor below is opened without following the final link; chmod it
	// after opening rather than chmodding a path.
	f, err := openNoFollow(filepath.Join(dir, base), os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info.Mode().Perm()&0o077 != 0 {
		if err := f.Chmod(0o600); err != nil {
			return nil, err
		}
	}
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxNotesBytes {
		return nil, fmt.Errorf("%w: notes file too large", errUnsafePath)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxNotesBytes+1))
	if err != nil || int64(len(data)) > maxNotesBytes {
		return nil, fmt.Errorf("%w: notes file too large", errUnsafePath)
	}
	return data, err
}
