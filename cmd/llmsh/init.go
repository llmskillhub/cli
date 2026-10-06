package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	skill "github.com/llmskillhub/cli/skillpkg"
)

// cmdInit scaffolds a skill.
//
// The questions are chosen from what the validator will ask for later. A
// scaffold that produces something the registry then refuses has taught the
// person nothing except that the tool does not know its own rules, so the
// generated SKILL.md is one that passes llmsh validate as written.
func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	name := fs.String("name", "", "skill name (also the directory and the URL)")
	desc := fs.String("description", "", "what it does and when to use it")
	cats := fs.String("categories", "", "comma-separated, see llmsh init --list-categories")
	license := fs.String("license", "MIT", "licence identifier")
	list := fs.Bool("list-categories", false, "print the category vocabulary and stop")
	dir := first(parseArgs(fs, args), ".")

	if *list {
		for _, c := range skill.Categories {
			fmt.Printf("  %-14s %s\n", c.Slug, c.Name)
		}
		return nil
	}

	in := bufio.NewReader(os.Stdin)
	ask := func(prompt, current string) string {
		if current != "" {
			return current
		}
		fmt.Print(prompt)
		line, _ := in.ReadString('\n')
		return strings.TrimSpace(line)
	}

	if *name == "" && dir != "." {
		*name = filepath.Base(filepath.Clean(dir))
	}
	*name = strings.ToLower(ask("Name (lowercase, hyphens): ", *name))
	if *name == "" {
		return fmt.Errorf("a skill needs a name")
	}

	if *desc == "" {
		fmt.Println()
		fmt.Println("The description is the most load-bearing field in the package: it is what an")
		fmt.Println("agent reads to decide whether to use this skill at all. Say what it does AND")
		fmt.Println("when to reach for it, including the words someone would actually type.")
		fmt.Println()
	}
	*desc = ask("Description: ", *desc)
	if *desc == "" {
		return fmt.Errorf("a skill needs a description")
	}

	if *cats == "" {
		fmt.Printf("\nCategories (comma-separated). Options:\n")
		for _, c := range skill.Categories {
			fmt.Printf("  %-14s %s\n", c.Slug, c.Name)
		}
		fmt.Println()
	}
	*cats = ask("Categories: ", *cats)

	var chosen []string
	for _, c := range strings.Split(*cats, ",") {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		if !skill.IsCategory(c) {
			return fmt.Errorf("%q is not a category — run llmsh init --list-categories", c)
		}
		chosen = append(chosen, c)
	}
	if len(chosen) == 0 {
		return fmt.Errorf("pick at least one category, or nobody will find this")
	}

	target := dir
	if target == "." {
		target = *name
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("%s already exists", target)
	}

	// references/ and scripts/ are created empty on purpose: the layout check
	// warns about files outside them, and a scaffold that leaves someone to
	// discover the convention from a warning has buried the lede.
	for _, d := range []string{"", "references", "scripts"} {
		if err := os.MkdirAll(filepath.Join(target, d), 0o755); err != nil {
			return err
		}
	}

	if err := os.WriteFile(filepath.Join(target, "SKILL.md"),
		[]byte(scaffold(*name, *desc, chosen, *license)), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(target, "CHANGELOG.md"),
		[]byte("# Changelog\n\n## 0.1.0\n\nFirst version.\n"), 0o644); err != nil {
		return err
	}

	fmt.Printf("\nCreated %s\n", target)
	fmt.Printf("  SKILL.md       the instructions, and the frontmatter above them\n")
	fmt.Printf("  references/    background the agent reads only when it needs to\n")
	fmt.Printf("  scripts/       code the agent may run\n")
	fmt.Printf("\nNext: edit SKILL.md, then llmsh validate %s\n", target)
	return nil
}

// scaffold writes a SKILL.md that already passes validation.
//
// The description is folded rather than quoted because it is long by design, and
// the body is a skeleton with the headings the completeness score looks for --
// present so they are filled in, not so they can be left as they are.
func scaffold(name, desc string, cats []string, license string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + name + "\n")
	b.WriteString("description: >\n")
	for _, line := range wrap(desc, 92) {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("license: " + license + "\n")
	// From the constant, not typed out. The scaffold is where someone learns
	// the shape of a manifest, so a stale key here teaches the wrong one to
	// everybody who runs init -- and it is the copy least likely to be noticed,
	// because it produces a file that still validates.
	b.WriteString("metadata:\n  " + skill.HubKey + ":\n")
	b.WriteString("    version: 0.1.0\n")
	b.WriteString("    categories: [" + strings.Join(cats, ", ") + "]\n")
	b.WriteString("    keywords: []\n")
	b.WriteString("    capabilities:\n")
	b.WriteString("      network: false\n")
	b.WriteString("      filesystem: read\n")
	b.WriteString("      shell: false\n")
	b.WriteString("      secrets: []\n")
	b.WriteString("---\n\n")

	title := strings.ReplaceAll(name, "-", " ")
	b.WriteString("# " + strings.ToUpper(title[:1]) + title[1:] + "\n\n")
	b.WriteString(desc + "\n\n")
	b.WriteString("## When to reach for this\n\n")
	b.WriteString("Describe the situation in the user's words, and say what goes wrong without\n")
	b.WriteString("this skill. An agent decides from this whether the skill applies at all.\n\n")
	b.WriteString("## Process\n\n")
	b.WriteString("1. **Look before changing anything.** Say what to inspect first and why.\n")
	b.WriteString("2. **Do the work.** One step per numbered item, in the order they happen.\n")
	b.WriteString("3. **Check it worked.** Say how to tell, not just that it should be checked.\n\n")
	b.WriteString("## What not to do\n\n")
	b.WriteString("The failure modes you already know about. This section is worth more than\n")
	b.WriteString("the happy path, because the happy path is usually guessable.\n")
	return b.String()
}

func wrap(s string, width int) []string {
	words := strings.Fields(s)
	var lines []string
	var cur string
	for _, w := range words {
		if cur == "" {
			cur = w
			continue
		}
		if len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
