// Package workdir records where a working copy came from.
//
// Without it, a directory of files is just a directory: the CLI cannot tell a
// clone of someone else's skill from your own work in progress, and cannot tell
// an untouched copy from one you have edited. That is the difference between
// pull being safe and pull being a way to lose an afternoon.
package workdir

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	skill "github.com/llmskillhub/cli/skillpkg"
)

// Dir is the marker directory. Stripped when packing, so it never ships.
const Dir = ".llmsh"

// Origin is what a working copy remembers.
type Origin struct {
	API   string `json:"api"`
	Owner string `json:"owner"`
	Slug  string `json:"slug"`
	// Version and Digest are what was checked out. Digest is what makes "have
	// you changed anything" answerable without keeping a second copy of the
	// files: the tree digest of the directory either still matches or does not.
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

func path(dir string) string { return filepath.Join(dir, Dir, "origin.json") }

// Read returns the origin, or nil when the directory is not a working copy.
func Read(dir string) (*Origin, error) {
	b, err := os.ReadFile(path(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var o Origin
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

func Write(dir string, o *Origin) error {
	if err := os.MkdirAll(filepath.Join(dir, Dir), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path(dir), append(b, '\n'), 0o644)
}

// Modified reports whether the files differ from what was checked out.
//
// Compared by tree digest rather than by timestamps: a file rewritten with
// identical content is not a change, and a file touched by a formatter that
// changed nothing should not stop a pull.
func Modified(dir string, o *Origin) (bool, string, error) {
	if o == nil || o.Digest == "" {
		return false, "", nil
	}
	now, _, err := skill.PackDigest(dir)
	if err != nil {
		return false, "", err
	}
	return now != o.Digest, now, nil
}
