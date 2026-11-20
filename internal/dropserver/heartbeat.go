package dropserver

import (
	"net"
	"sync"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"golang.org/x/time/rate"
)

// Heartbeats. An agent reports every 30 seconds (reports.go). Writing each
// report to the database would make the synced database write back all the
// time, so the server keeps the last report time of each node in memory and
// persists the node's fields only when they change (agent version, accepted
// revision, nibrunner's state, capacity, allocatable or versions) or when
// HeartbeatPersistEvery has passed since the last write. In steady state that
// is one write per node every five minutes. node.list and status read the
// in-memory time first and fall back to the stored last_heartbeat after a
// restart.

// HeartbeatPersistEvery is the longest a node's stored heartbeat lags.
const HeartbeatPersistEvery = 5 * time.Minute

// agentState is the server's in-memory state of its nodes, one per app.
type agentState struct {
	mu    sync.Mutex
	now   func() time.Time
	beats map[string]*beat
	// routes limits each node on the agent routes; signIn limits each client
	// address on the node sign-in routes (core's hosted budget counts users
	// accounts only).
	routes map[string]*rate.Limiter
	signIn *clientLimiter
}

// beat is one node's last report: when it arrived, when the node's fields
// were last written and what they were.
type beat struct {
	seen, persisted time.Time
	fingerprint     string
}

const agentStateKey = "dropAgentState"

func newAgentState() *agentState {
	return &agentState{now: time.Now, beats: map[string]*beat{}, routes: map[string]*rate.Limiter{},
		signIn: newClientLimiter(rate.Every(6*time.Second), 10)}
}

// agentStateOf returns the app's agent state, creating it on first use.
func agentStateOf(app core.App) *agentState {
	return app.Store().GetOrSet(agentStateKey, func() any { return newAgentState() }).(*agentState)
}

// SetAgentClock replaces the clock of the agent routes (tests).
func SetAgentClock(app core.App, now func() time.Time) {
	s := agentStateOf(app)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

func (s *agentState) clock() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now()
}

// seen records a report of node at now and reports whether the node's fields
// must be written: they changed (fingerprint) or the last write is older than
// HeartbeatPersistEvery. The caller confirms a write with persisted.
func (s *agentState) seen(node, fingerprint string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.beats[node]
	if b == nil {
		b = &beat{}
		s.beats[node] = b
	}
	b.seen = now
	return b.persisted.IsZero() || b.fingerprint != fingerprint || now.Sub(b.persisted) >= HeartbeatPersistEvery
}

// persisted records that node's fields were written at now.
func (s *agentState) persisted(node, fingerprint string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.beats[node]; b != nil {
		b.persisted, b.fingerprint = now, fingerprint
	}
}

// forget drops a node's in-memory heartbeat, so its next report is written
// (after its password or state changed).
func (s *agentState) forget(node string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.beats, node)
}

// lastSeen is when node last reported: in memory, else its stored
// last_heartbeat.
func (s *agentState) lastSeen(node *core.Record) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.beats[node.Id]
	if b != nil && !b.seen.IsZero() {
		return b.seen
	}
	return node.GetDateTime("last_heartbeat").Time()
}

// allowRoute spends one agent route request of node: 30 at once, then one a
// second.
func (s *agentState) allowRoute(node string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.routes[node]
	if l == nil {
		l = rate.NewLimiter(rate.Every(time.Second), 30)
		s.routes[node] = l
	}
	return l.AllowN(s.now(), 1)
}

// clientLimiter is a token bucket per client address; idle addresses are
// forgotten after ten minutes.
type clientLimiter struct {
	mu      sync.Mutex
	every   rate.Limit
	burst   int
	clients map[string]*uploadClient
}

func newClientLimiter(every rate.Limit, burst int) *clientLimiter {
	return &clientLimiter{every: every, burst: burst, clients: map[string]*uploadClient{}}
}

func (l *clientLimiter) allow(remoteAddr string, now time.Time) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.clients) > 4096 {
		for k, c := range l.clients {
			if now.Sub(c.seen) > 10*time.Minute {
				delete(l.clients, k)
			}
		}
	}
	c := l.clients[host]
	if c == nil {
		c = &uploadClient{limiter: rate.NewLimiter(l.every, l.burst)}
		l.clients[host] = c
	}
	c.seen = now
	return c.limiter.AllowN(now, 1)
}
