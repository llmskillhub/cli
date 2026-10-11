package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Tools is what a client sees. Descriptions are written for the model that
// reads them -- they say when to reach for the tool, not only what it does,
// because that is what the choice is actually made on.
var Tools = []map[string]any{
	{
		"name":        "search_skills",
		"title":       "Search skills",
		"description": "Find skills by describing the task, in a sentence, in whatever words the user used — the catalogue matches meaning as well as words, so \"make an animated picture for chat\" finds a Slack GIF skill that shares none of those words with it. Use when the user asks for a capability you do not have, or describes something a published skill might already do. If you already know the skill's name, call get_skill instead: this searches, that fetches. Read the first line of the result: it says whether these matched what you asked or are only related to it. Each result then has a match line. high: it was named, or contains every word asked and matches by meaning -- a fair answer to recommend. medium: one kind of evidence -- read it with get_skill before recommending. low: related by meaning or by a few words only -- not an answer; say so, or keep looking.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":    map[string]any{"type": "string", "description": "What the skill should do, phrased as the task rather than as a name. A sentence works better than a keyword."},
				"category": map[string]any{"type": "string", "description": "Restrict to one category slug; see list_categories."},
				"limit":    map[string]any{"type": "integer", "description": "How many to return. Default 10, maximum 50."},
			},
		},
	},
	{
		"name":        "get_skill",
		"title":       "Get a skill",
		"description": "Full detail for one named skill: description, categories, licence, and every published version with its digest and size. Use whenever you already know the name — from the user, from a link, or from a search_skills result — rather than searching for a name you have. Searching for a name you already know costs a call and can return something else.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":  map[string]any{"type": "string", "description": "The skill, as \"owner/slug\" or just \"slug\". A slug alone is usually enough; when more than one publisher has that name the reply lists them, so ask again with the owner."},
				"owner": map[string]any{"type": "string", "description": "Alternative to name: the publisher's handle."},
				"slug":  map[string]any{"type": "string", "description": "Alternative to name: the skill's own name."},
			},
		},
	},
	{
		"name":        "read_skill_file",
		"title":       "Read a file from a skill",
		"description": "Read one file out of a published version. Defaults to SKILL.md, the skill's own instructions, so a name alone answers \"what does this actually do\" — you do not need get_skill first. Pass path for any script or reference it ships.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]any{"type": "string", "description": "The skill, as \"owner/slug\" or just \"slug\"."},
				"owner":   map[string]any{"type": "string", "description": "Alternative to name."},
				"slug":    map[string]any{"type": "string", "description": "Alternative to name."},
				"path":    map[string]any{"type": "string", "description": "Path inside the package. Defaults to SKILL.md, which is the skill's own instructions."},
				"version": map[string]any{"type": "string", "description": "Defaults to the latest published version."},
			},
		},
	},
	{
		"name":        "list_categories",
		"title":       "List categories",
		"description": "The catalogue's category vocabulary, with how many skills are in each. Use to browse when a search term is not obvious.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":  "install_command",
		"title": "How to install a skill",
		// Deliberately returns the command instead of running it.
		//
		// Installing writes a stranger's instructions into a directory an
		// agent loads from, which is the decision a person should be making.
		// The specification asks for a human in the loop on tool calls; a tool
		// that hands back a command someone can read and run IS that loop,
		// rather than a confirmation dialog bolted onto a side effect. It also
		// happens to be the only honest answer over HTTP, where the server has
		// no access to the caller's disk at all.
		"description": "The exact command to install a skill. Show it to the user and let them run it — this does not install anything itself.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]any{"type": "string", "description": "The skill, as \"owner/slug\" or just \"slug\"."},
				"owner":   map[string]any{"type": "string", "description": "Alternative to name."},
				"slug":    map[string]any{"type": "string", "description": "Alternative to name."},
				"version": map[string]any{"type": "string", "description": "Pin an exact version. Recommended."},
			},
		},
	},
}

// call runs one tool and renders its answer as text.
//
// Text rather than JSON, because the consumer is a language model: a readable
// list costs fewer tokens than the same data wrapped in braces, and the model
// does not have to be told a schema first. The fields a decision turns on --
// the digest, the licence, the command -- are spelled out rather than implied.
// splitName takes "owner/slug" or "slug" and gives back both halves, the owner
// empty when it was not offered.
//
// An unqualified name can be ambiguous, and the caller is told so rather than
// guessed at -- names are unique per publisher since 013, so two people may
// each have a "pricing-strategy". See resolve in the server, which refuses and
// names the candidates.
func splitName(name string) (owner, slug string) {
	name = strings.TrimSpace(strings.TrimPrefix(name, "@"))
	if o, s, ok := strings.Cut(name, "/"); ok {
		return strings.TrimSpace(o), strings.TrimSpace(s)
	}
	return "", name
}

// summaryChars is how much of a description a search result carries.
//
// Measured on the live catalogue: a ten-result search ran 5,600 characters and
// 83% of it was descriptions -- median 457, longest 746. They open with what
// the skill does and go on to enumerate its features, and a list is read to
// choose what to look at rather than to learn what a thing is. get_skill has
// the whole text for the one that gets chosen.
const summaryChars = 240

// summarise shortens a description for a list, on a word boundary.
func summarise(d string) string {
	d = strings.TrimSpace(d)
	if len([]rune(d)) <= summaryChars {
		return d
	}
	// By rune, not by byte. These descriptions carry em dashes and accents,
	// and slicing a string at a byte offset lands inside one often enough --
	// the result is not a shorter description, it is a broken character.
	cut := string([]rune(d)[:summaryChars])
	// Back to the last space, so a list never ends mid-word. With no space to
	// find, the text is one long token and cutting it anywhere is the same;
	// take the budget as it stands.
	if i := strings.LastIndex(cut, " "); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:-—") + "…"
}

// qualify names a skill for display. Without an owner it is the slug alone,
// rather than a slash with nothing in front of it -- a caller who gave a bare
// name should not be shown "/slack-gif-creator" back.
func qualify(owner, slug string) string {
	if owner == "" {
		return slug
	}
	return owner + "/" + slug
}

func (s *Server) call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	var a struct {
		Query    string `json:"query"`
		Category string `json:"category"`
		Limit    int    `json:"limit"`
		Name     string `json:"name"`
		Owner    string `json:"owner"`
		Slug     string `json:"slug"`
		Path     string `json:"path"`
		Version  string `json:"version"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", fmt.Errorf("could not read the arguments: %v", err)
		}
	}

	// One name, however it was given. A model that has "anthropics/mcp-builder"
	// in front of it should not have to take it apart, and one that has only
	// "mcp-builder" should not have to search for the half it is missing.
	if a.Name != "" {
		a.Owner, a.Slug = splitName(a.Name)
	}

	switch name {
	case "search_skills":
		limit := a.Limit
		if limit <= 0 {
			limit = 10
		}
		if limit > 50 {
			limit = 50
		}
		skills, meta, err := s.Catalogue.Search(ctx, a.Query, a.Category, limit)
		if err != nil {
			return "", err
		}
		if len(skills) == 0 {
			// How it looked decides what to do about it. Searched by meaning,
			// a different wording will not find what this one missed -- and a
			// model told only "no match" will try three more phrasings before
			// concluding the same thing. Searched by words alone, rephrasing
			// is exactly what might work.
			if meta.Semantic {
				return fmt.Sprintf("No skills match %q. The catalogue was searched by meaning as well as by words, "+
					"so rephrasing is unlikely to find one. Use list_categories to browse, or tell the user nothing "+
					"published does this yet.", a.Query), nil
			}
			// One word is matched literally on purpose, not for want of a
			// model: a lone word embeds to a vague point that sits close to
			// everything, so meaning was measured and deliberately not used.
			// Saying "unavailable" blamed an outage that was not happening
			// and asked for a rewording, when what helps is more words.
			if len(strings.Fields(strings.TrimSpace(a.Query))) == 1 {
				return fmt.Sprintf("No skills match %q. A single word is matched literally rather than by "+
					"meaning -- describe the task in a few words and the catalogue will search for what a "+
					"skill does. Or list_categories to browse.", a.Query), nil
			}
			return fmt.Sprintf("No skills match %q, searched by words alone -- matching by meaning is "+
				"unavailable just now. A different wording may work; list_categories to browse.", a.Query), nil
		}
		var b strings.Builder
		// Matches, or near misses, said before the list rather than after it.
		//
		// Relaxed means nothing contained every word, so the catalogue widened
		// rather than answer with nothing. The results look identical either
		// way -- a name and a description -- and the difference is whether a
		// model should recommend one or keep looking. Not "N of M": this only
		// ever knew N, and saying "4 match, showing 4" told a model the
		// catalogue held four when it held four on this page.
		if meta.Relaxed {
			fmt.Fprintf(&b, "%d skill(s) related to %q. Nothing matched every word, so these are near misses "+
				"rather than answers -- read one before recommending it.\n\n", len(skills), a.Query)
		} else {
			fmt.Fprintf(&b, "%d skill(s) for %q:\n\n", len(skills), a.Query)
		}
		for _, sk := range skills {
			// Marked, because the catalogue holds two kinds and only one of them
			// is instructions. An agent that picked an eval from this list and
			// treated it as a skill would be loading test cases as guidance.
			label := ""
			if sk.Kind == "eval" {
				label = "  [eval]"
			}
			fmt.Fprintf(&b, "%s/%s%s\n  %s\n", sk.Owner, sk.Slug, label, summarise(sk.Description))
			if m := sk.Match; m != nil && m.Confidence != "" {
				fmt.Fprintf(&b, "  match: %s%s\n", m.Confidence, evidence(m))
			}
			var bits []string
			if sk.Latest != nil {
				bits = append(bits, "v"+sk.Latest.Version,
					fmt.Sprintf("%d files", sk.Latest.FileCount), humanBytes(sk.Latest.Size))
			}
			if len(sk.Categories) > 0 {
				bits = append(bits, strings.Join(sk.Categories, ", "))
			}
			bits = append(bits, fmt.Sprintf("%d installs", sk.Downloads))
			fmt.Fprintf(&b, "  %s\n\n", joinNonEmpty(bits, " · "))
		}
		if len(skills) == limit {
			fmt.Fprintf(&b, "That is the first %d; raise limit to see more.\n", limit)
		}
		b.WriteString("Descriptions are shortened here; get_skill has the full text, read_skill_file has the instructions.")
		return b.String(), nil

	case "get_skill":
		if a.Slug == "" {
			return "", fmt.Errorf("name is required, as \"owner/slug\" or just \"slug\"")
		}
		sk, err := s.Catalogue.Skill(ctx, a.Owner, a.Slug)
		if err != nil {
			return "", err
		}
		versions, _ := s.Catalogue.Versions(ctx, a.Owner, a.Slug)

		var b strings.Builder
		fmt.Fprintf(&b, "%s/%s\n\n%s\n\n", sk.Owner, sk.Slug, sk.Description)
		fmt.Fprintf(&b, "Kind:       %s\n", kindName(sk.Kind))
		if len(sk.Categories) > 0 {
			fmt.Fprintf(&b, "Categories: %s\n", strings.Join(sk.Categories, ", "))
		}
		if len(sk.Keywords) > 0 {
			fmt.Fprintf(&b, "Keywords:   %s\n", strings.Join(sk.Keywords, ", "))
		}
		if sk.License != "" {
			fmt.Fprintf(&b, "Licence:    %s\n", sk.License)
		}
		if sk.Repository != "" {
			fmt.Fprintf(&b, "Source:     %s\n", sk.Repository)
		}
		fmt.Fprintf(&b, "Installs:   %d\n", sk.Downloads)
		if len(versions) > 0 {
			b.WriteString("\nVersions:\n")
			for _, v := range versions {
				fmt.Fprintf(&b, "  %-10s %s  %d files  %s\n",
					v.Version, v.ShortDigest, v.FileCount, humanBytes(v.Size))
			}
		}
		if u := s.Catalogue.WebURL(sk.Owner, sk.Slug); u != "" {
			fmt.Fprintf(&b, "\nPage: %s\n", u)
		}
		fmt.Fprintf(&b, "\n%s\n", whereItGoes(sk.Kind))
		return b.String(), nil

	case "read_skill_file":
		if a.Slug == "" {
			return "", fmt.Errorf("name is required, as \"owner/slug\" or just \"slug\"")
		}
		// SKILL.md by default, because it is the answer to "what does this
		// actually do" and that is why the tool gets called. Requiring the
		// path meant search, then get_skill to learn the shape of the
		// package, then this -- three calls to read one file whose name never
		// varies.
		if a.Path == "" {
			a.Path = "SKILL.md"
		}
		version := a.Version
		if version == "" {
			vs, err := s.Catalogue.Versions(ctx, a.Owner, a.Slug)
			if err != nil {
				return "", err
			}
			if len(vs) == 0 {
				return "", fmt.Errorf("%s has no published versions", qualify(a.Owner, a.Slug))
			}
			version = vs[0].Version
		}
		f, err := s.Catalogue.File(ctx, a.Owner, a.Slug, version, a.Path)
		if err != nil {
			return "", err
		}
		if !f.IsText {
			return fmt.Sprintf("%s is not a text file (%s).", f.Path, humanBytes(f.Size)), nil
		}
		return fmt.Sprintf("%s@%s — %s\n\n%s", qualify(a.Owner, a.Slug), version, f.Path, f.Content), nil

	case "list_categories":
		cats, err := s.Catalogue.Categories(ctx)
		if err != nil {
			return "", err
		}
		// Empty shelves are left out, the same way the website leaves them out.
		// An agent reading a category with nothing in it will either search it
		// and come back empty or tell someone the catalogue has none of what
		// they want; neither is true, and both cost a round trip to find out.
		var b strings.Builder
		var shown int
		for _, c := range cats {
			if c.Count == 0 {
				continue
			}
			fmt.Fprintf(&b, "  %-14s %-24s %d skill(s)\n    %s\n", c.Slug, c.Name, c.Count, c.Blurb)
			shown++
		}
		if shown == 0 {
			// Saying so beats printing a bare heading over nothing, which reads
			// as a failure rather than as an empty catalogue.
			return "No skills are filed under a category yet. Use search_skills instead.", nil
		}
		return "Categories:\n\n" + b.String(), nil

	case "install_command":
		if a.Slug == "" {
			return "", fmt.Errorf("name is required, as \"owner/slug\" or just \"slug\"")
		}
		// Always resolved now, not only when the owner is missing. The owner
		// still matters -- "anthropics/x" tells a person who wrote what they
		// are about to install, and "x" does not -- but so does the kind, and
		// the kind is what decides where it unpacks.
		sk, err := s.Catalogue.Skill(ctx, a.Owner, a.Slug)
		if err != nil {
			return "", err
		}
		owner := sk.Owner
		ref := owner + "/" + a.Slug
		if a.Version != "" {
			ref += "@" + a.Version
		}
		// One command for both kinds. llmsh asks the registry what it is
		// downloading and sends it to the right place, so the instruction an
		// agent passes on can never be the wrong command -- which it was for
		// an eval, until install learned to route them.
		return fmt.Sprintf(
			"To install %s, the user runs:\n\n  %s %s\n\n%s\n\n"+
				"Show them this command rather than running it: it writes a third "+
				"party's files to their machine, which is their decision to make.",
			ref, s.InstallCommand, ref, whereItGoes(sk.Kind)), nil
	}
	return "", fmt.Errorf("no such tool: %s", name)
}


func kindName(kind string) string {
	if kind == "eval" {
		return "eval — test cases and expected answers, not instructions"
	}
	return "skill — instructions an agent loads"
}

/*
whereItGoes says where llmsh unpacks a package of this kind, for the agent to
pass on.

Said at the moment it matters, rather than left for the agent to work out. A
skill belongs where an agent loads guidance; an eval must never go there, since
an agent reading a dataset of questions and expected answers as instructions is
the one outcome the separation exists to prevent. llmsh decides this itself from
what the registry says it downloaded, so the instruction is the same command for
both -- this only tells the reader what that command will do.
*/
func whereItGoes(kind string) string {
	if kind == "eval" {
		return "This is an eval, so llmsh unpacks it into ./evals/ — never into a " +
			"skills directory, where an agent would read its test cases as instructions. " +
			"It is for running and grading, not for loading."
	}
	return "This is a skill, so llmsh unpacks it into .claude/skills/ in the project, " +
		"or ~/.claude/skills/ otherwise, and verifies the package digest before writing anything."
}

// evidence says what found a result, in the order the catalogue lists it,
// with the word count beside "words" so "words" alone never implies all of
// them.
func evidence(m *Match) string {
	if len(m.By) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m.By))
	for _, by := range m.By {
		if by == "words" && m.Words != "" {
			by = "words " + m.Words
		}
		parts = append(parts, by)
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
