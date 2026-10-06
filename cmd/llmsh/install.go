package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	skill "github.com/llmskillhub/cli/skillpkg"
)

// cmdInstall downloads a skill and unpacks it where an agent will find it.
//
// The digest check is the point of doing this rather than curl and unzip. The
// archive arrives from an object store over a link the API minted, and the tree
// digest is recomputed from the unpacked files and compared with what the API
// said it should be -- so a substituted archive is caught by the client rather
// than trusted because it came over HTTPS.
func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	dest := fs.String("dir", "", "where to unpack (overrides -for)")
	forAgent := fs.String("for", "claude", "which agent will read it: "+strings.Join(targetNames(), ", "))
	force := fs.Bool("force", false, "replace an existing installation")
	ref := first(parseArgs(fs, args), "")
	if ref == "" {
		return fmt.Errorf("which skill? e.g. llmsh install xalpha2/flaky-test-hunter")
	}
	owner, name, version := parseRef(ref)

	// Checked before anything is downloaded, because it is an argument rather
	// than a discovery: a typo in -for is knowable without the network, and
	// reporting it after fetching an archive makes the user wait to be told
	// they mistyped a flag.
	agent, err := lookupTarget(*forAgent)
	if err != nil {
		return err
	}

	cfg, err := loadConfigOnly()
	if err != nil {
		return err
	}
	if owner == "" {
		return fmt.Errorf("which publisher? use owner/skill, e.g. llmsh install xalpha2/%s", name)
	}
	c := newClient(cfg)

	archive, want, kind, err := c.DownloadPackage(owner, name, version)
	if err != nil {
		return err
	}

	/*
	 * One command for both kinds, routed on what the server says it sent.
	 *
	 * An eval is test cases and expected answers, and an agent's skills
	 * directory is where it loads instructions. Unpacked there, a dataset would
	 * be read as guidance -- so evals go to ./evals instead, and --for is not
	 * consulted, because no agent is meant to load one.
	 *
	 * The kind comes from the X-Skill-Kind header the API sends with the digest,
	 * not from inspecting the bytes: what a package claims to be is the thing
	 * being decided, so the package cannot be the one to say. installEval then
	 * validates it as an eval, so a package that disagrees with its header is
	 * refused rather than written anywhere.
	 *
	 * This used to tell people to run a different command. install refused an
	 * eval outright -- "no SKILL.md at the package root", which reads as a broken
	 * package rather than a wrong command -- and the agent-facing instructions
	 * named install for every package, so the instruction and the tool
	 * disagreed. Now there is nothing to get wrong.
	 */
	if kind == "eval" {
		where := *dest
		if where == "" {
			where = "evals"
		}
		return installEval(owner, name, archive, want, where, *force)
	}

	target, err := installDir(*dest, agent)
	if err != nil {
		return err
	}
	into := filepath.Join(target, name)
	// Already there, and possibly already right.
	//
	// Two agents can share a skills directory -- Codex and Gemini CLI both read
	// their instructions from a project file and both keep the skill itself in
	// .agents/skills -- so installing the same skill for the second of them
	// finds the first one's copy. Demanding --force there would be telling
	// somebody to overwrite a directory with its own contents.
	//
	// The question is whether the bytes on disk are the bytes the registry just
	// vouched for, which is answerable rather than guessable: the digest is
	// recomputed from the existing directory. Identical means there is nothing
	// to install and only the new agent to tell. Different means a real
	// collision, and that still needs --force.
	if _, err := os.Stat(into); err == nil && !*force {
		have, _, derr := skill.PackDigest(into)
		if derr == nil && want != "" && have == want {
			wrote, err := announce(agent, owner, name, into, nil)
			if err != nil {
				return err
			}
			fmt.Printf("%s/%s is already installed at %s\n", owner, name, into)
			fmt.Printf("  same digest %s, nothing re-downloaded\n", skill.ShortDigest(have))
			for _, w := range wrote {
				fmt.Printf("  %s\n", w)
			}
			if agent.Note != "" {
				fmt.Printf("  %s\n", agent.Note)
			}
			return nil
		}
		return fmt.Errorf("%s already exists and holds something else\n  Pass --force to replace it", into)
	}

	// Unpacked to a temporary directory first, and moved into place only after
	// the digest matches. A half-written skill directory is one an agent may
	// load, so nothing lands under the target until it is known to be right.
	//
	// Unpack strips the archive's single top-level directory and writes the
	// contents at the root it is given, so this temp directory IS the skill --
	// it is renamed into place rather than something being lifted out of it.
	tmp, err := os.MkdirTemp(target, ".aq-install-")
	if err != nil {
		return err
	}
	moved := false
	defer func() {
		if !moved {
			os.RemoveAll(tmp)
		}
	}()

	pkg, err := verifyInto(archive, want, tmp)
	if err != nil {
		return err
	}

	if *force {
		if err := os.RemoveAll(into); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, into); err != nil {
		return err
	}
	moved = true
	// The temp directory was created with restrictive permissions; the installed
	// skill is ordinary content and should read like the rest of the directory.
	if err := os.Chmod(into, 0o755); err != nil {
		return err
	}

	// The skill is on disk and verified. What remains is telling this
	// particular agent about it, which for everything but Claude Code is a
	// second file in a format that agent reads.
	//
	// After the move rather than before: a rule or a context block pointing at
	// a directory that failed to install is worse than neither.
	wrote, err := announce(agent, owner, name, into, pkg)
	if err != nil {
		return err
	}

	shown := version
	if shown == "" {
		shown = "latest"
	}
	fmt.Printf("%s/%s@%s → %s\n", owner, name, shown, into)
	fmt.Printf("  %d files · digest %s verified\n", len(pkg.Files), skill.ShortDigest(pkg.Digest))
	for _, w := range wrote {
		fmt.Printf("  %s\n", w)
	}
	if agent.Note != "" {
		fmt.Printf("  %s\n", agent.Note)
	}
	return nil
}

// announce makes an installed skill visible to the agent that asked for it.
//
// Claude Code needs nothing: it reads the skills directory itself. The others
// each read one kind of file, so the skill's own instructions are written into
// that file -- once, in a block this tool can find again.
func announce(agent target, owner, name, into string, _ *skill.Package) ([]string, error) {
	if agent.Rule == "" && agent.Context == "" {
		return nil, nil
	}
	body, description, err := readInstructions(into)
	if err != nil {
		return nil, err
	}
	switch {
	case agent.Rule != "":
		path, err := writeRule(agent.Rule, owner, name, description, body, into)
		if err != nil {
			return nil, err
		}
		return []string{"rule written to " + path}, nil
	default:
		if err := writeContextBlock(agent.Context, owner, name, body, into); err != nil {
			return nil, err
		}
		return []string{"block written in " + agent.Context}, nil
	}
}

// readInstructions reads the installed SKILL.md back and splits it into the
// description and the prose.
//
// Read from disk rather than carried through from the archive: what is on disk
// is what the agent will read, and if those two ever disagree the file wins.
// The frontmatter is dropped, because it is our metadata -- an agent reading a
// Cursor rule or an AGENTS.md block has its own idea of what a header means,
// and ours would appear as text in the middle of a document.
func readInstructions(dir string) (body, description string, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return "", "", err
	}
	man, _ := skill.ParseManifest(raw)
	text := string(raw)
	// Frontmatter is the first --- fenced block, when it is the first thing in
	// the file. Anything else is prose that happens to contain a rule.
	if strings.HasPrefix(text, "---") {
		if i := strings.Index(text[3:], "\n---"); i >= 0 {
			rest := text[3+i+4:]
			if j := strings.IndexByte(rest, '\n'); j >= 0 {
				text = rest[j+1:]
			} else {
				text = ""
			}
		}
	}
	return strings.TrimSpace(text), man.Description, nil
}

// parseRef splits owner/name@version. Version is optional and means latest.
func parseRef(ref string) (owner, name, version string) {
	if i := strings.LastIndex(ref, "@"); i > 0 {
		ref, version = ref[:i], ref[i+1:]
	}
	if i := strings.Index(ref, "/"); i > 0 {
		owner, name = ref[:i], ref[i+1:]
		return owner, name, version
	}
	return "", ref, version
}

// installDir picks where skills go.
//
// A project-local directory wins when it exists, because a skill installed
// beside a project should travel with it; otherwise the personal one. Never
// created speculatively in the project: making .claude/ in someone's repository
// because they ran an install is not this command's business.
//
// The personal fallback only applies to an agent that reads a skills directory.
// The others are told about a skill through a project file -- AGENTS.md, a
// Cursor rule -- and a skill in the home directory that no project file
// mentions is one nothing will ever read, so for those the project directory is
// created.
func installDir(explicit string, agent target) (string, error) {
	if explicit != "" {
		return explicit, os.MkdirAll(explicit, 0o755)
	}
	if st, err := os.Stat(agent.Dir); err == nil && st.IsDir() {
		return agent.Dir, nil
	}
	if agent.Rule != "" || agent.Context != "" {
		return agent.Dir, os.MkdirAll(agent.Dir, 0o755)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	under := agent.Home
	if under == "" {
		under = agent.Dir
	}
	p := filepath.Join(home, under)
	return p, os.MkdirAll(p, 0o755)
}

// verifyInto unpacks an archive and refuses it unless it is what the registry
// said it would be.
//
// Separate from the command because it is the security-relevant step: the bytes
// arrive from an object store over a link the API minted, and this is what
// makes that link's contents checkable rather than merely encrypted in transit.
// The digest is recomputed from the unpacked files -- not read from the archive,
// which would be asking the thing being verified to vouch for itself.
func verifyInto(archive []byte, want, dir string) (*skill.Package, error) {
	pkg, res, err := skill.Unpack(context.Background(), bytes.NewReader(archive), int64(len(archive)), dir)
	if err != nil {
		return nil, err
	}
	if errs, _ := report(res); errs > 0 {
		return nil, fmt.Errorf("the downloaded archive did not pass validation")
	}
	if want == "" {
		// Nothing to compare against. Said plainly rather than passing quietly:
		// an unverified install is a different thing from a verified one.
		return pkg, nil
	}
	if pkg.Digest != want {
		return nil, fmt.Errorf("digest mismatch: the registry says %s, these bytes are %s\n"+
			"  Do not use this. Report it.", skill.ShortDigest(want), skill.ShortDigest(pkg.Digest))
	}
	return pkg, nil
}
