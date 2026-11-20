package dropserver

import (
	"encoding/json"
	"fmt"
	"regexp"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
)

var recordID = regexp.MustCompile(`^[a-z0-9]{15}$`)

// MaxEventDetailBytes bounds an event's detail JSON.
const MaxEventDetailBytes = 8 << 10

// EventRecord is one events row. Actor is the users account that acted, or
// ActorNode the node (the agent routes); App and SubjectUser name what it
// acted on, when anything. Detail never holds a secret value (names and
// lengths only).
type EventRecord struct {
	Actor, ActorNode, App, SubjectUser, Action string
	Detail                                     map[string]any
}

// WriteEvent writes one events row with app, normally the operation's
// transaction. The operation framework calls it for every mutating
// operation; routes outside it (uploads, the agent) call it themselves.
func WriteEvent(app core.App, e EventRecord) error {
	collection, err := app.FindCachedCollectionByNameOrId("events")
	if err != nil {
		return err
	}
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	if len(raw) > MaxEventDetailBytes {
		return fmt.Errorf("event %s: detail is %d bytes, at most %d", e.Action, len(raw), MaxEventDetailBytes)
	}
	record := core.NewRecord(collection)
	record.Set("actor", e.Actor)
	record.Set("actor_node", e.ActorNode)
	record.Set("app", e.App)
	record.Set("subject_user", e.SubjectUser)
	record.Set("action", e.Action)
	record.Set("detail", json.RawMessage(raw))
	return app.Save(record)
}
