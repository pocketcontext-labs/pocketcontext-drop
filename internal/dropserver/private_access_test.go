package dropserver_test

import (
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestRetiredRuntimeAccessOperations(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, token := h.User("builder@example.test", "developer")
	for _, op := range []string{"private.access", "private.grant", "private.revoke", "private.shared"} {
		wantError(t, h.Op(token, op, map[string]any{"app": "custom-test"}), 400, "unknown_op")
	}
}
