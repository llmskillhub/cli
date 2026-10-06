package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/llmskillhub/cli/internal/client"
	"github.com/llmskillhub/cli/internal/config"
	"github.com/llmskillhub/cli/internal/gitinfo"
	"github.com/llmskillhub/cli/internal/workdir"
	skill "github.com/llmskillhub/cli/skillpkg"
)

// report prints validation findings the same way the web uploader does, using
// the same codes, because the CLI and the browser run the same checks and a
// person who sees different words in each has to learn the format twice.
func report(res *skill.Result) (errors, warnings int) {
	for _, v := range res.Violations {
		switch v.Severity {
		case "error":
			errors++
		case "warn":
			warnings++
		}
		mark := map[string]string{"error": "✗", "warn": "!", "info": "·"}[string(v.Severity)]
		where := v.Path
		if v.Line > 0 {
			where = fmt.Sprintf("%s:%d", v.Path, v.Line)
		}
		if where != "" {
			where = " " + where
		}
		fmt.Printf("  %s %s%s  %s\n", mark, v.Code, where, v.Message)
		if v.Hint != "" {
			fmt.Printf("      %s\n", v.Hint)
		}
	}
	return errors, warnings
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	dir := first(parseArgs(fs, args), ".")
	man, res, err := skill.ValidateDir(dir)
	if err != nil {
		return err
	}

	digest, files, derr := skill.PackDigest(dir)
	if derr != nil {
		return derr
	}

	var size int64
	for _, f := range files {
		size += f.Size
	}
	name := "(no name)"
	if man != nil {
		name = man.Name
	}
	fmt.Printf("%s — %d files, %s\n", name, len(files), humanBytes(size))
	fmt.Printf("  digest %s\n", skill.ShortDigest(digest))

	errs, warns := report(res)
	if errs == 0 && warns == 0 {
		fmt.Println("  no problems found")
	}
	if errs > 0 {
		return fmt.Errorf("%d error(s) — this would be refused", errs)
	}
	return nil
}

// cmdDiff answers "what am I about to change", against the registry.
//
// This is the question git cannot answer on its own: your working tree knows
// what you changed since your last commit, and nothing local knows what you
// changed since the version other people are installing.
func cmdDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	against := fs.String("version", "", "compare against this published version (default: latest)")
	dir := first(parseArgs(fs, args), ".")

	c, cfg, err := clientFromConfig()
	if err != nil {
		return err
	}
	man, _, err := skill.ValidateDir(dir)
	if err != nil {
		return err
	}
	if man == nil || man.Name == "" {
		return fmt.Errorf("%s has no name in SKILL.md", dir)
	}
	// Against what was cloned, not against a skill of yours that happens to
	// share its name. Diffing a collaborator's working copy against your own
	// unrelated skill of the same name reports every line as changed, which
	// reads as "you have rewritten this" rather than "this is a different
	// skill".
	owner := ""
	if t, terr := targetFor(dir, man.Name, "", false); terr == nil {
		owner = t.Owner
	}
	if owner == "" {
		owner = cfg.Handle
	}
	if owner == "" {
		me, err := c.Me()
		if err != nil {
			return err
		}
		owner = me.Handle
	}

	versions, err := c.Versions(owner, man.Name)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		fmt.Printf("%s/%s has no published versions — everything here is new.\n", owner, man.Name)
		return nil
	}
	target := *against
	if target == "" {
		target = versions[0].Version
	}

	published, err := c.Files(owner, man.Name, target)
	if err != nil {
		return err
	}
	_, local, err := skill.PackDigest(dir)
	if err != nil {
		return err
	}

	// Compared by content hash, not by reading both sides: the registry already
	// stores a hash per file, so an unchanged file needs no download at all.
	pub := map[string]string{}
	for _, f := range published {
		pub[f.Path] = f.SHA256
	}
	loc := map[string]string{}
	for _, f := range local {
		loc[f.Path] = f.SHA256
	}

	var added, changed, removed []string
	for p, h := range loc {
		switch prev, ok := pub[p]; {
		case !ok:
			added = append(added, p)
		case prev != h:
			changed = append(changed, p)
		}
	}
	for p := range pub {
		if _, ok := loc[p]; !ok {
			removed = append(removed, p)
		}
	}
	sort.Strings(added)
	sort.Strings(changed)
	sort.Strings(removed)

	fmt.Printf("%s/%s — working directory against %s\n", owner, man.Name, target)
	if len(added)+len(changed)+len(removed) == 0 {
		fmt.Println("  identical: there is nothing to publish")
		return nil
	}
	for _, p := range added {
		fmt.Printf("  + %s\n", p)
	}
	for _, p := range changed {
		fmt.Printf("  ~ %s\n", p)
	}
	for _, p := range removed {
		fmt.Printf("  - %s\n", p)
	}
	fmt.Printf("\n  %d added, %d changed, %d removed\n", len(added), len(changed), len(removed))

	if g := gitinfo.Read(dir); g.Repo {
		fmt.Printf("  git: %s on %s%s\n", g.Short(), g.Branch, dirtySuffix(g))
	}
	return nil
}

func dirtySuffix(g gitinfo.Info) string {
	if !g.Dirty {
		return ""
	}
	return fmt.Sprintf(", %d uncommitted change(s)", len(g.DirtyPaths))
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func cmdPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	version := fs.String("version", "", "version to publish (default: the one in SKILL.md)")
	allowDirty := fs.Bool("allow-dirty", false, "publish even with uncommitted changes")
	dryRun := fs.Bool("dry-run", false, "validate on the server without storing anything")
	// Needed on every publish to a private skill, not only the first.
	//
	// The server settles visibility when it authorises the upload and refuses a
	// version that disagrees with its skill, so there is no way to infer it
	// here without asking first -- and asking would put a round trip on every
	// publish to save a flag on some. Forgetting it is a 409 that names both
	// sides and says what to pass, which is a better trade than a slower
	// common case.
	private := fs.Bool("private", false, "publish into your private space instead of the catalogue")
	// The opt-out from following the working copy.
	//
	// It exists because today's behaviour IS forking: a clone edited and
	// published went to a skill of your own. Changing where that lands without
	// a way back would be the same surprise in the other direction, so the
	// people relying on it keep a one-word way to ask for it.
	fork := fs.Bool("fork", false, "publish your own copy instead of back to where this was cloned from")
	positional := parseArgs(fs, args)

	// The argument is a directory, or the full name of a skill in somebody
	// else's space.
	//
	// Both, because a publish into a space you do not own has to say whose, and
	// a slug alone cannot: a collaborator may well have a skill of the same
	// name, and the server would then resolve it to their own -- which is the
	// bug this exists to close, and it did not announce itself. It published
	// happily and made a second skill nobody asked for.
	//
	// Told apart by looking at the disk rather than by guessing at the string.
	// A directory that is there is a directory, so every existing invocation
	// keeps its meaning, and "owner/slug" is only read as a name when nothing
	// of that path exists to mean anything else.
	target, dir := "", first(positional, ".")
	if strings.Contains(dir, "/") && !isDir(dir) {
		target, dir = dir, first(positional[1:], ".")
	}
	var intoOwner, intoSlug string
	if target != "" {
		owner, slug, ok := strings.Cut(strings.TrimSuffix(target, "/"), "/")
		if !ok || owner == "" || slug == "" || strings.Contains(slug, "/") {
			return fmt.Errorf("%q is neither a directory that exists nor an owner/name\n"+
				"  To publish into somebody else's space: llmsh publish <owner>/<name> [dir]", target)
		}
		intoOwner, intoSlug = owner, slug
	}
	if *fork && intoOwner != "" {
		return fmt.Errorf("--fork publishes your own copy, so it cannot be combined with %s", target)
	}

	c, cfg, err := clientFromConfig()
	if err != nil {
		return err
	}

	// Which kind this is, decided by looking rather than by asking. A
	// directory with a manifest.yaml is an eval; everything else is a skill,
	// which is what every directory was before there was a second kind.
	if isEvalDir(dir) {
		if !evalsEnabled() {
			return evalRefusal(dir)
		}
		return publishEval(c, dir, *version, *allowDirty, *dryRun, *private)
	}

	man, res, err := skill.ValidateDir(dir)
	if err != nil {
		return err
	}
	if man == nil || man.Name == "" {
		return fmt.Errorf("%s has no name in SKILL.md", dir)
	}
	if errs, _ := report(res); errs > 0 {
		return fmt.Errorf("%d error(s) — fix these first", errs)
	}

	// Where this goes, when nothing on the command line said.
	//
	// The working copy knows: it was written at clone time and nothing else in
	// the directory does. Resolved after the manifest is read, because a
	// package renamed away from what was cloned is no longer that skill, and
	// following the origin then would publish under a name its owner never
	// chose.
	t, terr := targetFor(dir, man.Name, intoOwner, *fork)
	if terr != nil {
		return terr
	}
	if intoOwner == "" && t.Owner != "" {
		intoOwner = t.Owner
	}
	if intoOwner == "" && !*fork {
		if was := renamedFrom(dir, man.Name); was != "" {
			fmt.Printf("  this was cloned from %s and is now called %s, so it publishes as yours\n",
				was, man.Name)
		}
	}

	// The name on the command line and the name in the package have to agree.
	//
	// Checked here rather than left to the server, which does reject the
	// mismatch, because the server can only say the two disagree. Here it is
	// still known which is which, so the message can say what to fix -- and
	// the common case is somebody publishing the wrong directory into a space
	// they do have write on, where the only clue is a name they did not type.
	if intoSlug != "" && intoSlug != man.Name {
		return fmt.Errorf("%s is named %q, but you asked to publish into %s/%s\n"+
			"  Publish the directory that holds %s, or correct the name",
			dir, man.Name, intoOwner, intoSlug, intoSlug)
	}

	// The git check. Publishing something that exists in no commit means the
	// bytes in the registry cannot be reproduced from the repository, and the
	// person who wrote them is the only one who will ever have them.
	g := gitinfo.Read(dir)
	if g.Repo && g.Dirty && !*allowDirty {
		fmt.Fprintf(os.Stderr, "Uncommitted changes in %s:\n", dir)
		for _, p := range g.DirtyPaths {
			fmt.Fprintf(os.Stderr, "  %s\n", p)
		}
		return fmt.Errorf("publishing now would ship bytes that exist in no commit\n" +
			"  Commit them, or pass --allow-dirty if that is what you meant")
	}

	ver := *version
	if ver == "" && man.Hub.Version != "" {
		ver = man.Hub.Version
	}
	if ver == "" {
		return fmt.Errorf("no version: set metadata.skillhub.version in SKILL.md, or pass --version")
	}

	var buf bytes.Buffer
	packRes, files, err := skill.Pack(dir, &buf, skill.PackOptions{RootName: man.Name})
	if err != nil {
		return err
	}
	if errs, _ := report(packRes); errs > 0 {
		return fmt.Errorf("packaging refused")
	}

	fmt.Printf("%s@%s — %d files, %s\n", man.Name, ver, len(files), humanBytes(int64(buf.Len())))
	if g.Repo {
		fmt.Printf("  from %s on %s\n", g.Short(), g.Branch)
	}

	// Whose space, said before it happens, because the difference is what a
	// reader most needs to know and the reply only confirms it afterwards.
	//
	// "your space" is wrong when somebody is publishing into a space they
	// collaborate on, and wrong in the direction that matters: it tells them
	// the version went somewhere they control when it went somewhere they do
	// not.
	space := "your space"
	if intoOwner != "" {
		space = intoOwner + "'s space"
	}
	if *private {
		fmt.Printf("  --private: into %s, not the catalogue. Nobody reviews it.\n", space)
	} else if intoOwner != "" {
		fmt.Printf("  into %s\n", space)
	}
	if *dryRun {
		fmt.Println("  --dry-run: checking on the server, storing nothing")
	}
	out, err := c.Publish(client.PublishOptions{
		Slug: man.Name, Owner: intoOwner, Version: ver, Kind: "skill",
		Private: *private, DryRun: *dryRun,
	}, buf.Bytes())
	if err != nil {
		return err
	}
	if errs, warns := reportUpload(out); errs > 0 {
		return fmt.Errorf("the server refused this package")
	} else if warns > 0 {
		fmt.Printf("  %d warning(s) — published anyway\n", warns)
	}

	// A working copy that just published is now at the version it published.
	// Leaving the record at whatever was cloned is what made pull and diff
	// contradict each other: one compared against a stale version, the other
	// against the latest.
	if out.OK && !*dryRun {
		if origin, rerr := workdir.Read(dir); rerr == nil && origin != nil {
			origin.Version, origin.Digest = out.Version, out.Digest
			// A fork is a new lineage, so the working copy now belongs to the
			// copy rather than to what it was taken from. Left pointing at the
			// original, the next bare publish would swing back to it -- the
			// same confusion as before, just postponed until after somebody
			// had stopped thinking about it.
			if *fork {
				if who := publisherHandle(c, cfg); who != "" {
					origin.Owner = who
				}
			}
			_ = workdir.Write(dir, origin)
		}
	}

	switch {
	case *dryRun:
		fmt.Printf("\n  would publish %s@%s · digest %s\n", man.Name, ver, skill.ShortDigest(digestOf(files)))
		fmt.Printf("  Nothing was stored.\n")
	case out.OK:
		// "submitted" and "a person reads every version" are both true of a
		// public publish and both false of a private one: nothing was
		// submitted to anybody, and nobody is going to read it. Printing them
		// anyway would tell somebody their work is queued for review when it is
		// already live in their space -- the same sentence the publish page had
		// to stop showing for the same reason.
		// Keyed on what came back, not on what was asked.
		//
		// A publish that says nothing now inherits the skill's visibility, so
		// the flag no longer decides where the version went -- and reading the
		// flag printed "submitted, a person reads every version" over a version
		// that had just landed privately and would never be read by anyone.
		if out.ReviewState == "not_required" {
			fmt.Printf("\n  %s@%s is in %s · digest %s\n",
				out.Slug, out.Version, space, out.ShortDigest)
			if intoOwner != "" {
				fmt.Printf("  Not reviewed and not listed. %s and their collaborators can see it.\n",
					intoOwner)
			} else {
				fmt.Printf("  Not reviewed and not listed. Yours now; share it from the website.\n")
			}
		} else {
			fmt.Printf("\n  %s@%s submitted · digest %s · %s\n",
				out.Slug, out.Version, out.ShortDigest, out.ReviewState)
			fmt.Printf("  A person reads every version before it appears in the catalogue.\n")
		}
	}
	return nil
}

// reportUpload prints what the server found. Its verdict, not ours: the CLI
// runs the same checks locally for speed, but ingest re-reads the archive it
// actually received and that is the answer that counts.
func reportUpload(out *client.UploadResponse) (errors, warnings int) {
	for _, v := range out.Violations {
		switch v.Severity {
		case "error":
			errors++
		case "warn":
			warnings++
		}
		mark := map[string]string{"error": "✗", "warn": "!", "info": "·"}[v.Severity]
		where := v.Path
		if v.Line > 0 {
			where = fmt.Sprintf("%s:%d", v.Path, v.Line)
		}
		if where != "" {
			where = " " + where
		}
		fmt.Printf("  %s %s%s  %s\n", mark, v.Code, where, v.Message)
		if v.Hint != "" {
			fmt.Printf("      %s\n", v.Hint)
		}
	}
	if out.Scores.Description.Total > 0 || out.Scores.Completeness.Total > 0 {
		fmt.Printf("  description %d/100 · completeness %d/100\n",
			out.Scores.Description.Total, out.Scores.Completeness.Total)
	}
	return errors, warnings
}

func cmdStatus(args []string) error {
	// Inside a working copy the name is optional, because the directory already
	// knows which skill this is -- and a bare name there resolved to a skill of
	// your own, so somebody collaborating on three same-named skills got the
	// status of a fourth without being told.
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	fromCopy := ""
	if o, oerr := workdir.Read("."); oerr == nil && o != nil {
		if name == "" {
			name = o.Slug
		}
		if o.Slug == name {
			fromCopy = o.Owner
		}
	}
	if name == "" {
		return fmt.Errorf("which skill? e.g. llmsh status flaky-test-hunter\n" +
			"  Inside a working copy the name can be left out")
	}

	c, cfg, err := clientFromConfig()
	if err != nil {
		return err
	}
	owner := fromCopy
	if owner == "" {
		owner = cfg.Handle
	}
	if i := strings.Index(name, "/"); i > 0 {
		owner, name = name[:i], name[i+1:]
	}
	if owner == "" {
		me, err := c.Me()
		if err != nil {
			return err
		}
		owner = me.Handle
	}

	versions, err := c.Versions(owner, name)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		return fmt.Errorf("no versions of %s/%s that you can see", owner, name)
	}
	fmt.Printf("%s/%s\n", owner, name)
	for _, v := range versions {
		// "not_required" is the server's word for a rule, not a word for a
		// reader, and it reads oddly in a column whose other values are review
		// outcomes. The website stopped showing it for the same reason.
		state := v.ReviewState
		if state == "not_required" {
			state = "private"
		}
		if v.Yanked {
			state += ", withdrawn"
		}
		fmt.Printf("  %-10s %-18s %s  %s\n", v.Version, state, v.ShortDigest, humanBytes(v.Size))
		if v.ReviewNotes != "" {
			fmt.Printf("      %s\n", strings.TrimSpace(v.ReviewNotes))
		}
	}
	return nil
}

// digestOf is the identity these files would publish under, computed from the
// entries Pack already produced rather than by walking the directory twice.
func digestOf(files []skill.FileEntry) string { return skill.TreeDigest(files) }


// publisherHandle is who the credential in hand belongs to, asked for only when
// something needs to be written down under their name.
func publisherHandle(c *client.Client, cfg *config.Config) string {
	if cfg != nil && cfg.Handle != "" {
		return cfg.Handle
	}
	if me, err := c.Me(); err == nil {
		return me.Handle
	}
	return ""
}
