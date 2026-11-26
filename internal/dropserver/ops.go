package dropserver

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// The operation framework: POST /api/drop/op dispatches one registered
// operation by name. Account operations go through it, from the reader, the
// CLI and (M6) the MCP tool drop_write, so roles and ownership are checked in
// one place:
//
//  1. The caller is a users account with a valid token, not suspended (401,
//     403 otherwise; node tokens get 403).
//  2. The operation exists (400 unknown_op) and the caller's role may call it
//     (403 forbidden; admins may call every operation).
//  3. The input decodes into the operation's input type with unknown fields
//     refused (400 invalid_input).
//  4. A read operation runs on the app. Any other runs in one transaction,
//     which also writes exactly one events row (Ctx.Event) and, with an
//     idempotency key, the replay record.
//
// Register an operation with register in an init function, after adding its
// Spec to internal/dropapi; TestEveryOperationHasAHandler keeps them equal.

// Ctx is one operation call.
type Ctx struct {
	// App is the transaction of a mutating operation, or the app for a read.
	// Use it for every query and save so the operation stays atomic.
	App core.App
	// Auth is the caller's users record, loaded for this request.
	Auth *core.Record
	// Admin is true for role admin (and always for the owner); Owner for the
	// instance owner (Runtime.IsOwner).
	Admin, Owner bool
	// Request is the HTTP request, for reading only.
	Request         *http.Request
	Spec            dropapi.Spec
	creditKey       string
	event           *Event
	deleteParameter string               // parameter queued for cleanup after commit
	metricsFilter   *metricsFilterAction // per-app metrics filter to apply after commit
}

// Event is the events row an operation writes. Action defaults to the
// operation name. Detail must never hold a secret value; it is at most 8 KiB
// of JSON.
type Event struct {
	Action      string
	App         string
	SubjectUser string
	Detail      map[string]any
}

// Event sets the events row this operation writes when it succeeds. Calling
// it again replaces the earlier row; not calling it writes {action: op}.
func (c *Ctx) Event(e Event) { c.event = &e }

// handler runs one operation with its decoded input (a pointer to the spec's
// input type) and returns the result to encode.
type handler func(c *Ctx, input any) (any, error)

var handlers = map[string]handler{}

// register adds the handler of an operation whose Spec exists in dropapi
// with input type In. It panics on a mismatch, at start.
func register[In any](name string, run func(c *Ctx, in *In) (any, error)) {
	spec, ok := dropapi.Lookup(name)
	if !ok {
		panic("dropserver: no dropapi spec for operation " + name)
	}
	if reflect.TypeOf(spec.Input) != reflect.TypeOf((*In)(nil)) {
		panic(fmt.Sprintf("dropserver: operation %s takes %T", name, spec.Input))
	}
	if _, dup := handlers[name]; dup {
		panic("dropserver: operation registered twice: " + name)
	}
	handlers[name] = func(c *Ctx, input any) (any, error) { return run(c, input.(*In)) }
}

// registerOpRoutes adds POST /api/drop/op and GET /api/drop/me.
func registerOpRoutes(e *core.ServeEvent) {
	var keys sync.Mutex // serializes calls that carry an idempotency key
	e.Router.POST(dropapi.Route, func(re *core.RequestEvent) error {
		return serveOp(re, &keys)
	})
	e.Router.GET(dropapi.MeRoute, func(re *core.RequestEvent) error {
		c, err := caller(re)
		if err != nil {
			return writeError(re, err)
		}
		out, err := me(c)
		if err != nil {
			return writeError(re, err)
		}
		return re.JSON(http.StatusOK, out)
	})
}

// caller checks the request's account and builds the call context.
func caller(re *core.RequestEvent) (*Ctx, error) {
	if re.Auth == nil {
		return nil, &dropapi.Error{Status: http.StatusUnauthorized, Code: dropapi.CodeUnauthenticated, Message: "Sign in first."}
	}
	if re.Auth.Collection().Name != "users" {
		return nil, Forbidden("Only Drop accounts can use operations.")
	}
	return accountContext(re.App, re.Auth, re.Request)
}

func accountContext(app core.App, account *core.Record, request *http.Request) (*Ctx, error) {
	if account.GetBool("suspended") {
		return nil, &dropapi.Error{Status: http.StatusForbidden, Code: dropapi.CodeSuspended, Message: "This account is suspended."}
	}
	owner := isOwner(app, account)
	return &Ctx{
		App: app, Auth: account, Request: request, Owner: owner,
		Admin: owner || account.GetString("role") == string(dropapi.RoleAdmin),
	}, nil
}

func (c *Ctx) checkRole(spec dropapi.Spec) error {
	role := dropapi.RoleUser
	if c.Admin {
		if !accountsOf(c.App).hosted {
			return nil
		}
		role = dropapi.RoleAdmin
	}
	if !spec.Allows(role) {
		return Forbidden("This operation needs a " + string(spec.Role) + " account.")
	}
	return nil
}

// authorizeOp reloads the account after the request body has arrived. For writes
// the lookup and all protected effects share the same database transaction.
func authorizeOp(app core.App, re *core.RequestEvent, spec dropapi.Spec) (*Ctx, error) {
	account, err := app.FindRecordById("users", re.Auth.Id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if account == nil || account.TokenKey() != re.Auth.TokenKey() {
		return nil, &dropapi.Error{Status: http.StatusUnauthorized, Code: dropapi.CodeUnauthenticated, Message: "Sign in first."}
	}
	c, err := accountContext(app, account, re.Request)
	if err != nil {
		return nil, err
	}
	if err := c.checkRole(spec); err != nil {
		return nil, err
	}
	c.Spec = spec
	return c, nil
}

func serveOp(re *core.RequestEvent, keys *sync.Mutex) error {
	c, err := caller(re)
	if err != nil {
		return writeError(re, err)
	}
	body, err := io.ReadAll(http.MaxBytesReader(re.Response, re.Request.Body, dropapi.MaxRequestBytes))
	if err != nil {
		return writeError(re, &dropapi.Error{Status: http.StatusRequestEntityTooLarge, Code: dropapi.CodeInvalidRequest, Message: "The request is too large."})
	}
	var req dropapi.Request
	if err := decodeStrict(body, &req); err != nil {
		return writeError(re, &dropapi.Error{Status: http.StatusBadRequest, Code: dropapi.CodeInvalidRequest, Message: "The body must be {op, input, idempotency_key}."})
	}
	spec, ok := dropapi.Lookup(req.Op)
	run := handlers[req.Op]
	if !ok || run == nil {
		return writeError(re, &dropapi.Error{Status: http.StatusBadRequest, Code: dropapi.CodeUnknownOp, Message: "Unknown operation.", Data: map[string]any{"op": req.Op}})
	}
	if err := c.checkRole(spec); err != nil {
		return writeError(re, err)
	}
	input := reflect.New(reflect.TypeOf(spec.Input).Elem()).Interface()
	raw := bytes.TrimSpace(req.Input)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	if raw[0] != '{' || decodeStrict(raw, input) != nil {
		return writeError(re, &dropapi.Error{Status: http.StatusBadRequest, Code: dropapi.CodeInvalidInput, Message: "The input does not match the operation.", Data: map[string]any{"op": req.Op}})
	}
	// A standalone operation makes its own transactions and writes its own
	// event (Spec.Standalone); like a read it ignores idempotency keys.
	if spec.Read || spec.Standalone {
		c, err := authorizeOp(re.App, re, spec)
		if err != nil {
			return writeError(re, err)
		}
		result, err := run(c, input)
		if err != nil {
			return writeError(re, err)
		}
		return writeResult(re, result, false)
	}
	var key *opKey
	// An input holding a secret value is never hashed or stored: its key is
	// ignored, like a read operation's.
	if req.IdempotencyKey != "" && !spec.SecretInput {
		if !dropapi.IdempotencyKeyPattern.MatchString(req.IdempotencyKey) {
			return writeError(re, &dropapi.Error{Status: http.StatusBadRequest, Code: dropapi.CodeInvalidIdempotencyKey, Message: "An idempotency key is 1-128 letters, digits, '.', '_', ':' or '-'."})
		}
		canonical, err := json.Marshal(input)
		if err != nil {
			return writeError(re, err)
		}
		key = newOpKey(c.Auth.Id, req.IdempotencyKey, spec.Name, canonical)
		keys.Lock()
		defer keys.Unlock()
	}
	var encoded json.RawMessage
	replayed := false
	err = re.App.RunInTransaction(func(tx core.App) error {
		var err error
		c, err = authorizeOp(tx, re, spec)
		if err != nil {
			return err
		}
		if key != nil {
			c.creditKey = key.keyHash
			stored, found, err := key.lookup(tx)
			if err != nil || found {
				encoded, replayed = stored, found
				return err
			}
		}
		result, err := run(c, input)
		if err != nil {
			return err
		}
		// Every change that alters a node's desired document bumps its
		// desired_seq in the same transaction (seq.go).
		if err := syncDesiredSeqs(tx); err != nil {
			return err
		}
		if encoded, err = json.Marshal(result); err != nil {
			return err
		}
		e := Event{Action: spec.Name}
		if c.event != nil {
			e = *c.event
			if e.Action == "" {
				e.Action = spec.Name
			}
		}
		if err := WriteEvent(tx, EventRecord{Actor: c.Auth.Id, App: e.App, SubjectUser: e.SubjectUser, Action: e.Action, Detail: e.Detail}); err != nil {
			return err
		}
		if key != nil {
			return key.store(tx, encoded, !spec.OnceResult)
		}
		return nil
	})
	if err != nil {
		return writeError(re, err)
	}
	if c.deleteParameter != "" {
		_ = finishSecretDeletion(re.Request.Context(), re.App, c.deleteParameter) // queued for retry on failure
	}
	if c.metricsFilter != nil {
		applyMetricsFilter(re.Request.Context(), re.App, c.Auth.Id, *c.metricsFilter) // best effort; the collector reconciles
	}
	return writeResult(re, encoded, replayed)
}

// decodeStrict decodes exactly one JSON value with unknown fields refused.
func decodeStrict(data []byte, into any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return err
	}
	if d.More() {
		return errors.New("trailing data")
	}
	var extra json.RawMessage
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

func writeResult(re *core.RequestEvent, result any, replayed bool) error {
	raw, ok := result.(json.RawMessage)
	if !ok {
		var err error
		if raw, err = json.Marshal(result); err != nil {
			return writeError(re, err)
		}
	}
	re.Response.Header().Set("Cache-Control", "no-store")
	return re.JSON(http.StatusOK, dropapi.Response{Result: raw, Replayed: replayed})
}

// writeError answers a *dropapi.Error as is and anything else as a 500 whose
// details stay in the server log (never the input, which may hold a secret).
func writeError(re *core.RequestEvent, err error) error {
	var e *dropapi.Error
	if !errors.As(err, &e) {
		re.App.Logger().Error("Drop operation failed", "error", err.Error())
		e = &dropapi.Error{Status: http.StatusInternalServerError, Code: dropapi.CodeInternal, Message: "Something went wrong. Please retry."}
	}
	out := *e
	if out.Data == nil {
		out.Data = map[string]any{}
	}
	re.Response.Header().Set("Cache-Control", "no-store")
	return re.JSON(out.Status, out)
}

// Error helpers for operations.

// Invalid is a 400 for one input field.
func Invalid(field, message string) *dropapi.Error {
	return &dropapi.Error{Status: http.StatusBadRequest, Code: dropapi.CodeInvalidInput, Message: message, Data: map[string]any{"field": field}}
}

// NotFound is a 404. Use it for records the caller may not see as well, so
// existence never leaks.
func NotFound(what string) *dropapi.Error {
	return &dropapi.Error{Status: http.StatusNotFound, Code: dropapi.CodeNotFound, Message: what + " not found."}
}

// Forbidden is a 403 for an action the caller's role does not allow.
func Forbidden(message string) *dropapi.Error {
	return &dropapi.Error{Status: http.StatusForbidden, Code: dropapi.CodeForbidden, Message: message}
}

// Conflict is a 409 with a stable code.
func Conflict(code, message string) *dropapi.Error {
	return &dropapi.Error{Status: http.StatusConflict, Code: code, Message: message}
}

// Ownership helpers.

// LoadOwned returns the record of collection whose ownerField is the caller,
// or a 404 (the same answer for a missing record and another account's).
func (c *Ctx) LoadOwned(collection, id, ownerField, what string) (*core.Record, error) {
	return c.load(collection, id, ownerField, what, false)
}

// LoadVisible is LoadOwned that also lets an admin load any record.
func (c *Ctx) LoadVisible(collection, id, ownerField, what string) (*core.Record, error) {
	return c.load(collection, id, ownerField, what, true)
}

func (c *Ctx) load(collection, id, ownerField, what string, admins bool) (*core.Record, error) {
	if !recordID.MatchString(id) {
		return nil, NotFound(what)
	}
	record, err := c.App.FindRecordById(collection, id)
	if err != nil {
		return nil, NotFound(what)
	}
	if record.GetString(ownerField) != c.Auth.Id && !(admins && c.Admin) {
		return nil, NotFound(what)
	}
	return record, nil
}
