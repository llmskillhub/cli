package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/llmskillhub/cli/internal/workdir"
)

// TestSwapInReplacesContentsAndKeepsTheMarker covers the update path used by
// pull and checkout on a directory that already exists.
func TestSwapInReplacesContentsAndKeepsTheMarker(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "skill")
	if err := os.MkdirAll(filepath.Join(dest, workdir.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dest, workdir.Dir, "origin.json"), `{"slug":"demo"}`)
	write(t, filepath.Join(dest, "OLD.md"), "old")
	write(t, filepath.Join(dest, "keep", "nested.md"), "old nested")

	tmp := filepath.Join(root, ".aq-fetch-x")
	if err := os.MkdirAll(filepath.Join(tmp, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(tmp, "SKILL.md"), "new")
	write(t, filepath.Join(tmp, "references", "a.md"), "new ref")

	if err := swapIn(tmp, dest); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dest, "OLD.md")); !os.IsNotExist(err) {
		t.Error("a file from the previous version survived the swap")
	}
	if _, err := os.Stat(filepath.Join(dest, "keep")); !os.IsNotExist(err) {
		t.Error("a directory from the previous version survived the swap")
	}
	for _, want := range []string{"SKILL.md", "references/a.md"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(want))); err != nil {
			t.Errorf("%s did not arrive: %v", want, err)
		}
	}
	// The marker is how the directory knows what it is. Losing it mid-update
	// would turn a working copy into an anonymous pile of files.
	if _, err := os.Stat(filepath.Join(dest, workdir.Dir, "origin.json")); err != nil {
		t.Errorf("the working copy marker was destroyed: %v", err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("the temporary directory was left behind")
	}
}

// TestSwapInRefusesToEatItsOwnSource is the data-loss bug.
//
// fetchInto took filepath.Dir(dest) for the temporary directory, and
// filepath.Dir(".") is "." -- so with `llmsh pull` in a working copy the temporary
// directory was created INSIDE the directory about to be emptied, and clearing
// the destination deleted the fetched files before they could be moved in. The
// directory ended up empty and the version was gone.
func TestSwapInRefusesToEatItsOwnSource(t *testing.T) {
	dest := t.TempDir()
	write(t, filepath.Join(dest, "SKILL.md"), "original")

	// A temp directory nested inside the destination: the shape that lost data.
	tmp := filepath.Join(dest, ".aq-fetch-inside")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(tmp, "SKILL.md"), "replacement")

	err := swapIn(tmp, dest)
	if err == nil {
		// If it ever succeeds it must at least not have destroyed both sides.
		if _, statErr := os.Stat(filepath.Join(dest, "SKILL.md")); statErr != nil {
			t.Fatal("swapIn emptied the destination and lost the replacement")
		}
		return
	}
	// Failing is fine. Silently emptying the directory is not.
	entries, readErr := os.ReadDir(dest)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) == 0 {
		t.Fatal("swapIn emptied the destination before failing")
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
