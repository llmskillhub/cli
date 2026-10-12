package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project's AGENTS.md is the author's file. These tests exist because the
// failure mode is silent and expensive: an installer that rewrites the file
// destroys instructions somebody wrote by hand, and they find out when their
// agent stops following them.

func TestAnExistingContextFileKeepsItsOwnContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	theirs := "# My project\n\nRun tests with `make test`.\nNever commit to main.\n"
	if err := os.WriteFile(path, []byte(theirs), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeContextBlock(path, "alice", "pdf-tools", "Do the thing.", ".agents/skills/pdf-tools"); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, "Never commit to main.") {
		t.Fatalf("the author's instructions were lost:\n%s", got)
	}
	if !strings.Contains(got, "Do the thing.") {
		t.Errorf("the skill was not added:\n%s", got)
	}
}

func TestReinstallingReplacesItsOwnBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")

	for i, body := range []string{"First version.", "Second version."} {
		if err := writeContextBlock(path, "alice", "pdf-tools", body, "d"); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	got := read(t, path)
	if n := strings.Count(got, "<!-- llmskillhub:alice/pdf-tools -->"); n != 1 {
		t.Errorf("%d blocks for one skill, want 1:\n%s", n, got)
	}
	if strings.Contains(got, "First version.") {
		t.Error("the old body is still there; a reinstall should replace it")
	}
	if !strings.Contains(got, "Second version.") {
		t.Error("the new body is missing")
	}
}

func TestTwoSkillsCoexist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := writeContextBlock(path, "alice", "pdf-tools", "A.", "d"); err != nil {
		t.Fatal(err)
	}
	if err := writeContextBlock(path, "bob", "docx-tools", "B.", "d"); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	for _, want := range []string{"alice/pdf-tools", "bob/docx-tools", "A.", "B."} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing:\n%s", want, got)
		}
	}
}

// A block whose end marker is gone is a file half-written by something that
// crashed. Refused rather than repaired: the rest of the file might be the
// author's and might be the remains of the block, and guessing wrong deletes
// their work.
func TestAnUnterminatedBlockIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	broken := "<!-- llmskillhub:alice/pdf-tools -->\nhalf a block\n\n# Notes the author wrote after\n"
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeContextBlock(path, "alice", "pdf-tools", "New.", "d"); err == nil {
		t.Fatal("an unterminated block was overwritten rather than refused")
	}
	if got := read(t, path); !strings.Contains(got, "Notes the author wrote after") {
		t.Errorf("the file was modified despite the refusal:\n%s", got)
	}
}

func TestEveryTargetIsUsable(t *testing.T) {
	for _, name := range targetNames() {
		tg, err := lookupTarget(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if tg.Dir == "" {
			t.Errorf("%s has nowhere to put a skill", name)
		}
		// An agent either reads a folder of skills itself or has to be told
		// about one in a file it does read. A target that does neither would
		// install successfully and be noticed by nothing.
		//
		// Named rather than inferred: adding an agent to the table is the moment
		// to decide which of the two it is, and a new name that is silently
		// neither is the bug this catches.
		readsFolders := name == "claude" || name == "opencode" || name == "cursor"
		if !readsFolders && tg.Context == "" {
			t.Errorf("%s is told about a skill by nothing", name)
		}
		if readsFolders && tg.Context != "" {
			t.Errorf("%s reads folders, so it needs no context file", name)
		}
	}
	if _, err := lookupTarget("emacs"); err == nil {
		t.Error("an unknown agent should be refused, with the list of known ones")
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// opencode's personal skills directory is not its project one with a home
// prefix, which every other target here happens to be. Getting that wrong
// writes to ~/.opencode/skills, a path nothing reads, and the install reports
// success.
func TestATargetWhoseGlobalPathDiffersSaysSo(t *testing.T) {
	oc, err := lookupTarget("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if oc.Home == "" {
		t.Fatal("opencode needs an explicit Home; ~/.opencode/skills is not where it looks")
	}
	if oc.Home == oc.Dir {
		t.Errorf("Home %q equals Dir; then it did not need to be set", oc.Home)
	}
	if !strings.Contains(oc.Home, "opencode") || !strings.Contains(oc.Home, "skills") {
		t.Errorf("opencode Home is %q", oc.Home)
	}
}

// A rule an older llmsh wrote is removed on reinstall; one somebody wrote by
// hand under the same name is left alone.
func TestReinstallingForCursorRemovesOnlyOurOldRule(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(".cursor/rules", 0o755); err != nil {
		t.Fatal(err)
	}
	ours := "---\ndescription: x\n---\n\n<!-- alice/pdf-tools, installed by llmsh from llmskillhub.com -->\n"
	os.WriteFile(".cursor/rules/pdf-tools.mdc", []byte(ours), 0o644)
	os.WriteFile(".cursor/rules/mine.mdc", []byte("my own rule"), 0o644)

	if _, ok := removeOldCursorRule("pdf-tools", ".cursor/skills/pdf-tools"); !ok {
		t.Error("llmsh's own old rule was not removed")
	}
	if _, err := os.Stat(".cursor/rules/pdf-tools.mdc"); !os.IsNotExist(err) {
		t.Error("the old rule is still there")
	}
	if _, ok := removeOldCursorRule("mine", ".cursor/skills/mine"); ok {
		t.Error("a hand-written rule was removed")
	}
}

// The rule beside a skill installed under ~/.cursor is found even when the
// reinstall runs from another directory -- the case that left one behind.
func TestTheOldRuleBesideTheSkillIsFoundFromAnywhere(t *testing.T) {
	home := t.TempDir()
	t.Chdir(t.TempDir())
	rules := filepath.Join(home, ".cursor", "rules")
	if err := os.MkdirAll(rules, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(rules, "brag-slim.mdc")
	os.WriteFile(old, []byte("<!-- x/brag-slim, installed by llmsh from llmskillhub.com -->"), 0o644)

	into := filepath.Join(home, ".cursor", "skills", "brag-slim")
	if path, ok := removeOldCursorRule("brag-slim", into); !ok || path != old {
		t.Fatalf("removed %q, %v; want %q", path, ok, old)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the rule beside the skill is still there")
	}
}
