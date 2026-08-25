// Command punchcard is the command-line client.
//
// It is a client like any other: a bearer token and the public API. Anything
// awkward here is awkward for every client, and belongs fixed in the API.
//
//	punchcard login                        sign in through the browser
//	punchcard start <project> "what"       start a timer
//	punchcard stop                         stop the running timer
//	punchcard status                       what is running, and for how long
//	punchcard today                        the day's records with commit counts
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cobanov/punchcard/internal/cli"
)

// version is set at build time.
var version = "dev"

func main() { os.Exit(run()) }

func run() int {
	args := os.Args[1:]

	// Flags are position-free and few enough to parse by hand; the standard
	// flag package would force them before the subcommand, which reads badly
	// for `punchcard start caps "note" --json`.
	app := &cli.App{Out: os.Stdout, Err: os.Stderr, BaseURL: os.Getenv("PUNCHCARD_URL")}
	// --tool names which agent a hook line came from. It defaults to Claude
	// Code because that is what `hook install` wires up; anything else is
	// integrating through the queue file and can say so.
	hookTool := "claude-code"
	var rest []string
	for _, a := range args {
		switch {
		case a == "--json":
			app.JSON = true
		case strings.HasPrefix(a, "--tool="):
			hookTool = strings.TrimPrefix(a, "--tool=")
		case strings.HasPrefix(a, "--url="):
			app.BaseURL = strings.TrimPrefix(a, "--url=")
		default:
			rest = append(rest, a)
		}
	}
	args = rest

	path, err := cli.ConfigPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	app.ConfigPath = path

	if len(args) == 0 {
		usage()
		return 2
	}

	var cmdErr error
	switch args[0] {
	case "login":
		cmdErr = app.Login(app.BaseURL)
	case "logout":
		cmdErr = app.Logout()
	case "projects", "p":
		cmdErr = app.Projects()
	case "new", "n":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, `usage: punchcard new <name> [client] [rate-per-hour] [currency]`)
			return 2
		}
		cmdErr = app.NewProject(args[1], argAt(args, 2), argAt(args, 3), argAt(args, 4))
	case "link":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, `usage: punchcard link <project> <owner/repo>`)
			return 2
		}
		cmdErr = app.LinkRepo(args[1], args[2])
	case "start", "s":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, `usage: punchcard start <project> ["what you are doing"]`)
			return 2
		}
		cmdErr = app.Start(args[1], strings.Join(args[2:], " "))
	case "stop", "x":
		cmdErr = app.Stop()
	case "status", "st":
		cmdErr = app.Status()
	case "today", "t":
		cmdErr = app.Today(1)
	case "week", "w":
		cmdErr = app.Today(7)
	case "hook":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, `usage: punchcard hook <install|emit start|emit stop>`)
			return 2
		}
		switch args[1] {
		case "install":
			cmdErr = app.HookInstall()
		case "emit":
			if len(args) < 3 {
				fmt.Fprintln(os.Stderr, `usage: punchcard hook emit <start|stop> [--tool name]`)
				return 2
			}
			cmdErr = app.HookEmit(args[2], hookTool, os.Stdin)
		default:
			fmt.Fprintf(os.Stderr, "unknown hook command %q\n", args[1])
			return 2
		}
	case "sync":
		cmdErr = app.Sync()
	case "version", "--version", "-v":
		fmt.Println("punchcard", version)
		return 0
	case "help", "--help", "-h":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage()
		return 2
	}

	if cmdErr != nil {
		// ErrNotLoggedIn already says what to do; anything else is the server's
		// own wording, which is more useful than anything this layer could add.
		fmt.Fprintln(os.Stderr, cmdErr)
		if errors.Is(cmdErr, cli.ErrNotLoggedIn) {
			return 3
		}
		return 1
	}
	return 0
}

// argAt returns args[i] or "" — the optional positional arguments of `new`.
func argAt(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func usage() {
	fmt.Fprint(os.Stderr, `punchcard — time tracking for developers

Usage:
  punchcard <command> [args]

Commands:
  login                      Sign in through the browser
  logout                     Forget the stored token
  start <project> ["what"]   Start a timer; the project name may be a prefix
  stop                       Stop the running timer
  status                     What is running, and for how long
  today                      Today's records with their commits
  week                       The last seven days
  sync                       Send recorded agent turns to the server
  hook install               Record Claude Code turns as evidence
  projects                   List projects
  new <name> [client] [rate] [currency]
                             Create a project
  link <project> <owner/repo>
                             Link a repository (optional; it helps punchcard
                             guess which project unmatched commits belong to)
  version                    Print the version

Flags:
  --json                     Machine-readable output
  --url=<base>               Talk to another instance (or set PUNCHCARD_URL)

Examples:
  punchcard new capsarsiv Acme 2500 TRY
  punchcard start caps "yorum sistemi refactor"
  punchcard stop
  punchcard today
`)
}
