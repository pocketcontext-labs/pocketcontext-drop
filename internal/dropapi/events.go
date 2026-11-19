package dropapi

// Event reads (milestone M6, for drop_read what=events and the reader).

// EventListInput selects one page of events, newest first: those of the
// caller's own apps, of one app (App, by id or slug; admins may name any
// app), or with All (admins only) every event, including account events.
type EventListInput struct {
	App    string `json:"app,omitempty"`
	All    bool   `json:"all,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

// EventEntry is one events row. ActorEmail is filled for the caller's own
// actions and, for admins, every account's. Detail never holds a secret value.
type EventEntry struct {
	ID          string         `json:"id"`
	Action      string         `json:"action"`
	Actor       string         `json:"actor"`
	ActorEmail  string         `json:"actor_email"`
	App         string         `json:"app"`
	SubjectUser string         `json:"subject_user"`
	Detail      map[string]any `json:"detail"`
	Created     string         `json:"created"`
}

// EventList is a page of events.
type EventList struct {
	Events  []EventEntry `json:"events"`
	HasMore bool         `json:"has_more"`
}
