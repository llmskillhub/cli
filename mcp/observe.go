package mcp

import (
	"context"
	"strings"
)

// Call is what one request did, handed to Server.Observe for telemetry.
//
// It says what happened, never what was asked: there is no search text here,
// and nothing about the caller beyond the client name it announced. The
// privacy policy says search text is not logged or stored, and a field for it
// would be the easiest way to break that by accident.
type Call struct {
	// Method is the JSON-RPC method, or "other" for one this server doesn't
	// know -- a client can send any string, and telemetry keyed on it would
	// grow without bound.
	Method string
	// Tool is the tool named by tools/call, or "unknown" for a name that
	// isn't one of ours, for the same reason.
	Tool string
	// ClientName and ClientVersion are what initialize announced in
	// clientInfo, cut to a sensible length. Empty on every other method: the
	// transport is stateless, so later requests don't carry them.
	ClientName, ClientVersion string
	// Skill is the owner/slug a skill tool was asked about, as given.
	Skill string
	// Results is how many skills a search returned; -1 when it wasn't a search.
	Results int
	// TopMatch is the first search result's confidence (high, medium, low),
	// or "none" when nothing came back.
	TopMatch string
	// Agent is the agent install_command was asked for (claude by default).
	Agent string
	// IsError is true when the tool reported a failure.
	IsError bool
	// NotFound is true when the answer was "no such skill, version or file"
	// -- reported to the client as a normal result, not as an error.
	NotFound bool
}

type callKey struct{}

// recording returns the Call this request is filling in, or nil.
func recording(ctx context.Context) *Call {
	c, _ := ctx.Value(callKey{}).(*Call)
	return c
}

var knownMethods = map[string]bool{
	"server/discover": true, "initialize": true, "tools/list": true,
	"tools/call": true, "ping": true,
}

func knownTool(name string) bool {
	for _, t := range Tools {
		if t["name"] == name {
			return true
		}
	}
	return false
}

// short keeps announced client strings to a length a dashboard can facet on.
func short(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}
