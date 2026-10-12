package mcp

import "testing"

// TestEveryToolIsAnnotated: Anthropic's directory rejects a connector whose
// tools lack a title and a read or write hint, and the hints are what decide
// whether Claude asks before each call. A new tool has to state which it is.
func TestEveryToolIsAnnotated(t *testing.T) {
	for _, tool := range Tools {
		name := tool["name"]
		a, ok := tool["annotations"].(map[string]any)
		if !ok {
			t.Errorf("%v has no annotations", name)
			continue
		}
		if a["title"] == "" || a["title"] != tool["title"] {
			t.Errorf("%v: annotations.title %q should match title %q", name, a["title"], tool["title"])
		}
		ro, _ := a["readOnlyHint"].(bool)
		de, _ := a["destructiveHint"].(bool)
		if ro == de {
			t.Errorf("%v: exactly one of readOnlyHint and destructiveHint must be true", name)
		}
	}
}
