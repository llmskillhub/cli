package main

import (
	"testing"

	"github.com/llmskillhub/cli/internal/workdir"
)

// writeOrigin makes dir look like a working copy of owner/slug.
func writeOrigin(t *testing.T, dir, owner, slug string) {
	t.Helper()
	if err := workdir.Write(dir, &workdir.Origin{
		API: "http://localhost", Owner: owner, Slug: slug, Version: "1.0.0", Digest: "sha256:x",
	}); err != nil {
		t.Fatal(err)
	}
}

/*
Where a publish goes when the command line did not say.

The case this exists for cannot be settled any other way. Somebody with write on
three people's "pdf-tools" has the right to publish to all three, so permissions
cannot disambiguate; the name is identical in every case, so the name cannot
either. Only the directory knows, because the clone wrote it down -- and until
it was read, a bare publish from any of those three working copies went to a
fourth skill of the publisher's own, silently, with a success message saying the
work was theirs now.
*/
func TestTargetFor(t *testing.T) {
	t.Run("a working copy publishes back to where it came from", func(t *testing.T) {
		d := t.TempDir()
		writeOrigin(t, d, "alice", "pdf-tools")
		got, err := targetFor(d, "pdf-tools", "", false)
		if err != nil || got.Owner != "alice" {
			t.Errorf("owner = %q (err %v), want alice", got.Owner, err)
		}
	})

	t.Run("three copies of the same name keep their own destinations", func(t *testing.T) {
		for _, owner := range []string{"alice", "carol", "dave"} {
			d := t.TempDir()
			writeOrigin(t, d, owner, "pdf-tools")
			got, _ := targetFor(d, "pdf-tools", "", false)
			if got.Owner != owner {
				t.Errorf("copy of %s/pdf-tools resolved to %q", owner, got.Owner)
			}
		}
	})

	t.Run("an explicit name wins over the working copy", func(t *testing.T) {
		d := t.TempDir()
		writeOrigin(t, d, "alice", "pdf-tools")
		got, _ := targetFor(d, "pdf-tools", "carol", false)
		if got.Owner != "carol" {
			t.Errorf("owner = %q, want carol — saying where you mean must win", got.Owner)
		}
	})

	t.Run("--fork ignores the working copy", func(t *testing.T) {
		d := t.TempDir()
		writeOrigin(t, d, "alice", "pdf-tools")
		got, _ := targetFor(d, "pdf-tools", "", true)
		if got.Owner != "" {
			t.Errorf("owner = %q, want empty — a fork is published under your own name", got.Owner)
		}
	})

	t.Run("no origin means your own namespace, with no ceremony", func(t *testing.T) {
		// The ordinary case: a skill somebody wrote. Unlike git's no-upstream
		// state this is not ambiguous, so it must not ask anybody anything.
		got, err := targetFor(t.TempDir(), "pdf-tools", "", false)
		if err != nil || got.Owner != "" {
			t.Errorf("owner = %q (err %v), want empty", got.Owner, err)
		}
	})

	t.Run("a renamed package stops following the origin", func(t *testing.T) {
		// Renaming is how a fork is made by hand, and following the origin then
		// would publish into somebody's space under a name they never chose.
		d := t.TempDir()
		writeOrigin(t, d, "alice", "pdf-tools")
		got, _ := targetFor(d, "pdf-tools-mine", "", false)
		if got.Owner != "" {
			t.Errorf("owner = %q, want empty for a renamed package", got.Owner)
		}
		if was := renamedFrom(d, "pdf-tools-mine"); was != "alice/pdf-tools" {
			t.Errorf("renamedFrom = %q, want alice/pdf-tools so it can be explained", was)
		}
	})
}
