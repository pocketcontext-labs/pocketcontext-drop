package dropserver

import (
	"encoding/json"

	"ganigeorgiev.com/pocketbase/dbx"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// Event reads (M6). Visibility follows the events collection rule: an account
// sees the events of its own apps; admins see an app's events by naming it,
// and every event (account events too) with all.

func init() {
	register("event.list", eventList)
}

func eventList(c *Ctx, in *dropapi.EventListInput) (any, error) {
	limit, offset, err := page(in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	if in.All && !c.Admin {
		return nil, Forbidden("Only admins list every event.")
	}
	if in.All && in.App != "" {
		return nil, Invalid("all", "Name an app or ask for all events, not both.")
	}
	filter, params := "", dbx.Params{}
	switch {
	case in.App != "":
		app, err := c.findApp(in.App, false)
		if err != nil {
			return nil, err
		}
		filter, params["app"] = "app = {:app}", app.Id
	case !in.All:
		filter, params["owner"] = "app != '' && app.owner = {:owner}", c.Auth.Id
	}
	records, err := c.App.FindRecordsByFilter("events", filter, "-created,-id", limit+1, offset, params)
	if err != nil {
		return nil, err
	}
	out := dropapi.EventList{Events: []dropapi.EventEntry{}, HasMore: len(records) > limit}
	emails := map[string]string{c.Auth.Id: c.Auth.Email()}
	for i, r := range records {
		if i == limit {
			break
		}
		actor := r.GetString("actor")
		email, known := emails[actor]
		if !known && c.Admin && actor != "" {
			if u, err := c.App.FindRecordById("users", actor); err == nil {
				email = u.Email()
			}
			emails[actor] = email
		}
		detail := map[string]any{}
		if raw, err := json.Marshal(r.Get("detail")); err == nil {
			_ = json.Unmarshal(raw, &detail)
		}
		if detail == nil {
			detail = map[string]any{}
		}
		out.Events = append(out.Events, dropapi.EventEntry{
			ID: r.Id, Action: r.GetString("action"), Actor: actor, ActorEmail: email,
			App: r.GetString("app"), SubjectUser: r.GetString("subject_user"), Detail: detail,
			Created: dateString(r, "created"),
		})
	}
	return out, nil
}
