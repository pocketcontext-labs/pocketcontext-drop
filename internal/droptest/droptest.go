// Package droptest builds Drop's real hosted request path for tests: the
// application's hooks and migrations on an isolated database, core's hosted
// Multi public handler (owner, sign-in limits, OAuth and MCP) and a mailbox
// that captures sign-in emails instead of sending them. Tests in any package
// can use it; it is never linked into the binary.
package droptest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/plugins/jsvm"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/hook"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appconfig"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
)

// Domain is the synthetic hosted domain; OwnerEmail its OWNER_EMAIL;
// AppDomain the domain of app hostnames.
const (
	Domain     = "drop.example.test"
	OwnerEmail = "owner@example.test"
	AppDomain  = "apps.example.test"
)

// Mail is one captured sign-in email: the address, the login request id, the
// 32-digit link password and the six-digit code from the subject.
type Mail struct {
	Email, OTPID, Password, Code string
}

// Options select optional parts of the hosted server.
type Options struct {
	// MCPHandler builds the MCP adapter around the unfiltered router; nil
	// leaves /mcp and OAuth off.
	MCPHandler func(api http.Handler) http.Handler
	// Secrets replaces the in-memory secret store (for example with one that
	// fails); NoSecrets starts the server without a secret store.
	Secrets   awsx.SecretWriter
	NoSecrets bool
}

// Hosted is one synthetic hosted Drop instance.
type Hosted struct {
	T      *testing.T
	App    core.App
	Server *appserver.TestServer
	// Secrets is the in-memory secret store the server writes to (nil with
	// Options.Secrets or NoSecrets). Tests read values back from it; the
	// server itself never can.
	Secrets *awsx.MemSecretStore
	mail    chan Mail
	client  atomic.Int64
}

var codeSubject = regexp.MustCompile(`^(\d{6}) is your Drop sign-in code$`)

// Root is the repository root (pb_hooks, pb_migrations, server.json, ui).
func Root(t testing.TB) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the repository")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// NewHosted starts a hosted Multi test server whose database holds only the
// owner, created by core as hosted startup does for an empty database.
func NewHosted(t *testing.T, opts Options) *Hosted {
	t.Helper()
	root := Root(t)
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	hooks := dropserver.Hooks()
	// As the server does: before maintenance and the JS hooks, then Register.
	if err := hooks.PreRegister(app, nil, appserver.ServeOptions{Hosted: true, HostedConfig: HostedConfig()}); err != nil {
		t.Fatal(err)
	}
	jsvm.MustRegister(app, appconfig.Runtime.HooksConfig(root, ""))
	if err := hooks.Register(app, appserver.RegisterContext{AppDir: root, LivePath: filepath.Join(app.DataDir(), "data.db")}); err != nil {
		t.Fatal(err)
	}
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Close the SQL reader like server shutdown does.
		_ = app.OnTerminate().Trigger(&core.TerminateEvent{App: app})
		_ = app.ResetBootstrapState()
	})
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	h := &Hosted{T: t, App: app, mail: make(chan Mail, 64)}
	services := dropserver.Services{AppDomain: AppDomain}
	switch {
	case opts.NoSecrets:
	case opts.Secrets != nil:
		services.Secrets = opts.Secrets
	default:
		h.Secrets = awsx.NewMemSecretStore()
		services.Secrets = h.Secrets
	}
	dropserver.SetServices(app, services)
	// A late priority captures the email after core rendered the code email;
	// not calling Next means nothing is ever sent.
	app.OnMailerRecordOTPSend("users").Bind(&hook.Handler[*core.MailerRecordEvent]{Id: "droptestMailbox", Priority: 100, Func: func(e *core.MailerRecordEvent) error {
		m := Mail{Email: e.Record.Email()}
		m.OTPID, _ = e.Meta["otpId"].(string)
		m.Password, _ = e.Meta["password"].(string)
		if match := codeSubject.FindStringSubmatch(e.Message.Subject); len(match) == 2 {
			m.Code = match[1]
		}
		h.mail <- m
		return nil
	}})
	options := appserver.TestServerOptions{Domain: Domain, Hosted: true, Cache: t.TempDir(), HostedConfig: HostedConfig()}
	// The MCP adapter calls the unfiltered router, which exists only once the
	// test server is built.
	var router http.Handler
	if opts.MCPHandler != nil {
		options.MCP = true
		options.MCPHandler = opts.MCPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r) }))
	}
	server, err := appconfig.Runtime.NewTestServer(app, options)
	if err != nil {
		t.Fatal(err)
	}
	router = server.Router()
	h.Server = server
	return h
}

// HostedConfig is a synthetic hosted configuration; nothing is ever sent.
func HostedConfig() *appserver.HostedConfig {
	return &appserver.HostedConfig{OwnerEmail: OwnerEmail, From: "login@example.test",
		SMTP: core.SMTPConfig{Enabled: true, Host: "smtp.example.test", Port: 465, TLS: true, Username: "synthetic", Password: "synthetic"}}
}

// Owner returns the owner's account.
func (h *Hosted) Owner() *core.Record {
	h.T.Helper()
	owner, err := h.App.FindAuthRecordByEmail("users", OwnerEmail)
	if err != nil {
		h.T.Fatal("no owner:", err)
	}
	return owner
}

// Request sends one request through the public handler, from a client
// address of its own (core limits sign-in per client address).
func (h *Hosted) Request(method, path, authorization, body string) *httptest.ResponseRecorder {
	h.T.Helper()
	r := httptest.NewRequest(method, "https://"+Domain+path, strings.NewReader(body))
	r.TLS = nil // TLS ends at the proxy in front of hosted plain HTTP
	r.Host = Domain
	r.RemoteAddr = fmt.Sprintf("198.51.100.%d:40000", 1+h.client.Add(1)%250)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	h.Server.Public().ServeHTTP(w, r)
	return w
}

// RequestOTP asks for a sign-in email and returns the login request id that
// every answer carries, whether or not an email was sent.
func (h *Hosted) RequestOTP(email string) string {
	h.T.Helper()
	w := h.Request(http.MethodPost, "/api/collections/users/request-otp", "", fmt.Sprintf(`{"email":%q}`, email))
	if w.Code != http.StatusOK {
		h.T.Fatalf("request-otp %s: %d %s", email, w.Code, w.Body)
	}
	var out struct {
		OTPID string `json:"otpId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.OTPID == "" {
		h.T.Fatalf("request-otp answer %s: %v", w.Body, err)
	}
	return out.OTPID
}

// Mail returns the next captured sign-in email, failing after a few seconds.
func (h *Hosted) Mail() Mail {
	h.T.Helper()
	select {
	case m := <-h.mail:
		return m
	case <-time.After(5 * time.Second):
		h.T.Fatal("no sign-in email sent")
	}
	return Mail{}
}

// NoMail fails if a sign-in email was captured.
func (h *Hosted) NoMail() {
	h.T.Helper()
	select {
	case m := <-h.mail:
		h.T.Fatalf("unexpected sign-in email to %s", m.Email)
	default:
	}
}

// Auth is a signed-in session.
type Auth struct {
	Token  string
	Record map[string]any
}

// SignIn signs an address in the way the reader does: it requests the email,
// then redeems the six-digit code bound to that login request. It fails the
// test if no email is sent.
func (h *Hosted) SignIn(email string) Auth {
	h.T.Helper()
	otpID := h.RequestOTP(email)
	m := h.Mail()
	if !strings.EqualFold(m.Email, email) || m.OTPID != otpID || m.Code == "" {
		h.T.Fatalf("sign-in email for %s does not match the request", email)
	}
	return h.RedeemCode(otpID, m.Code)
}

// RedeemCode exchanges a login request id and its six-digit code for a session.
func (h *Hosted) RedeemCode(otpID, code string) Auth {
	h.T.Helper()
	w := h.Request(http.MethodPost, appserver.OTPCodePath, "", fmt.Sprintf(`{"otpId":%q,"code":%q}`, otpID, code))
	if w.Code != http.StatusOK {
		h.T.Fatalf("redeem code: %d %s", w.Code, w.Body)
	}
	var out Auth
	var raw struct {
		Token  string         `json:"token"`
		Record map[string]any `json:"record"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil || raw.Token == "" {
		h.T.Fatalf("redeem answer %s: %v", w.Body, err)
	}
	out.Token, out.Record = raw.Token, raw.Record
	return out
}

// OAuthToken walks the real MCP OAuth flow for a signed-in session (dynamic
// registration, consent approved with the session token, PKCE code exchange)
// and returns the access token as an Authorization header value. It needs a
// server built with Options.MCPHandler.
func (h *Hosted) OAuthToken(session string) string {
	h.T.Helper()
	callback := "https://client.example.test/callback"
	registered := h.Request("POST", "/oauth/register", "", `{"client_name":"synthetic","redirect_uris":["`+callback+`"],"token_endpoint_auth_method":"none"}`)
	if registered.Code != http.StatusCreated {
		h.T.Fatalf("register: %d %s", registered.Code, registered.Body)
	}
	var client struct {
		ID string `json:"client_id"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &client); err != nil {
		h.T.Fatal(err)
	}
	verifier := strings.Repeat("v", 64)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {client.ID}, "redirect_uri": {callback}, "response_type": {"code"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	approved := h.Request("POST", "/oauth/authorize?"+q.Encode(), session, `{"approve":true}`)
	if approved.Code != http.StatusOK {
		h.T.Fatalf("authorize: %d %s", approved.Code, approved.Body)
	}
	var redirect struct {
		Redirect string `json:"redirect"`
	}
	if err := json.Unmarshal(approved.Body.Bytes(), &redirect); err != nil {
		h.T.Fatal(err)
	}
	target, err := url.Parse(redirect.Redirect)
	if err != nil {
		h.T.Fatal(err)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {client.ID}, "code": {target.Query().Get("code")}, "redirect_uri": {callback}, "code_verifier": {verifier}}
	r := httptest.NewRequest("POST", "https://"+Domain+"/oauth/token", strings.NewReader(form.Encode()))
	r.TLS, r.Host, r.RemoteAddr = nil, Domain, "198.51.100.254:40000"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	exchanged := httptest.NewRecorder()
	h.Server.Public().ServeHTTP(exchanged, r)
	if exchanged.Code != http.StatusOK {
		h.T.Fatalf("token: %d %s", exchanged.Code, exchanged.Body)
	}
	var tokens struct {
		Access string `json:"access_token"`
	}
	if err := json.Unmarshal(exchanged.Body.Bytes(), &tokens); err != nil || tokens.Access == "" {
		h.T.Fatalf("token response: %v %s", err, exchanged.Body)
	}
	return "Bearer " + tokens.Access
}

// MCP sends one JSON-RPC request to /mcp with an OAuth Authorization value.
func (h *Hosted) MCP(authorization, method string, params any) *httptest.ResponseRecorder {
	h.T.Helper()
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	r := httptest.NewRequest("POST", "https://"+Domain+"/mcp", strings.NewReader(string(payload)))
	r.TLS, r.Host, r.RemoteAddr = nil, Domain, "198.51.100.253:40000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", "2025-11-25")
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	h.Server.Public().ServeHTTP(w, r)
	return w
}

// Invite records an open invite for an address as an admin's invite.create
// would (role "user" or "admin", the default quota, seven days), straight in
// the database, and returns its id.
func (h *Hosted) Invite(email, role string) string {
	h.T.Helper()
	collection, err := h.App.FindCollectionByNameOrId("invites")
	if err != nil {
		h.T.Fatal(err)
	}
	invite := core.NewRecord(collection)
	invite.Set("email", strings.ToLower(email))
	invite.Set("email_lower", strings.ToLower(email))
	invite.Set("role", role)
	invite.Set("app_quota", 3)
	invite.Set("invited_by", h.Owner().Id)
	invite.Set("expires_at", time.Now().UTC().Add(7*24*time.Hour))
	if err := h.App.Save(invite); err != nil {
		h.T.Fatal(err)
	}
	return invite.Id
}

// User creates a signed-in (verified) account for an address with a role, as
// an invite and a first sign-in would, and returns it with an auth token.
func (h *Hosted) User(email, role string) (*core.Record, string) {
	h.T.Helper()
	h.Invite(email, role)
	users, err := h.App.FindCollectionByNameOrId("users")
	if err != nil {
		h.T.Fatal(err)
	}
	user := core.NewRecord(users)
	user.SetEmail(email)
	user.SetRandomPassword()
	user.SetVerified(true)
	if err := h.App.Save(user); err != nil {
		h.T.Fatal(err)
	}
	return user, h.Token(user)
}

// Token issues an auth token for an account.
func (h *Hosted) Token(user *core.Record) string {
	h.T.Helper()
	token, err := user.NewAuthToken()
	if err != nil {
		h.T.Fatal(err)
	}
	return token
}

// OpResult is one answer of POST /api/drop/op.
type OpResult struct {
	Status   int
	Result   map[string]any
	Replayed bool
	Error    map[string]any // the error body when Status is not 200
	Raw      string
}

// Op calls one operation through the public handler with an Authorization
// value (empty for anonymous) and an optional idempotency key.
func (h *Hosted) Op(authorization, op string, input any, idempotencyKey ...string) OpResult {
	h.T.Helper()
	body := map[string]any{"op": op}
	if input != nil {
		body["input"] = input
	}
	if len(idempotencyKey) > 0 {
		body["idempotency_key"] = idempotencyKey[0]
	}
	raw, err := json.Marshal(body)
	if err != nil {
		h.T.Fatal(err)
	}
	w := h.Request(http.MethodPost, "/api/drop/op", authorization, string(raw))
	out := OpResult{Status: w.Code, Raw: w.Body.String()}
	if w.Code == http.StatusOK {
		var ok struct {
			Result   map[string]any `json:"result"`
			Replayed bool           `json:"replayed"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &ok); err != nil {
			h.T.Fatalf("%s answer %s: %v", op, w.Body, err)
		}
		out.Result, out.Replayed = ok.Result, ok.Replayed
	} else {
		_ = json.Unmarshal(w.Body.Bytes(), &out.Error)
	}
	return out
}

// MustOp is Op that fails the test unless the operation succeeds.
func (h *Hosted) MustOp(authorization, op string, input any) map[string]any {
	h.T.Helper()
	r := h.Op(authorization, op, input)
	if r.Status != http.StatusOK {
		h.T.Fatalf("%s: %d %s", op, r.Status, r.Raw)
	}
	return r.Result
}
