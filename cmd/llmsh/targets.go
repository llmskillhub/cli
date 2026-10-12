package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Where a skill goes depends on which agent is meant to read it.
//
// A skill is a directory -- SKILL.md plus whatever it references -- and the
// agents disagree about where such a directory lives and, more awkwardly,
// whether it is a directory at all. Claude Code, Cursor and opencode read a tree
// of them. Codex and Gemini CLI read one project context file each.
//
// So installing for a non-Claude agent is two steps rather than one: the skill
// itself is unpacked somewhere stable, and then that agent is told about it in
// the form it actually reads. Both steps are here, in a table, because the
// alternative is four branches of an if-statement in the middle of a download.
//
// Nothing outside this file knows any agent's name.

// target is one agent's idea of where instructions live.
type target struct {
	// Name is what -for takes.
	Name string
	// Dir is where the unpacked skill directory goes, relative to the project.
	Dir string
	// Home is where it goes when there is no project directory, relative to the
	// home directory. Empty means Dir works in both places, which is the common
	// case -- .claude/skills is ~/.claude/skills -- and opencode is why the
	// field exists: its project directory is .opencode/skills and its personal
	// one is .config/opencode/skills, so joining home to Dir would write to
	// ~/.opencode/skills, which nothing reads.
	Home string
	// Context is a single file that agent reads, empty when it reads Dir
	// directly. When set, a delimited block naming the skill is kept in it.
	Context string
	// Note is printed after a successful install, because "where did it go"
	// and "will my agent read it" are different questions.
	Note string
}

var targets = map[string]target{
	"claude": {
		Name: "claude",
		Dir:  filepath.Join(".claude", "skills"),
		Note: "Claude Code loads this on its own, by the description in SKILL.md.",
	},
	// opencode reads skill folders natively, the way Claude Code does, so it
	// needs no rule file and no context block. It also reads .claude/skills and
	// .agents/skills, which means a skill installed for any of the other agents
	// is already visible to it -- this target exists so that somebody who uses
	// opencode alone gets its own directory rather than a competitor's.
	"opencode": {
		Name: "opencode",
		Dir:  filepath.Join(".opencode", "skills"),
		Home: filepath.Join(".config", "opencode", "skills"),
		Note: "opencode loads this on its own, by the description in SKILL.md.",
	},
	// Cursor loads skill folders natively now -- .cursor/skills in a project,
	// ~/.cursor/skills otherwise, by the description in SKILL.md
	// (cursor.com/docs/context/skills). It used to be told through a .mdc rule
	// per skill, which now makes Cursor see the same instructions twice.
	"cursor": {
		Name: "cursor",
		Dir:  filepath.Join(".cursor", "skills"),
		Note: "Cursor loads this on its own, by the description in SKILL.md.",
	},
	"codex": {
		Name:    "codex",
		Dir:     filepath.Join(".agents", "skills"),
		Context: "AGENTS.md",
		Note:    "Codex reads AGENTS.md at the repository root.",
	},
	"gemini": {
		Name:    "gemini",
		Dir:     filepath.Join(".agents", "skills"),
		Context: "GEMINI.md",
		Note:    "Gemini CLI reads GEMINI.md at the repository root.",
	},
}

// targetNames lists what -for accepts, in a fixed order so help text and error
// messages do not reshuffle between runs.
func targetNames() []string {
	out := make([]string, 0, len(targets))
	for k := range targets {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func lookupTarget(name string) (target, error) {
	t, ok := targets[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return target{}, fmt.Errorf("unknown agent %q; -for takes one of: %s",
			name, strings.Join(targetNames(), ", "))
	}
	return t, nil
}

// markers delimit the block this tool owns inside a file it does not own.
//
// A project's AGENTS.md is the author's file, and an installer that rewrites it
// wholesale destroys work. Everything between these two lines is ours to
// replace on reinstall and to remove on uninstall; everything outside them is
// never touched, and the marker names the skill so several can coexist.
func markers(owner, name string) (string, string) {
	id := owner + "/" + name
	return "<!-- llmskillhub:" + id + " -->", "<!-- /llmskillhub:" + id + " -->"
}

// writeContextBlock puts a skill's instructions into an agent's context file,
// replacing its own previous block and leaving every other line alone.
//
// The file is created when absent and appended to when present. The block holds
// the skill's own text rather than a path, because a context file is read as
// prose and a path in it is a line an agent may or may not choose to follow.
func writeContextBlock(path, owner, name, body, dir string) error {
	start, end := markers(owner, name)
	block := strings.Join([]string{
		start,
		fmt.Sprintf("<!-- installed by llmsh from llmskillhub.com; edit the skill, not this block -->"),
		body,
		fmt.Sprintf("\nThe files for this skill are in %s.", dir),
		end,
	}, "\n")

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(existing)

	if i := strings.Index(text, start); i >= 0 {
		// Replace in place, so a reinstall does not stack up copies. The end
		// marker is searched for after the start one: a truncated block left by
		// a crashed run would otherwise swallow the rest of the file.
		rest := text[i:]
		if j := strings.Index(rest, end); j >= 0 {
			text = text[:i] + block + rest[j+len(end):]
			return os.WriteFile(path, []byte(text), 0o644)
		}
		// A start with no end. Refused rather than guessed at: the rest of the
		// file might be the author's and might be a half-written block, and
		// only they know which.
		return fmt.Errorf("%s has an unterminated llmskillhub block for %s/%s\n"+
			"  Remove it by hand, then install again", path, owner, name)
	}

	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if text != "" {
		text += "\n"
	}
	return os.WriteFile(path, []byte(text+block+"\n"), 0o644)
}
