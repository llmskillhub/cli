// Command llmsh is the LLM SkillHub command line.
//
// It does not replace git and does not try to. A skill lives in a repository,
// and git already gives you diffing, rollback and history on your own machine --
// far better than a registry ever could. What was missing is that the two knew
// nothing about each other, so this reads the repository when publishing:
// refusing to ship uncommitted work by accident, and recording the commit a
// version came from.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

const usage = `llmsh — publish and manage LLM SkillHub skills

Usage:
  llmsh init [dir]               scaffold a new skill, asking what the validator needs
  llmsh clone <owner>/<skill>    fetch a skill's source to work on
  llmsh pull                     update a working copy to the newest version
  llmsh checkout <version>       switch a working copy to a specific version

  llmsh validate [dir]           check a skill folder, storing nothing
  llmsh diff [dir]               what you would publish, against what is published
  llmsh publish [dir]            package the folder and submit it for review
  llmsh push [dir]               the same thing, if that is the word you reach for
  llmsh status <skill>           versions, review state and what a reviewer asked for
  llmsh install <owner>/<name>   download and verify; a skill goes where an agent loads it,
                                 an eval into ./evals

  llmsh login / logout / whoami  the stored access token
  llmsh mcp                      serve the catalogue to an AI over MCP
`

// evalUsage is shown only when evals are switched on.
//
// A build with them off says nothing about them, because every line here is a
// promise that the command works -- and against a catalogue that has not
// enabled evals, it does not. `llmsh publish` needs no entry of its own: it
// recognises an eval directory by its manifest.yaml and says so if the switch
// is off.
const evalUsage = `
Evals (LLMSH_EVALS=1):
  llmsh publish [dir]            a folder with a manifest.yaml publishes as an eval
  llmsh eval get <owner>/<name>  the same as install, for an eval (kept for scripts that use it)
  llmsh eval get --dir ./evals   where to unpack (default ./evals)
`

const usageTail = `

Flags:
  llmsh publish --version 1.2.0  override the version in SKILL.md
  llmsh publish --allow-dirty    publish even with uncommitted changes
  llmsh publish --dry-run        validate on the server without storing anything
  llmsh publish --private        into your space, not the catalogue; no review
  llmsh install name@1.2.0       install an exact version instead of the latest
  llmsh install name --for cursor  install for another agent: claude (default),
                                 cursor, codex, gemini, opencode
  llmsh install --dir ./skills   unpack somewhere specific

Environment:
  LLMSH_API       default https://api.llmskillhub.com
  LLMSH_INGEST    default https://api.llmskillhub.com
  LLMSH_TOKEN     a token, for CI, instead of the stored one
  LLMSH_EVALS     1 to work with evals as well as skills
`

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}
	args := os.Args[2:]
	var err error

	switch os.Args[1] {
	case "login":
		err = cmdLogin(args)
	case "logout":
		err = cmdLogout()
	case "whoami":
		err = cmdWhoami()
	case "validate":
		err = cmdValidate(args)
	case "diff":
		err = cmdDiff(args)
	case "publish":
		err = cmdPublish(args)
	case "status":
		err = cmdStatus(args)
	case "install":
		err = cmdInstall(args)
	case "init":
		err = cmdInit(args)
	case "clone":
		err = cmdClone(args)
	case "pull":
		err = cmdPull(args)
	case "checkout":
		err = cmdCheckout(args)
	case "push":
		// An alias, not a second implementation. git push sends commits; this
		// submits a version for review, and calling it push only because the
		// hand reaches for it is fine as long as it is the same thing.
		err = cmdPublish(args)
	case "eval":
		err = cmdEval(args)
	case "mcp":
		err = cmdMCP(args)
	case "version", "--version", "-v":
		fmt.Println("llmsh " + buildVersion())
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "llmsh: unknown command %q\n\n%s%s", os.Args[1], usage, usageTail)
		os.Exit(2)
	}

	if err != nil {
		// Multi-line errors carry a hint on the second line; both belong on
		// stderr so a script's stdout stays parseable.
		fmt.Fprintln(os.Stderr, "llmsh: "+strings.TrimSpace(err.Error()))
		os.Exit(1)
	}
}

// version is set at build time: go build -ldflags "-X main.version=1.0.0"
var version = ""

// buildVersion is what `llmsh version` prints.
//
// The release workflow stamps it with ldflags. `go install ...@v0.1.0` cannot
// -- there is nowhere to pass a flag -- so it falls back to the module version
// the toolchain recorded, which is exactly the tag that was installed. Without
// this, every Go install reports "dev" and no bug report from one is traceable
// to a build.
func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return strings.TrimPrefix(v, "v")
		}
	}
	return "dev"
}

// printUsage writes the help, with the eval section only when it is usable.
func printUsage() {
	fmt.Print(usage)
	if evalsEnabled() {
		fmt.Print(evalUsage)
	}
	fmt.Print(usageTail)
}
