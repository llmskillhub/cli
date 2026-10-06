package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/llmskillhub/cli/internal/client"
	"github.com/llmskillhub/cli/internal/gitinfo"
	"github.com/llmskillhub/cli/evalpkg"
	skill "github.com/llmskillhub/cli/skillpkg"
)

// evalsEnabled reports whether this build will touch the second kind at all.
//
// Off unless asked for, matching the services: the API and ingest carry their
// own EVALS_ENABLED and refuse a kind=eval publish without it, so a CLI that
// offered the path anyway would send somebody into a 404 that reads as a fault
// in their package. One switch, three processes, all failing closed.
//
// Read from the environment rather than taken as a flag on the command,
// because it is not a per-invocation choice: it says whether this catalogue
// has evals yet. The spellings match EVALS_ENABLED for the same reason -- the
// person setting it is setting the same thing in three places.
func evalsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LLMSH_EVALS"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// isEvalDir reports whether a directory holds an eval rather than a skill.
//
// The manifest decides, not a flag somebody has to remember. A directory has a
// SKILL.md or it has a manifest.yaml; asking which one you are standing in,
// when it is visible from here, is a question with a wrong answer available
// for no reason.
func isEvalDir(dir string) bool {
	for _, name := range []string{evalpkg.ManifestFile, evalpkg.ManifestFileAlt} {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

// evalRefusal is what somebody gets after doing everything right on a build
// that has evals switched off. It names the switch and warns about the other
// two, because setting only this one produces a publish the server refuses.
func evalRefusal(dir string) error {
	return fmt.Errorf("%s looks like an eval — it has a %s — and evals are not switched on\n"+
		"  Set LLMSH_EVALS=1. The server needs EVALS_ENABLED too, or it will refuse the publish",
		dir, evalpkg.ManifestFile)
}

/*
packEval builds an eval's archive and checks it by reading it back.

Packed first and validated from the archive, rather than walking the directory
with a second set of rules. The server does exactly this -- it re-unpacks what
was stored and runs the validator on that -- and a CLI that checked the
directory instead would be a second implementation of "is this a valid eval",
free to disagree with the one that decides. Somebody would then see a green
check here and a refusal there, with nothing to tell them which was right.

It also catches the packaging failures that only exist in the archive: a file
the packer strips, a path it rewrites, a symlink it refuses to follow. Those
are invisible on disk, because on disk everything is still there.
*/
func packEval(dir string) (*evalpkg.Package, *skill.Result, []byte, error) {
	// The root name has to come from the manifest, and the manifest has to be
	// read before packing can start, so this reads it twice -- once off disk
	// for the name, once out of the archive for everything else. The second
	// read is the one that counts.
	raw, err := readManifest(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	man, res := evalpkg.ParseManifest(raw)
	if man == nil {
		return nil, res, nil, fmt.Errorf("%s could not be read as an eval manifest", dir)
	}

	var buf bytes.Buffer
	// No KeepRoots. StripRootDirs holds only evals/, and this convention keeps
	// the dataset at the package root with rubrics/ and metrics/ beside it --
	// so nothing load-bearing is stripped. See EvalShape.
	packRes, _, err := skill.Pack(dir, &buf, skill.PackOptions{RootName: man.Slug()})
	if err != nil {
		return nil, packRes, nil, err
	}
	if errs, _ := report(packRes); errs > 0 {
		return nil, packRes, nil, fmt.Errorf("packaging refused")
	}

	archive := buf.Bytes()
	pkg, unpackRes, err := evalpkg.Unpack(context.Background(),
		bytes.NewReader(archive), int64(len(archive)), "")
	if err != nil {
		return nil, unpackRes, archive, err
	}
	return pkg, unpackRes, archive, nil
}

// readManifest returns the manifest's bytes, accepting either spelling.
func readManifest(dir string) ([]byte, error) {
	for _, name := range []string{evalpkg.ManifestFile, evalpkg.ManifestFileAlt} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("%s has no %s", dir, evalpkg.ManifestFile)
}

// describeEval prints what the package turned out to contain.
//
// Counts rather than a verdict. "2 samples, 1 rubric" is checkable against what
// the author believes they wrote; "looks fine" is not.
func describeEval(pkg *evalpkg.Package) {
	fmt.Printf("  %d sample(s)", pkg.Samples())
	if d := pkg.Dataset; d != nil {
		if d.WithTarget > 0 {
			fmt.Printf(", %d with an expected answer", d.WithTarget)
		}
		if d.WithContext > 0 {
			fmt.Printf(", %d with context", d.WithContext)
		}
	}
	if n := len(pkg.Rubrics); n > 0 {
		fmt.Printf(", %d rubric(s)", n)
	}
	fmt.Println()
	// Said separately and plainly: these are the fields that run something,
	// and a reviewer is going to ask about them.
	if pkg.Executes() {
		fmt.Printf("  this eval executes: some samples carry a setup script, a sandbox or files\n")
	}
	if d := pkg.Dataset; d != nil && len(d.RemoteFiles) > 0 {
		// Listed, not counted. A reviewer's next question is which URL, and a
		// number sends them back to the file to find out.
		fmt.Printf("  %d sample(s) fetch a file from a URL — content this package does not contain:\n",
			len(d.RemoteFiles))
		for _, u := range d.RemoteFiles {
			fmt.Printf("    %s\n", u)
		}
	}
}

// cmdEval is the `llmsh eval` family. Only `get` so far; publishing an eval
// goes through `llmsh publish`, which recognises the directory on its own.
func cmdEval(args []string) error {
	if !evalsEnabled() {
		return fmt.Errorf("evals are not switched on\n" +
			"  Set LLMSH_EVALS=1 if this catalogue accepts them")
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: llmsh eval get owner/name[@version] [--dir ./evals]")
	}
	switch args[0] {
	case "get":
		return cmdEvalGet(args[1:])
	default:
		return fmt.Errorf("llmsh eval: unknown subcommand %q\n  Only `get` so far", args[0])
	}
}

/*
cmdEvalGet downloads an eval and unpacks it.

Deliberately not `llmsh install`. Installing means putting a skill where an
agent will read it, and the agent directories are for instructions -- dropping
a dataset into one would have an agent load two hundred rows of test cases as
though they were guidance. An eval is fetched to look at, to run, or to compare
against; the default destination is a directory under the working one, and
nothing is written to any agent's configuration.
*/
func cmdEvalGet(args []string) error {
	fs := flag.NewFlagSet("eval get", flag.ExitOnError)
	dest := fs.String("dir", "evals", "where to unpack")
	force := fs.Bool("force", false, "replace an existing directory")
	rest := parseArgs(fs, args)
	if len(rest) == 0 {
		return fmt.Errorf("usage: llmsh eval get owner/name[@version] [--dir ./evals]")
	}

	owner, name, version := parseRef(rest[0])
	if owner == "" {
		return fmt.Errorf("which publisher? use owner/name, e.g. llmsh eval get admin/%s", name)
	}
	c, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	archive, want, err := c.Download(owner, name, version)
	if err != nil {
		return err
	}
	return installEval(owner, name, archive, want, *dest, *force)
}

/*
installEval unpacks an eval where evals belong, after checking it is one and is
the one the registry described.

Shared by `llmsh eval get` and by `llmsh install`, which now sends an eval here
when the server says that is what it downloaded. One function, so the two cannot
drift on where an eval lands or on what it must pass first.

It verifies the digest, which eval get did not. It printed the digest the
registry declared and then unpacked whatever had arrived, so a download altered
anywhere between the object store and the disk would have been written out and
reported with a digest it did not have. install has always refused that, and an
eval is data a grader trusts, so it gets the same check.
*/
func installEval(owner, name string, archive []byte, want, dest string, force bool) error {
	// Read before it is written anywhere, so a package that is not an eval is
	// refused rather than unpacked into a directory named evals.
	pkg, res, err := evalpkg.Unpack(context.Background(),
		bytes.NewReader(archive), int64(len(archive)), "")
	if err != nil {
		return err
	}
	if errs, _ := report(res); errs > 0 {
		return fmt.Errorf("%s/%s is not a valid eval package", owner, name)
	}
	if want != "" && pkg.Digest != want {
		return fmt.Errorf("digest mismatch: the registry says %s, these bytes are %s\n"+
			"  Nothing was written. Try again; if it repeats, the package is not what was published",
			skill.ShortDigest(want), skill.ShortDigest(pkg.Digest))
	}

	into := filepath.Join(dest, name)
	if _, err := os.Stat(into); err == nil && !force {
		return fmt.Errorf("%s already exists\n  Pass --force to replace it", into)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if force {
		if err := os.RemoveAll(into); err != nil {
			return err
		}
	}
	// The same call again, this time with a destination: Unpack writes the
	// files when it is given one. Read first, written second, so a package
	// that fails the checks above never reaches the disk.
	if _, _, err := evalpkg.Unpack(context.Background(),
		bytes.NewReader(archive), int64(len(archive)), into); err != nil {
		return err
	}

	fmt.Printf("%s/%s@%s → %s\n", owner, name, pkg.Eval.Version, into)
	describeEval(pkg)
	fmt.Printf("  %d files · digest %s verified\n", len(pkg.Files), skill.ShortDigest(pkg.Digest))
	return nil
}

/*
publishEval is `llmsh publish` for a directory that turned out to be an eval.

The same shape as the skill path on purpose -- validate, check the working
tree, pack, send -- because the two differ in what makes a package valid and in
nothing else. The git check in particular is not relaxed: a dataset that exists
in no commit is worse than a skill that does, since nobody can tell by reading
it whether a row was edited.
*/
func publishEval(c *client.Client, dir, version string, allowDirty, dryRun, private bool) error {
	pkg, res, archive, err := packEval(dir)
	if err != nil {
		if res != nil {
			report(res)
		}
		return err
	}
	if errs, _ := report(res); errs > 0 {
		return fmt.Errorf("%d error(s) — fix these first", errs)
	}

	g := gitinfo.Read(dir)
	if g.Repo && g.Dirty && !allowDirty {
		fmt.Fprintf(os.Stderr, "Uncommitted changes in %s:\n", dir)
		for _, p := range g.DirtyPaths {
			fmt.Fprintf(os.Stderr, "  %s\n", p)
		}
		return fmt.Errorf("publishing now would ship rows that exist in no commit\n" +
			"  Commit them, or pass --allow-dirty if that is what you meant")
	}

	ver := version
	if ver == "" {
		ver = pkg.Eval.Version
	}
	if ver == "" {
		return fmt.Errorf("no version: set `version:` in %s, or pass --version", evalpkg.ManifestFile)
	}

	slug := pkg.Eval.Slug()
	fmt.Printf("%s@%s — %d files, %s\n", slug, ver, len(pkg.Files), humanBytes(int64(len(archive))))
	describeEval(pkg)
	if g.Repo {
		fmt.Printf("  from %s on %s\n", g.Short(), g.Branch)
	}
	if dryRun {
		fmt.Println("  --dry-run: checking on the server, storing nothing")
	}

	out, err := c.Publish(client.PublishOptions{
		Slug: slug, Version: ver, Kind: "eval", Private: private, DryRun: dryRun,
	}, archive)
	if err != nil {
		return err
	}
	if errs, warns := reportUpload(out); errs > 0 {
		return fmt.Errorf("the server refused this package")
	} else if warns > 0 {
		fmt.Printf("  %d warning(s) — published anyway\n", warns)
	}

	switch {
	case dryRun:
		fmt.Printf("\n  would publish %s@%s\n  Nothing was stored.\n", slug, ver)
	case out.OK && private:
		fmt.Printf("\n  %s@%s is in your space · digest %s\n",
			out.Slug, out.Version, out.ShortDigest)
		fmt.Printf("  Not reviewed and not listed. Yours now; share it from the website.\n")
	case out.OK:
		fmt.Printf("\n  %s@%s submitted · digest %s · %s\n",
			out.Slug, out.Version, out.ShortDigest, out.ReviewState)
		fmt.Printf("  A person reads every version before it appears in the catalogue.\n")
	}
	return nil
}
