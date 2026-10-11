package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/llmskillhub/cli/internal/client"
	"github.com/llmskillhub/cli/internal/gitinfo"
	"github.com/llmskillhub/cli/evalpkg"
	skill "github.com/llmskillhub/cli/skillpkg"
)

// evalsEnabled reports whether this build will touch the second kind at all.
//
// On, now that llmskillhub.com has launched evals (EVALS_ENABLED on the API
// and ingest). It was off until then, so a CLI could not send somebody into a
// 404 that read as a fault in their package; that reason is gone for the
// default catalogue. LLMSH_EVALS=0 (or false, no, off) still turns it off,
// for a self-hosted catalogue that has not switched evals on.
//
// Read from the environment rather than taken as a flag on the command,
// because it is not a per-invocation choice: it says whether this catalogue
// has evals. The spellings match EVALS_ENABLED.
func evalsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LLMSH_EVALS"))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
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
		return fmt.Errorf("usage:\n" +
			"  llmsh eval get    owner/name[@version] [--dir ./evals]\n" +
			"  llmsh eval attach owner/name --to owner/skill [owner/skill ...]\n" +
			"  llmsh eval detach owner/name --from owner/skill [owner/skill ...]\n" +
			"  llmsh eval list   --skill owner/skill\n" +
			"  llmsh eval run    owner/name --skill owner/skill [--wait]\n" +
			"  llmsh eval runs   --skill owner/skill")
	}
	switch args[0] {
	case "get":
		return cmdEvalGet(args[1:])
	case "attach":
		return cmdEvalAttach(args[1:], true)
	case "detach":
		return cmdEvalAttach(args[1:], false)
	case "list":
		return cmdEvalList(args[1:])
	case "run":
		return cmdEvalRun(args[1:])
	case "runs":
		return cmdEvalRuns(args[1:])
	default:
		return fmt.Errorf("llmsh eval: unknown subcommand %q\n  Try get, attach, detach, list, run or runs", args[0])
	}
}

// cmdEvalRun queues a run of one eval against one of the caller's skills, and
// with --wait polls until it is terminal and prints the score.
func cmdEvalRun(args []string) error {
	fs := flag.NewFlagSet("eval run", flag.ExitOnError)
	skillRef := fs.String("skill", "", "the skill to run against, owner/slug")
	wait := fs.Bool("wait", false, "poll until the run finishes and print the score")
	rest := parseArgs(fs, args)
	if len(rest) == 0 || *skillRef == "" {
		return fmt.Errorf("usage: llmsh eval run owner/name --skill owner/skill [--wait]")
	}
	eo, en, _ := parseRef(rest[0])
	so, sn, _ := parseRef(*skillRef)
	if eo == "" || so == "" {
		return fmt.Errorf("name both as owner/slug: the eval and --skill")
	}
	c, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	run, err := c.RunEval(eo+"/"+en, so+"/"+sn)
	if err != nil {
		return err
	}
	fmt.Printf("queued run %s\n", run.ID)
	if !*wait {
		fmt.Printf("  llmsh eval runs --skill %s/%s   to see it\n", so, sn)
		return nil
	}
	// Poll every 5s for up to 5 minutes, the same as the web. A run that takes
	// longer still finishes; `llmsh eval runs` shows it.
	for i := 0; i < 60; i++ {
		time.Sleep(5 * time.Second)
		cur, err := c.EvalRunStatus(run.ID)
		if err != nil {
			return err
		}
		switch cur.Status {
		case "succeeded":
			printRun(cur)
			return nil
		case "failed":
			return fmt.Errorf("run failed: %s", cur.Error)
		case "canceled":
			return fmt.Errorf("run was canceled")
		}
	}
	fmt.Println("  still running; check back with llmsh eval runs")
	return nil
}

// cmdEvalRuns lists a skill's runs.
func cmdEvalRuns(args []string) error {
	fs := flag.NewFlagSet("eval runs", flag.ExitOnError)
	skillRef := fs.String("skill", "", "the skill, owner/slug")
	parseArgs(fs, args)
	if *skillRef == "" {
		return fmt.Errorf("usage: llmsh eval runs --skill owner/skill")
	}
	owner, name, _ := parseRef(*skillRef)
	if owner == "" {
		return fmt.Errorf("which skill? use owner/slug")
	}
	c, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	runs, err := c.SkillRuns(owner, name)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Printf("No runs for %s/%s yet.\n", owner, name)
		return nil
	}
	// Newest first, as the server returns them; private runs -- the owner's
	// own and the hidden platform checks -- under a heading of their own, the
	// way the web groups them.
	var public, private []client.EvalRun
	for _, r := range runs {
		if r.Visibility == "public" {
			public = append(public, r)
		} else {
			private = append(private, r)
		}
	}
	for i := range public {
		printRun(&public[i])
	}
	if len(private) > 0 {
		if len(public) > 0 {
			fmt.Println()
		}
		fmt.Println("Private")
		for i := range private {
			printRun(&private[i])
		}
	}
	return nil
}

// evalLabel is how a run's eval is named: a platform check by what it checks.
func evalLabel(ref string) string {
	if rest, ok := strings.CutPrefix(ref, "llmskillhub/platform-"); ok {
		return "Platform check: " + strings.ReplaceAll(rest, "-", " ")
	}
	if ref == "" {
		return "eval"
	}
	return ref
}

func printRun(r *client.EvalRun) {
	score := "—"
	if r.Score != nil && r.MaxScore != nil && *r.MaxScore > 0 {
		score = fmt.Sprintf("%.0f%%", 100*(*r.Score)/(*r.MaxScore))
	}
	id := r.ID
	if len(id) > 8 {
		id = id[:8]
	}
	line := fmt.Sprintf("  %-9s %5s  %s  #%s", r.Status, score, evalLabel(r.Eval), id)
	if r.Method != "" {
		line += "  " + r.Method
	}
	if r.Model != "" {
		line += "  (" + r.Model + ")"
	}
	if t, err := time.Parse(time.RFC3339Nano, r.CreatedAt); err == nil {
		line += "  " + t.Local().Format("2006-01-02 15:04")
	}
	fmt.Println(line)
	if r.Summary != "" || r.Error != "" {
		msg := r.Summary
		if r.Error != "" {
			msg = r.Error
		}
		fmt.Printf("      %s\n", msg)
	}
	// What the platform found about the eval, and whether the score counts.
	var flags []string
	for _, f := range r.Integrity {
		label := strings.ReplaceAll(f.Flag, "_", " ")
		if f.Detail != "" {
			label += " (" + f.Detail + ")"
		}
		flags = append(flags, label)
	}
	if r.Counted != nil && !*r.Counted {
		flags = append(flags, "not counted in the public score")
	}
	if len(flags) > 0 {
		fmt.Printf("      ! %s\n", strings.Join(flags, "; "))
	}
}

// cmdEvalAttach attaches (or detaches) one eval to one or more of the caller's
// skills. Only the skill's owner may attach, so the eval is named once and the
// skills follow it, which is also how an owner attaches one community eval
// across a whole suite in a single command.
func cmdEvalAttach(args []string, attach bool) error {
	verb, prep := "attach", "--to"
	if !attach {
		verb, prep = "detach", "--from"
	}
	fs := flag.NewFlagSet("eval "+verb, flag.ExitOnError)
	var skills multiFlag
	fs.Var(&skills, strings.TrimPrefix(prep, "--"), "a skill of yours, owner/slug (repeatable)")
	rest := parseArgs(fs, args)
	if len(rest) == 0 {
		return fmt.Errorf("usage: llmsh eval %s owner/name %s owner/skill [owner/skill ...]", verb, prep)
	}
	// Everything after the eval reference that is not a flag is also a target,
	// so `--to a/b c/d` and `--to a/b --to c/d` both work.
	targets := append([]string(skills), rest[1:]...)
	if len(targets) == 0 {
		return fmt.Errorf("name at least one of your skills with %s owner/slug", prep)
	}
	owner, name, _ := parseRef(rest[0])
	if owner == "" {
		return fmt.Errorf("which publisher wrote the eval? use owner/name")
	}

	c, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	var results []client.AttachResult
	if attach {
		results, err = c.AttachEval(owner, name, targets)
	} else {
		results, err = c.DetachEval(owner, name, targets)
	}
	if err != nil {
		return err
	}
	done := verb + "ed"
	bad := 0
	for _, r := range results {
		if r.OK {
			fmt.Printf("  %s → %s\n", done, r.Skill)
		} else {
			bad++
			fmt.Printf("  skipped %s: %s\n", r.Skill, r.Error)
		}
	}
	if bad == len(results) {
		return fmt.Errorf("nothing was %s", done)
	}
	return nil
}

// cmdEvalList shows the evals attached to one skill.
func cmdEvalList(args []string) error {
	fs := flag.NewFlagSet("eval list", flag.ExitOnError)
	skillRef := fs.String("skill", "", "the skill, owner/slug")
	parseArgs(fs, args)
	if *skillRef == "" {
		return fmt.Errorf("usage: llmsh eval list --skill owner/skill")
	}
	owner, name, _ := parseRef(*skillRef)
	if owner == "" {
		return fmt.Errorf("which skill? use owner/slug")
	}
	c, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	evals, err := c.SkillEvals(owner, name)
	if err != nil {
		return err
	}
	if len(evals) == 0 {
		fmt.Printf("No evals attached to %s/%s yet.\n", owner, name)
		return nil
	}
	for _, e := range evals {
		tags := ""
		if e.Private {
			tags += " (private)"
		}
		if e.Source == "platform" {
			tags += " (platform check)"
		}
		fmt.Printf("  %s%s\n", e.FullName, tags)
	}
	return nil
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
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
