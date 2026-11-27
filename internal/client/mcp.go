package client

// The MCP adapter serves Drop's two tools, drop_read and drop_write
// (drop_mcp.go), through the ordinary authenticated API with the caller's own
// session: no shell, no filesystem, no administration. Each tool call reaches
// only POST /api/drop/op, in-process, so the server checks the account, its
// role, developer capability and ownership on every call. Each request gets
// tool schemas through GET /api/drop/me with the caller's current rights.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pocketcontext-labs/pocketcontext-core/appserver"
	"github.com/pocketcontext-labs/pocketcontext-core/mcpobserve"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appconfig"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droppolicy"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
)

const (
	mcpInputLimit    = 8 << 20
	mcpResultLimit   = 6 << 20
	mcpAPILimit      = 8 << 20
	mcpRequestBudget = 64
	mcpByteBudget    = 32 << 20
)

type mcpRequestContextKey struct{}
type mcpCaller struct {
	ctx   context.Context
	host  string
	token string
}

func newMCPHandler(api http.Handler) http.Handler {
	servers := make(map[mcpProfile]*mcp.Server)
	for _, developer := range []bool{false, true} {
		for _, profile := range []mcpProfile{
			{role: dropapi.RoleUser, developer: developer},
			{role: dropapi.RoleUser, developer: developer, official: true},
			{role: dropapi.RoleAdmin, developer: developer},
		} {
			servers[profile] = makeDropMCPServer(api, profile)
		}
	}
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		caller, _ := r.Context().Value(mcpRequestContextKey{}).(*mcpCaller)
		me := mcpAccount(api, caller)
		profile := mcpProfile{role: dropapi.RoleUser, developer: me.Developer}
		if me.Admin {
			profile.role = dropapi.RoleAdmin
		} else {
			profile.official = me.OfficialCatalog
		}
		return servers[profile]
	}, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, MaxRequestBodyBytes: mcpInputLimit, PropagateRequestCancellation: true,
		// The wrapper below requires the stronger exact-origin boundary and OAuth.
		DisableLocalhostProtection: true,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := appserver.MCPAuthToken(r.Context())
		if token == "" {
			http.Error(w, "MCP authorization required", http.StatusUnauthorized)
			return
		}
		if !appconfig.ValidatedMCPPublicRequest(r) {
			http.Error(w, "Configured public MCP origin required", http.StatusForbidden)
			return
		}
		// No protocol session retains credentials: the caller is bound to this request.
		caller := &mcpCaller{ctx: r.Context(), host: r.Host, token: token}
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), mcpRequestContextKey{}, caller)))
	})
}

// mcpAccount asks GET /api/drop/me, with the caller's own token, which
// server to show. Anything but a confirmed admin gets the user server; the
// role only shapes what is listed, since every call is checked again.
func mcpAccount(api http.Handler, caller *mcpCaller) dropapi.Me {
	if caller == nil || caller.token == "" {
		return dropapi.Me{}
	}
	r, err := http.NewRequestWithContext(caller.ctx, http.MethodGet, "https://"+caller.host+dropapi.MeRoute, http.NoBody)
	if err != nil {
		return dropapi.Me{}
	}
	out := &mcpResponse{header: make(http.Header)}
	api.ServeHTTP(out, mcpInnerRequest(r, caller))
	var me dropapi.Me
	if out.err != nil || out.status != http.StatusOK || json.Unmarshal(out.body.Bytes(), &me) != nil {
		return dropapi.Me{}
	}
	return me
}

// mcpInnerRequest makes an in-process API request carry only the caller's
// own token, from an address that never qualifies for local administration,
// marked as coming through MCP (a secret's set_via).
func mcpInnerRequest(inner *http.Request, caller *mcpCaller) *http.Request {
	inner.URL.Scheme, inner.URL.Host = "https", caller.host
	inner.Host = caller.host
	inner.RequestURI = inner.URL.RequestURI()
	inner.RemoteAddr = "192.0.2.1:0" // never qualifies for local administration
	inner.Header.Set("Authorization", caller.token)
	inner.Header.Del(appconfig.Identity.LocalKeyHeader)
	return dropserver.WithVia(inner, dropapi.SetViaMCP)
}

// mcpResponse bounds API output before it is buffered.
type mcpResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
	err    error
}

func (w *mcpResponse) Header() http.Header { return w.header }
func (w *mcpResponse) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}
func (w *mcpResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.err != nil {
		return 0, w.err
	}
	if w.body.Len()+len(p) > mcpAPILimit {
		w.err = mcpDiagnosticError("limit_exceeded", "API result exceeds MCP limit; narrow the read")
		return 0, w.err
	}
	return w.body.Write(p)
}

// mcpTransport dispatches a tool's API call straight to the application router,
// with the caller's ordinary token, within the bounds of one tool call.
type mcpTransport struct {
	api      http.Handler
	caller   *mcpCaller
	ctx      context.Context
	requests atomic.Int64
	bytes    atomic.Int64
}

func (t *mcpTransport) RoundTrip(req *http.Request) (response *http.Response, err error) {
	observation := mcpObservationFrom(t.ctx)
	route := mcpAPIRoute(req.Method, req.URL.Path)
	started := time.Now()
	status, size, dispatched := 0, int64(0), false
	defer func() {
		if observation != nil {
			observation.api(req.Method, route, status, size, dispatched, err)
		}
		if err != nil || status >= 400 {
			mcpobserve.Emit(t.ctx, mcpobserve.Event{Event: "mcp_api", Method: req.Method, Outcome: "error", Reason: mcpFailureReason(err, status), APIRoute: route, APIStatus: status, DurationMS: time.Since(started).Milliseconds(), ResponseBytes: size})
		}
	}()
	if route == "unknown" {
		return nil, mcpDiagnosticError("invalid_input", "API operation is not available through MCP")
	}
	if req.URL.RawQuery != "" {
		return nil, mcpDiagnosticError("invalid_input", "API query parameters are not available through MCP")
	}
	if t.requests.Add(1) > mcpRequestBudget {
		return nil, mcpDiagnosticError("limit_exceeded", "MCP operation request budget exceeded")
	}
	ctx, cancel := context.WithCancel(t.ctx)
	defer cancel()
	stopCaller := context.AfterFunc(t.caller.ctx, cancel)
	defer stopCaller()
	stopRequest := context.AfterFunc(req.Context(), cancel)
	defer stopRequest()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	inner := req.Clone(ctx)
	// Server requests always have a body; the router closes it after every request.
	if inner.Body == nil {
		inner.Body = http.NoBody
	}
	inner = mcpInnerRequest(inner, t.caller)
	out := &mcpResponse{header: make(http.Header)}
	dispatched = true
	t.api.ServeHTTP(out, inner)
	status, size = out.status, int64(out.body.Len())
	if status == 0 {
		status = http.StatusOK
	}
	if out.err != nil {
		return nil, out.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.bytes.Add(int64(out.body.Len())) > mcpByteBudget {
		return nil, mcpDiagnosticError("limit_exceeded", "MCP operation response budget exceeded; narrow the query")
	}
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Header: out.header, Body: io.NopCloser(bytes.NewReader(out.body.Bytes())), ContentLength: int64(out.body.Len()), Request: req}, nil
}

// mcpAPIRoute returns the fixed label of an API call the tools may make (an
// entry of droppolicy.Observe.APIRoutes), or "unknown" for anything else.
// Labels contain no record IDs or content.
func mcpAPIRoute(method, path string) string {
	if method == http.MethodPost && slices.Contains(droppolicy.Observe.APIRoutes, path) {
		return path
	}
	return "unknown"
}

func mcpClient(ctx context.Context, api http.Handler) (*Client, error) {
	caller, ok := ctx.Value(mcpRequestContextKey{}).(*mcpCaller)
	if !ok || caller.token == "" {
		return nil, mcpDiagnosticError("unauthorized", "MCP authorization required")
	}
	// Remote tools never inherit host configuration or local administration keys.
	return &Client{Base: "https://" + caller.host, token: caller.token,
		http: &http.Client{Transport: &mcpTransport{api: api, caller: caller, ctx: ctx}, Timeout: 45 * time.Second, CheckRedirect: noRedirect}}, nil
}
