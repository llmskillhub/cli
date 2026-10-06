package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/llmskillhub/cli/internal/client"
	"github.com/llmskillhub/cli/internal/config"
	"github.com/llmskillhub/cli/mcp"
)

// cmdMCP serves the catalogue over MCP on stdio.
//
// The protocol and the tools live in packages/mcp, shared with the API's own
// endpoint at /mcp. Two implementations of the same tools would both work and
// would slowly disagree -- and the disagreement would be invisible, because
// an agent only ever sees one of them.
//
// What differs is only how the data is reached: here through the public HTTP
// API as any client would, there straight out of the database.
func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	c := newClient(cfg)
	srv := &mcp.Server{
		Catalogue:      &httpCatalogue{c: c, web: webOrigin(cfg)},
		Name:           "llmskillhub",
		Version:        buildVersion(),
		InstallCommand: "llmsh install",
	}

	// stdout is the transport. Anything else written there is a protocol
	// error on the wire, so every diagnostic goes to stderr.
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 8<<20)
	out := json.NewEncoder(os.Stdout)
	fmt.Fprintf(os.Stderr, "llmsh mcp — %s\n", cfg.API)

	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue // not something we can answer; a reply needs an id
		}
		// A notification takes no response. Replying to one is a protocol
		// violation that some clients treat as fatal.
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}

		result, rpcErr := srv.Handle(context.Background(), req.Method, req.Params)
		msg := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rpcErr != nil {
			msg["error"] = rpcErr
		} else {
			msg["result"] = result
		}
		if err := out.Encode(msg); err != nil {
			return err
		}
	}
	if err := in.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}

// httpCatalogue reaches the catalogue the way any other client would.
type httpCatalogue struct {
	c   *client.Client
	web string
}

func (h *httpCatalogue) Search(_ context.Context, query, category string, limit int) ([]mcp.Skill, mcp.SearchMeta, error) {
	res, err := h.c.Search(query, category, "", limit)
	if err != nil {
		return nil, mcp.SearchMeta{}, err
	}
	skills := make([]mcp.Skill, 0, len(res.Skills))
	for _, s := range res.Skills {
		skills = append(skills, toMCP(s))
	}
	// Carried through rather than dropped: an agent that cannot tell a match
	// from a near-miss presents both as the answer.
	return skills, mcp.SearchMeta{Semantic: res.Semantic, Relaxed: res.Relaxed}, nil
}

func (h *httpCatalogue) Skill(_ context.Context, owner, slug string) (*mcp.Skill, error) {
	s, err := h.c.Skill(owner, slug)
	if err != nil {
		return nil, err
	}
	out := toMCP(*s)
	return &out, nil
}

func (h *httpCatalogue) Versions(_ context.Context, owner, slug string) ([]mcp.Version, error) {
	vs, err := h.c.Versions(owner, slug)
	if err != nil {
		return nil, err
	}
	out := make([]mcp.Version, 0, len(vs))
	for _, v := range vs {
		out = append(out, mcp.Version{
			Version: v.Version, ShortDigest: v.ShortDigest,
			Size: v.Size, FileCount: v.FileCount,
		})
	}
	return out, nil
}

func (h *httpCatalogue) File(_ context.Context, owner, slug, version, path string) (*mcp.File, error) {
	f, err := h.c.File(owner, slug, version, path)
	if err != nil {
		return nil, err
	}
	return &mcp.File{Path: f.Path, Content: f.Content, IsText: f.IsText, Size: f.Size}, nil
}

func (h *httpCatalogue) Categories(_ context.Context) ([]mcp.Category, error) {
	cats, err := h.c.Categories()
	if err != nil {
		return nil, err
	}
	out := make([]mcp.Category, 0, len(cats))
	for _, c := range cats {
		out = append(out, mcp.Category{Slug: c.Slug, Name: c.Name, Blurb: c.Blurb, Count: c.Count})
	}
	return out, nil
}

func (h *httpCatalogue) WebURL(owner, slug string) string {
	if h.web == "" {
		return ""
	}
	return h.web + "/" + owner + "/" + slug
}

func toMCP(s client.SkillSummary) mcp.Skill {
	out := mcp.Skill{
		Owner: s.Owner, Slug: s.Slug, Description: s.Description, Kind: s.Kind,
		Categories: s.Categories, Keywords: s.Keywords,
		License: s.License, Repository: s.Repository, Downloads: s.Downloads,
	}
	if s.Latest != nil {
		out.Latest = &mcp.Version{
			Version: s.Latest.Version, ShortDigest: s.Latest.ShortDigest,
			Size: s.Latest.Size, FileCount: s.Latest.FileCount,
		}
	}
	return out
}
