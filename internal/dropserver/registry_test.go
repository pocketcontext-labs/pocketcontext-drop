package dropserver

import (
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// TestEveryOperationHasAHandler keeps the dropapi registry (what MCP and the
// CLI offer) and the server's handlers equal.
func TestEveryOperationHasAHandler(t *testing.T) {
	specs := dropapi.Specs()
	for _, s := range specs {
		if handlers[s.Name] == nil {
			t.Errorf("operation %s has no handler", s.Name)
		}
	}
	if len(handlers) != len(specs) {
		t.Errorf("%d handlers for %d operations", len(handlers), len(specs))
	}
}
