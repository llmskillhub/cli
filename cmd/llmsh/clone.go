package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/llmskillhub/cli/internal/client"
	"github.com/llmskillhub/cli/internal/workdir"
	skill "github.com/llmskillhub/cli/skillpkg"
)

// cmdClone fetches a skill's source into a directory you can edit and publish.
//
// Distinct from install, and the distinction is the point: install puts a skill
// where an agent will load it, clone puts it where you will work on it. Only
// clone leaves a record of where the files came from, because only a working
// copy has a later question to answer -- what have I changed, and against what.
func cmdClone(args []string) error {
	fs := flag.NewFlagSet("clone", flag.ExitOnError)
	into := fs.String("dir", "", "directory to create (default: the skill's name)")
	ref := first(parseArgs(fs, args), "")
	if ref == "" {
		return fmt.Errorf("which skill? e.g. llmsh clone xalpha2/flaky-test-hunter")
	}
	owner, name, version := parseRef(ref)
	if owner == "" {
		return fmt.Errorf("which publisher? use owner/skill, e.g. llmsh clone xalpha2/%s", name)
	}

	cfg, err := loadConfigOnly()
	if err != nil {
		return err
	}
	c := newClient(cfg)

	dest := *into
	if dest == "" {
		dest = name
	}
	if _, err := os.Stat(dest); err == nil {
		// Naming the way out, because this is the first thing somebody hits
		// when they collaborate on two skills that share a name -- and the
		// directory is named after the skill, so it is guaranteed to collide.
		return fmt.Errorf("%s already exists\n"+
			"  Clone it somewhere else: llmsh clone %s/%s --dir %s-%s",
			dest, owner, name, owner, name)
	}

	pkg, resolved, err := fetchInto(c, owner, name, version, dest)
	if err != nil {
		return err
	}
	if err := workdir.Write(dest, &workdir.Origin{
		API: cfg.API, Owner: owner, Slug: name, Version: resolved, Digest: pkg.Digest,
	}); err != nil {
		return err
	}

	fmt.Printf("%s/%s@%s → %s\n", owner, name, resolved, dest)
	fmt.Printf("  %d files · digest %s verified\n", len(pkg.Files), skill.ShortDigest(pkg.Digest))
	fmt.Printf("  Edit, then: llmsh diff · llmsh push\n")
	return nil
}

// fetchInto downloads, verifies and writes a version into dest.
//
// Written to a sibling temporary directory and renamed, so an interrupted
// download cannot leave a directory that looks like a working copy but is half a
// skill. Shared by clone, pull and checkout because they differ only in what
// they do afterwards.
func fetchInto(c *client.Client, owner, name, version, dest string) (*skill.Package, string, error) {
	archive, want, err := c.Download(owner, name, version)
	if err != nil {
		return nil, "", err
	}

	// Resolved to an absolute path before taking the parent.
	//
	// filepath.Dir(".") is ".", so a relative dest put the temporary directory
	// INSIDE the directory being replaced -- and replacing the contents then
	// deleted the temporary directory along with everything else, losing the
	// files before they could be swapped in. `llmsh pull` with no arguments passes
	// ".", so this was the default path.
	abs, err := filepath.Abs(dest)
	if err != nil {
		return nil, "", err
	}
	parent := filepath.Dir(abs)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, "", err
	}
	tmp, err := os.MkdirTemp(parent, ".aq-fetch-")
	if err != nil {
		return nil, "", err
	}
	moved := false
	defer func() {
		if !moved {
			os.RemoveAll(tmp)
		}
	}()

	pkg, err := verifyInto(archive, want, tmp)
	if err != nil {
		return nil, "", err
	}

	resolved := version
	if resolved == "" {
		// The server resolved "latest" for us; ask what it actually gave.
		if vs, err := c.Versions(owner, name); err == nil {
			for _, v := range vs {
				if v.Digest == pkg.Digest {
					resolved = v.Version
					break
				}
			}
		}
	}
	if resolved == "" {
		resolved = "unknown"
	}

	if err := swapIn(tmp, abs); err != nil {
		return nil, "", err
	}
	moved = true
	if err := os.Chmod(abs, 0o755); err != nil {
		return nil, "", err
	}
	return pkg, resolved, nil
}

// cmdPull brings a working copy up to the newest published version.
func cmdPull(args []string) error {
	fs := flag.NewFlagSet("pull", flag.ExitOnError)
	dir := fs.String("dir", ".", "the working copy")
	force := fs.Bool("force", false, "discard local changes")
	_ = parseArgs(fs, args)
	return update(*dir, "", *force)
}

// cmdCheckout switches a working copy to a specific published version.
func cmdCheckout(args []string) error {
	// Parsed by the caller so the version can be taken out of the positionals
	// before updateTo defines its own flags over the same argument list.
	fs := flag.NewFlagSet("checkout", flag.ExitOnError)
	dir := fs.String("dir", ".", "the working copy")
	force := fs.Bool("force", false, "discard local changes")
	ver := first(parseArgs(fs, args), "")
	if ver == "" {
		return fmt.Errorf("which version? e.g. llmsh checkout 1.1.0")
	}
	return update(*dir, ver, *force)
}

func update(dir, version string, force bool) error {
	origin, err := workdir.Read(dir)
	if err != nil {
		return err
	}
	if origin == nil {
		return fmt.Errorf("%s is not a working copy\n  Use llmsh clone to make one, or llmsh install to install a skill", dir)
	}

	cfg, err := loadConfigOnly()
	if err != nil {
		return err
	}
	c := newClient(cfg)

	// Is there work here that exists nowhere else?
	//
	// "Differs from what was checked out" is the wrong question, and asking it
	// produced a flat contradiction: publish from a working copy and the files
	// now match the version you just published, while the recorded version is
	// still the one you cloned -- so pull refused to overwrite "local changes"
	// that diff could see were identical to a published version.
	//
	// What matters is whether the current files exist in the registry at all. If
	// they match some published version, there is nothing here to lose.
	modified, current, err := workdir.Modified(dir, origin)
	if err != nil {
		return err
	}
	if modified {
		if v, ok := publishedAs(c, origin, current); ok {
			// Already published, just not as the version we recorded. Correct
			// the record rather than stopping: the next command should not have
			// to re-derive this.
			origin.Version, origin.Digest = v, current
			_ = workdir.Write(dir, origin)
			modified = false
		}
	}
	if modified && !force {
		return fmt.Errorf("%s has changes that are not published\n"+
			"  llmsh diff shows them. Publish them, or pass --force to discard them", dir)
	}

	pkg, resolved, err := fetchInto(c, origin.Owner, origin.Slug, version, dir)
	if err != nil {
		return err
	}
	if err := workdir.Write(dir, &workdir.Origin{
		API: cfg.API, Owner: origin.Owner, Slug: origin.Slug,
		Version: resolved, Digest: pkg.Digest,
	}); err != nil {
		return err
	}

	if resolved == origin.Version && !modified {
		fmt.Printf("Already at %s@%s\n", origin.Slug, resolved)
		return nil
	}
	fmt.Printf("%s@%s → %s\n", origin.Slug, origin.Version, resolved)
	fmt.Printf("  %d files · digest %s verified\n", len(pkg.Files), skill.ShortDigest(pkg.Digest))
	return nil
}

// publishedAs reports whether the working copy's current content is exactly some
// published version.
//
// Checked by digest against every version the viewer can see, including ones
// still in review: a version you published a minute ago and are waiting on is
// still published, and refusing to pull because of it would be wrong for the
// same reason refusing because of an approved one would be.
func publishedAs(c *client.Client, origin *workdir.Origin, digest string) (string, bool) {
	versions, err := c.Versions(origin.Owner, origin.Slug)
	if err != nil {
		// Offline, or the skill is gone. Fall back to treating the copy as
		// modified, which is the cautious direction.
		return "", false
	}
	for _, v := range versions {
		if v.Digest == digest {
			return v.Version, true
		}
	}
	return "", false
}

// swapIn puts the freshly fetched directory in place of dest.
//
// A rename when dest does not exist yet, which is clone. Otherwise the CONTENTS
// are replaced, because dest may be the working directory: `llmsh pull` with no
// arguments passes ".", and removing and recreating the directory you are
// standing in fails outright on some systems and leaves the shell in a deleted
// directory on the rest.
//
// .llmsh survives, so an interruption leaves a directory that still knows what it
// is rather than one that has become an anonymous pile of files.
func swapIn(tmp, dest string) error {
	// Refuse before touching anything if the source is inside the destination.
	// Clearing dest would delete tmp along with it, which is precisely how this
	// lost a version: the caller is careful now, but a function that empties a
	// directory should not rely on being called carefully.
	tmpAbs, err := filepath.Abs(tmp)
	if err != nil {
		return err
	}
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if strings.HasPrefix(tmpAbs, destAbs+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to replace %s with a directory inside it", dest)
	}

	if _, err := os.Stat(dest); os.IsNotExist(err) {
		return os.Rename(tmp, dest)
	}

	existing, err := os.ReadDir(dest)
	if err != nil {
		return err
	}
	for _, e := range existing {
		if e.Name() == workdir.Dir {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dest, e.Name())); err != nil {
			return err
		}
	}

	fetched, err := os.ReadDir(tmp)
	if err != nil {
		return err
	}
	for _, e := range fetched {
		from := filepath.Join(tmp, e.Name())
		to := filepath.Join(dest, e.Name())
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	return os.RemoveAll(tmp)
}
