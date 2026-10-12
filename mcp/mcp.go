// Package mcp serves the catalogue over the Model Context Protocol.
//
// The protocol and the tools live here so that both ways of reaching them --
// `llmsh mcp` over stdio, and POST /mcp on the API -- are the same server with
// two front doors. The alternative was one implementation per transport, and a
// tool description that drifted between them would be invisible: both would
// work, and an agent would get different answers depending on how it connected.
//
// Written against the specification rather than an SDK. The surface is four
// methods of JSON-RPC, and one of the two callers is a binary people install
// and run on their own machine.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Two protocol eras, because the specification moved and the clients did not.
// 2026-07-28 dropped the initialize handshake, carries the version in each
// request's _meta and requires server/discover; everything shipping today
// still opens with initialize.
const (
	VersionModern = "2026-07-28"
	VersionLegacy = "2025-06-18"
)

// Catalogue is everything the tools need, and nothing else.
//
// An interface because the two callers reach the same data by different
// routes: the CLI over the public HTTP API, the API straight from its own
// repository. Neither should have to know which one it is.
type Catalogue interface {
	Search(ctx context.Context, query, category string, limit int) ([]Skill, SearchMeta, error)
	Skill(ctx context.Context, owner, slug string) (*Skill, error)
	Versions(ctx context.Context, owner, slug string) ([]Version, error)
	File(ctx context.Context, owner, slug, version, path string) (*File, error)
	Categories(ctx context.Context) ([]Category, error)
	// WebURL is where a person can open a skill, for links in the answers.
	WebURL(owner, slug string) string
}

// SearchMeta is how the catalogue arrived at the results, which the results
// themselves do not show.
//
// A model choosing between five names cannot tell a list of things that match
// from a list of things that are merely nearby, and the difference decides
// whether it recommends one or keeps looking. The catalogue knows; it used to
// stop knowing at this boundary.
type SearchMeta struct {
	// Semantic is true when meaning was compared as well as words.
	Semantic bool
	// Relaxed is true when nothing contained every word of the query, so the
	// catalogue widened the match rather than answer with nothing.
	Relaxed bool
}

type Skill struct {
	Owner, Slug, Description string
	// Kind is "skill" or "eval", as the catalogue recorded it. An agent needs
	// it before it acts: a skill is instructions it may load, an eval is test
	// cases it must not. Empty from a catalogue too old to say, read as skill.
	Kind                 string
	Categories, Keywords []string
	License, Repository  string
	Downloads            int
	Latest               *Version
	// Match is why a search returned this skill and how far to trust it. Nil
	// outside search results, and from a catalogue too old to say.
	Match *Match
}

// Match is the catalogue's own account of one search result.
//
// A label and its evidence, never a number: the scores underneath are not
// probabilities, and a percentage would be read as one. The catalogue measures
// how often each label is right against a labelled set of searches; high has
// been right about four times in five, low about one in eight.
type Match struct {
	Confidence string   // high, medium or low
	By         []string // what found it: name, words, meaning
	Words      string   // how many of the query's words it contains, as "3/4"
}

type Version struct {
	Version, ShortDigest string
	Size                 int64
	FileCount            int
}

type File struct {
	Path    string
	Content string
	IsText  bool
	Size    int64
}

type Category struct {
	Slug, Name, Blurb string
	Count             int
}

// Server answers JSON-RPC, whatever carried it.
type Server struct {
	Catalogue Catalogue
	// Name and Version identify this server to a client.
	Name, Version string
	// InstallCommand is how a person installs a skill, e.g. "llmsh install".
	InstallCommand string
	// AllowedOrigins are browser origins permitted to reach the HTTP
	// transport. Empty refuses every Origin header, which is right: an MCP
	// client sends none, and the ones that do are pages.
	AllowedOrigins []string
	// Observe, when set, is told what each request did, after it is handled.
	// See Call for what it carries and, as importantly, what it doesn't.
	Observe func(context.Context, Call)
}

// Cacheable results carry how long they are good for and who may share the
// cache. Both are specified on server/discover and tools/list, and a client
// that validates the result shape rejects the whole list without them -- which
// is how this was found: connected, and every tool missing.
//
// public, because nothing here varies by caller. The tool list is compiled
// into the binary and the discovery answer is the same for everyone, so one
// cached copy is correct for all of them.
const (
	ttlDiscover = 3600000 // an hour; this changes when the server is redeployed
	ttlTools    = 300000  // five minutes
	cacheScope  = "public"
)

const instructions = "Search LLM SkillHub for published agent skills — reusable " +
	"instructions, scripts and references an agent can load. Every version listed " +
	"here was read by a person before it was listed."

// Handle dispatches one request and returns either a result or a JSON-RPC
// error object.
func (s *Server) Handle(ctx context.Context, method string, params json.RawMessage) (any, map[string]any) {
	if s.Observe != nil {
		c := &Call{Method: method, Results: -1}
		if !knownMethods[method] {
			c.Method = "other"
		}
		ctx = context.WithValue(ctx, callKey{}, c)
		defer func() { s.Observe(ctx, *c) }()
	}
	switch method {
	case "server/discover":
		return map[string]any{
			"resultType":        "complete",
			"supportedVersions": []string{VersionModern, VersionLegacy},
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"instructions":      instructions,
			"ttlMs":             ttlDiscover,
			"cacheScope":        cacheScope,
			"_meta": map[string]any{
				"io.modelcontextprotocol/serverInfo": map[string]any{
					"name": s.Name, "version": s.Version,
				},
			},
		}, nil

	case "initialize":
		// Echo the version the client opened with rather than announcing
		// ours: a client on an older revision is telling us what it can parse.
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(params, &p)
		if c := recording(ctx); c != nil {
			c.ClientName, c.ClientVersion = short(p.ClientInfo.Name), short(p.ClientInfo.Version)
		}
		version := p.ProtocolVersion
		if version == "" {
			version = VersionLegacy
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
			"instructions":    instructions,
		}, nil

	case "tools/list":
		return map[string]any{
			"resultType": "complete",
			"tools":      Tools,
			"ttlMs":      ttlTools,
			"cacheScope": cacheScope,
		}, nil

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, map[string]any{"code": -32602, "message": "invalid params"}
		}
		if c := recording(ctx); c != nil {
			c.Tool = p.Name
			if !knownTool(p.Name) {
				c.Tool = "unknown"
			}
		}
		text, err := s.call(ctx, p.Name, p.Arguments)
		if errors.Is(err, ErrNotFound) {
			// An answer, not a failure: see ErrNotFound.
			if c := recording(ctx); c != nil {
				c.NotFound = true
			}
			return textResult(s.notFoundAnswer(ctx, err, p.Arguments)), nil
		}
		if err != nil {
			if c := recording(ctx); c != nil {
				c.IsError = true
			}
			// A failed tool is reported in the result, not as an RPC error:
			// the model should see what went wrong and be able to try
			// something else, rather than the client treating it as a
			// transport failure.
			return errorResult(err.Error()), nil
		}
		return textResult(text), nil

	case "ping":
		return map[string]any{"resultType": "complete"}, nil
	}
	return nil, map[string]any{"code": -32601, "message": "method not found: " + method}
}

func textResult(text string) map[string]any {
	return map[string]any{
		"resultType": "complete",
		"content":    []any{map[string]any{"type": "text", "text": text}},
		"isError":    false,
	}
}

func errorResult(text string) map[string]any {
	return map[string]any{
		"resultType": "complete",
		"content":    []any{map[string]any{"type": "text", "text": text}},
		"isError":    true,
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func joinNonEmpty(parts []string, sep string) string {
	out := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
