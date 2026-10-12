package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound marks an answer that is "there is no such thing", as opposed to
// a failure. A Catalogue returns it (via NotFound) for a skill, version or
// file that doesn't exist, or a name that several publishers share.
//
// The difference matters to whoever counts errors. Anthropic's directory
// counts every tool result with isError: true against a connector's public
// health -- above 5% it reads "Degraded" -- and the first day of real traffic
// had Claude asking for skills by names it remembered from elsewhere
// (doc-coauthoring, web-artifacts-builder) that this catalogue doesn't hold.
// The server answered correctly and was scored as failing. So not-found is a
// normal result: what was asked for isn't here, and what is close to it.
var ErrNotFound = errors.New("not found")

type notFound struct {
	msg string
	// within is set when the skill exists and something inside it doesn't --
	// a version, a file. Suggesting other skills would answer the wrong
	// question, so the message alone is the answer.
	within bool
}

func (e notFound) Error() string        { return e.msg }
func (e notFound) Is(target error) bool { return target == ErrNotFound }

// NotFound builds an ErrNotFound for a skill that doesn't exist, or a name
// several publishers share. The answer adds the closest skills by name.
func NotFound(format string, a ...any) error {
	return notFound{msg: fmt.Sprintf(format, a...)}
}

// Ambiguous is for a bare name several publishers share. Still never a guess
// -- the reply is the choice, not any one skill -- but a normal answer: the
// caller asked a reasonable question and gets the names to ask again with.
// No suggestions are added, because the names are the suggestions.
func Ambiguous(format string, a ...any) error {
	return notFound{msg: fmt.Sprintf(format, a...), within: true}
}

// NotFoundWithin builds an ErrNotFound for something missing inside a skill
// that does exist: a version, a file. The message should say what is there.
func NotFoundWithin(format string, a ...any) error {
	return notFound{msg: fmt.Sprintf(format, a...), within: true}
}

// notFoundAnswer turns a not-found into something useful: the message, then
// the closest skills to the name that was asked for. The name is searched as
// words ("doc-coauthoring" as "doc coauthoring"), so a guessed or misspelled
// name still lands somewhere near.
func (s *Server) notFoundAnswer(ctx context.Context, err error, raw json.RawMessage) string {
	var a struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	_ = json.Unmarshal(raw, &a)
	name := a.Name
	if name == "" {
		name = a.Slug
	}
	_, slug := splitName(name)

	var b strings.Builder
	b.WriteString(err.Error())
	b.WriteString(".")
	var nf notFound
	if errors.As(err, &nf) && nf.within {
		return b.String()
	}
	if skills := s.nearestByName(ctx, slug); len(skills) > 0 {
		b.WriteString("\n\nClosest in the catalogue:\n")
		for _, sk := range skills {
			fmt.Fprintf(&b, "  %s/%s -- %s\n", sk.Owner, sk.Slug, summarise(sk.Description))
		}
		b.WriteString("\nCall get_skill with one of these names, or search_skills with what the skill should do.")
		return b.String()
	}
	b.WriteString(" Nothing close was found; use search_skills with what the skill should do, " +
		"or tell the user this catalogue doesn't have it.")
	return b.String()
}

// nearestByName finds skills close to a name that doesn't exist: the name as
// words first ("doc-coauthoring" as "doc coauthoring"), then each of its words
// alone when the whole finds nothing -- word matching wants every word, and a
// guessed name usually has one that no description uses.
func (s *Server) nearestByName(ctx context.Context, slug string) []Skill {
	q := strings.TrimSpace(strings.NewReplacer("-", " ", "_", " ", ".", " ").Replace(slug))
	if q == "" {
		return nil
	}
	if skills, _, err := s.Catalogue.Search(ctx, q, "", 3); err == nil && len(skills) > 0 {
		return skills
	}
	for _, w := range strings.Fields(q) {
		if len(w) < 3 {
			continue
		}
		if skills, _, err := s.Catalogue.Search(ctx, w, "", 3); err == nil && len(skills) > 0 {
			return skills
		}
	}
	return nil
}
