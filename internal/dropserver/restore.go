package dropserver

import (
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// InvalidateNodeTokens rotates every node's token key, so no node token
// issued before a restore works on the restored instance. Core calls it
// first when serving with --hosted-restore (Hooks.OnHostedRestore); core
// itself rotates only the users accounts' keys and removes their sign-in
// codes and MCP grants. Node passwords stay valid: each agent gets a 401,
// signs in again with its password and pulls its desired document. An
// operator who suspects the passwords were copied too rotates them with
// node.rotate_secret.
func InvalidateNodeTokens(app core.App) error {
	return app.RunInTransaction(func(tx core.App) error {
		nodes, err := tx.FindAllRecords(dropapi.NodeCollection)
		if err != nil {
			return err
		}
		for _, node := range nodes {
			node.RefreshTokenKey()
			if err := tx.Save(node); err != nil {
				return err
			}
		}
		return nil
	})
}
