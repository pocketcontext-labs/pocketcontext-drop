package dropserver

import (
	"errors"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

// desired_seq: each node's counter of desired document versions. It goes up
// exactly when the node's desired document (desired.go) changes content, so
// the agent, which subscribes to its own nodes record (realtime nodes/<id>),
// wakes for every change that alters what it must run and for nothing else.
//
// Rather than listing which writes matter, every mutating operation (ops.go)
// and the upload route (upload.go) call syncDesiredSeqs in their transaction
// after their change: it rebuilds each node's document and bumps desired_seq
// where the content hash (stored in desired_hash) differs. That covers
// app.set, app.start and app.stop, archive and restore, secret.set and
// secret.delete (a new version or a removed name), release.deploy and
// activating uploads, a node's allocatable changing what passes the check,
// and placements, including both the old and the new node when an app moves.
// The bump commits with the change or not at all. Nodes are few; rebuilding
// each one's document is a handful of queries.

// syncDesiredSeqs bumps the desired_seq of every node whose desired document
// changed, or only of the nodes in ids when given.
func syncDesiredSeqs(tx core.App, ids ...string) error {
	var nodes []*core.Record
	var err error
	if len(ids) > 0 {
		nodes, err = tx.FindRecordsByIds("nodes", ids)
	} else {
		nodes, err = tx.FindAllRecords("nodes")
	}
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if _, err := syncNode(tx, node); err != nil {
			return err
		}
	}
	return nil
}

// syncNode bumps one node's desired_seq when its document changed and
// reports whether it did. A document the renderer refuses as a whole is
// logged and leaves the counter alone: the agent keeps what it runs.
func syncNode(tx core.App, node *core.Record) (bool, error) {
	d, err := buildDesired(tx, node)
	if err != nil {
		if isRenderRefusal(err) {
			tx.Logger().Error("Drop desired document refused", "node", node.Id, "error", err.Error())
			return false, nil
		}
		return false, err
	}
	if d.ContentHash == node.GetString("desired_hash") {
		return false, nil
	}
	// The field's maximum (999999999, render's limit) refuses a save beyond it.
	node.Set("desired_seq", node.GetInt("desired_seq")+1)
	node.Set("desired_hash", d.ContentHash)
	return true, tx.Save(node)
}

func isRenderRefusal(err error) bool {
	var refusal *render.Error
	return errors.As(err, &refusal)
}
