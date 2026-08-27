package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	dim    = lipgloss.NewStyle().Faint(true)
	bold   = lipgloss.NewStyle().Bold(true)
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	cyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("44"))
	mauve  = lipgloss.NewStyle().Foreground(lipgloss.Color("170"))
	sect   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("44"))
)

func has(flag string) bool {
	for _, a := range os.Args[1:] {
		if a == flag {
			return true
		}
	}
	return false
}

func main() {
	act := os.Getenv("HERDR_PLUGIN_ACTION_ID")
	switch {
	case act == "merge":
		openPane("merge", "popup", "")
	case act != "":
		openPane("panel", "split", "right")
	case has("--merge"):
		mergeFlow()
	case has("--sidebar"):
		sidebarLoop()
	default:
		watchPane()
	}
}

// openPane shells back into herdr to launch a plugin pane next to the caller.
func openPane(entrypoint, placement, direction string) {
	cwd := ctxCwd()
	id := pluginID()
	if !safeIdentifier(id) {
		return
	}
	args := []string{"plugin", "pane", "open", "--plugin", id, "--entrypoint", entrypoint, "--placement", placement}
	if direction != "" {
		args = append(args, "--direction", direction)
	}
	if placement != "popup" {
		args = append(args, "--no-focus")
	}
	if cwd != "" {
		args = append(args, "--env", "CI_CWD="+cwd)
	}
	// #nosec G204 -- all values are separate arguments; herdrBin is an explicit
	// local executable override and no shell is invoked.
	out, err := func() ([]byte, error) {
		cmd := commandWithEnv(herdrBin(), secureShellEnvForDir(cwd), args...)
		return limitedCommandOutput(cmd)
	}()
	if err != nil {
		return
	}
	// ponytail: splits take no width flag, so narrow the fresh split after open. Approximate & layout-dependent; tune the amount.
	if placement == "split" && direction == "right" {
		var m struct {
			Result struct {
				PluginPane struct {
					Pane struct {
						ID string `json:"pane_id"`
					} `json:"pane"`
				} `json:"plugin_pane"`
			} `json:"result"`
		}
		if json.Unmarshal(out, &m) == nil && safeIdentifier(m.Result.PluginPane.Pane.ID) {
			// #nosec G204 -- pane ID is passed as an argument, never through a shell.
			_ = commandWithEnv(herdrBin(), secureShellEnvForDir(cwd), "pane", "resize", "--pane", m.Result.PluginPane.Pane.ID, "--direction", "right", "--amount", "0.12").Run()
		}
	}
}

func ctxCwd() string {
	if ctx := os.Getenv("HERDR_PLUGIN_CONTEXT_JSON"); ctx != "" {
		var m struct {
			Worktree struct {
				CheckoutPath string `json:"checkout_path"`
			} `json:"worktree"`
		}
		if json.Unmarshal([]byte(ctx), &m) == nil && m.Worktree.CheckoutPath != "" {
			return m.Worktree.CheckoutPath
		}
	}
	if out, ok := run("", herdrBin(), "pane", "current", "--current"); ok {
		var m struct {
			Result struct {
				Pane struct {
					Cwd string `json:"cwd"`
				} `json:"pane"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(out), &m) == nil {
			return m.Result.Pane.Cwd
		}
	}
	return ""
}

// ---------- merge ----------
// gh's interactive flow picks squash/merge/rebase and shows the editable default commit message.
func readKey() string {
	cmd := commandWithEnv("sh", secureShellEnv(), "-c", `read -rsn1 k; printf %s "$k"`)
	cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	out, _ := cmd.Output()
	return string(out)
}

func readLine() string {
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line
}
func anyKey() {
	cmd := commandWithEnv("sh", secureShellEnv(), "-c", "read -rsn1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	_ = cmd.Run()
}

func mergeFlow() {
	cwd := ciCwd()
	st := prStatus(cwd)
	fmt.Print("\x1b[2J\x1b[H\x1b[?25l")
	if st.PR == nil {
		fmt.Print("\n\n  " + dim.Render("no open PR for this branch") + "\n\n  press any key\n")
		fmt.Print("\x1b[?25h")
		anyKey()
		return
	}
	p := st.PR
	fmt.Println()
	fmt.Println("  " + sect.Render("\uf407 Merge pull request"))
	fmt.Println()
	fmt.Printf("  %s  %s\n", bold.Render(fmt.Sprintf("#%d", p.Number)), dim.Render(sanitizeTerminalText(st.Branch)))
	if p.Title != "" {
		fmt.Println("  " + dim.Render(trunc(p.Title, 70)))
	}
	fmt.Println()
	fmt.Println("  " + cyan.Render("[enter]") + " merge         " + dim.Render("choose squash/merge/rebase + edit message next"))
	fmt.Println("  " + yellow.Render("[a]") + " admin bypass   " + dim.Render("skip required checks / branch protection"))
	fmt.Println("  " + dim.Render("[q] cancel"))
	fmt.Print("\n  " + mauve.Render("\u203a") + " ")

	k := readKey()
	fmt.Print("\x1b[?25h")
	if k == "q" || k == "\x1b" || k == "\x03" {
		fmt.Print("\n\n  cancelled\n")
		return
	}
	if k != "a" && k != "A" && k != "\r" && k != "\n" {
		fmt.Print("\n\n  cancelled (press enter to merge, or a for admin)\n")
		return
	}
	flags := []string{"pr", "merge"}
	if k == "a" || k == "A" {
		fmt.Print("\n  " + yellow.Render("Admin bypass skips required checks and branch protection."))
		fmt.Print("\n  Type " + bold.Render("ADMIN") + " to continue, or anything else to cancel: ")
		if readLine() != "ADMIN" {
			fmt.Print("\n\n  cancelled\n")
			return
		}
		flags = append(flags, "--admin")
	}
	// Re-check immediately before the remote mutation so a stale pane cannot
	// merge a PR that was closed or changed while the confirmation was shown.
	if latest := prStatus(cwd); latest.PR == nil || latest.PR.State != "OPEN" || latest.PR.Number != p.Number {
		fmt.Print("\n\n  " + red.Render("PR is no longer open; merge cancelled") + "\n")
		return
	}
	fmt.Print("\x1b[2J\x1b[H")
	cmd, ok := ghCommand(cwd, flags...)
	if !ok {
		fmt.Print("\n " + red.Render("repository remote is not a supported GitHub URL") + " — press any key\n")
		anyKey()
		return
	}
	cmd.Dir = cwd
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	// gh's exit code conflates the API merge with local branch cleanup, which fails in a
	// linked worktree (default branch checked out elsewhere). Trust the actual PR state.
	merged := false
	if st2 := prStatus(cwd); st2.PR != nil && st2.PR.State == "MERGED" {
		merged = true
	}
	if merged {
		msg := green.Render("\u2713 merged")
		if err != nil {
			msg += "  " + dim.Render("(worktree: local branch left in place)")
		}
		fmt.Print("\n " + msg + " \u2014 press any key\n")
	} else {
		fmt.Print("\n " + red.Render("\u2717 not merged / cancelled") + " \u2014 press any key\n")
	}
	anyKey()
}

// ---------- sidebar ----------
var glyph = map[string]string{"pass": "\uf42e", "fail": "\uf467", "merged": "\uf419", "open": "\uf407"}

var cspin = []rune("\u25d0\u25d3\u25d1\u25d2")

func meta(id string, extra ...string) {
	if !safeIdentifier(id) {
		return
	}
	args := append([]string{"workspace", "report-metadata", id, "--source", "herdr-gh-checks"}, extra...)
	// #nosec G204 -- metadata values are passed as arguments, never shell text.
	_ = commandWithEnv(herdrBin(), secureShellEnv(), args...).Run()
}

func sidebarLoop() {
	last := map[string]string{}
	push := func(id, state, g string) {
		if prev := last[id]; prev != "" && prev != state {
			meta(id, "--clear-token", "ci_"+prev)
		}
		if state != "" {
			meta(id, "--token", "ci_"+state+"="+g, "--ttl-ms", "90000")
		}
		last[id] = state
	}
	type ws struct{ id, state string }
	var list []ws
	tick := 0
	for {
		if tick%100 == 0 { // ~30s at 300ms; gentle on the GitHub API
			list = list[:0]
			if out, ok := run("", herdrBin(), "workspace", "list"); ok {
				var m struct {
					Result struct {
						Workspaces []struct {
							ID       string `json:"workspace_id"`
							Worktree *struct {
								CheckoutPath string `json:"checkout_path"`
							} `json:"worktree"`
						} `json:"workspaces"`
					} `json:"result"`
				}
				if json.Unmarshal([]byte(out), &m) == nil {
					for _, w := range m.Result.Workspaces {
						state := ""
						if w.Worktree != nil && w.Worktree.CheckoutPath != "" {
							state = stateOf(prStatus(w.Worktree.CheckoutPath))
						}
						list = append(list, ws{w.ID, state})
					}
				}
			}
		}
		spin := string(cspin[tick%len(cspin)])
		for _, w := range list {
			if w.state == "run" {
				push(w.id, "run", spin)
			} else if tick%100 == 0 {
				push(w.id, w.state, glyph[w.state])
			}
		}
		tick++
		time.Sleep(300 * time.Millisecond)
	}
}
