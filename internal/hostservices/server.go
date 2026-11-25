package hostservices

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The header nibrunnerd sends before relaying a guest connection:
// "CONNECT <appId> <deploymentId>\n". nibrunnerd fixes both from the jail the
// guest connected through; nothing in it comes from the guest.
var headerPattern = regexp.MustCompile(`^CONNECT (d-[a-z0-9]{15}) (d-[a-z0-9]{15}-dep-[0-9]{1,9})\n$`)

const (
	maxHeaderLine     = 128
	headerTimeout     = 5 * time.Second
	DefaultMaxConns   = 512
	DefaultMaxPerApp  = 32
	DefaultSocketPath = "/run/drop-agent/host-services.sock"
)

// Lookup returns the app a current deployment belongs to, or false when the
// deployment is not the app's current one with host services enabled.
type Lookup func(appID, deploymentID string) (App, bool)

// Server accepts nibrunnerd's forwarded guest connections on a Unix socket.
type Server struct {
	Socket     string
	PeerUID    uint32 // only connections from this uid (nibrunnerd's) are accepted
	Lookup     Lookup
	Proxy      *Proxy
	MaxConns   int
	MaxPerApp  int
	Logf       func(string, ...any)
	peerFilter func(*net.UnixConn) error // tests replace the SO_PEERCRED check

	mu     sync.Mutex
	active map[string]int
	total  int
}

type connKey struct{}

// guestConn is one forwarded connection after its header.
type guestConn struct {
	net.Conn
	r            *bufio.Reader
	appID        string
	deploymentID string
	release      func()
	once         sync.Once
}

func (c *guestConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *guestConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}

// chanListener hands handshaken connections to http.Server.
type chanListener struct {
	conns chan net.Conn
	addr  net.Addr
	done  chan struct{}
	once  sync.Once
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// Listen creates the socket: its directory 0700, the socket 0600, both
// root's. A stale socket from a previous run is replaced.
func (s *Server) Listen() (*net.UnixListener, error) {
	if s.Socket == "" || !filepath.IsAbs(s.Socket) {
		return nil, errors.New("the host services socket must be an absolute path")
	}
	if err := os.MkdirAll(filepath.Dir(s.Socket), 0o700); err != nil {
		return nil, err
	}
	if st, err := os.Lstat(s.Socket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", s.Socket)
		}
		if err := os.Remove(s.Socket); err != nil {
			return nil, err
		}
	}
	old := syscall.Umask(0o177)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.Socket, Net: "unix"})
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	l.SetUnlinkOnClose(true)
	return l, nil
}

// Serve accepts until ctx ends.
func (s *Server) Serve(ctx context.Context, l *net.UnixListener) error {
	if s.MaxConns <= 0 {
		s.MaxConns = DefaultMaxConns
	}
	if s.MaxPerApp <= 0 {
		s.MaxPerApp = DefaultMaxPerApp
	}
	s.active = map[string]int{}
	cl := &chanListener{conns: make(chan net.Conn), addr: l.Addr(), done: make(chan struct{})}
	srv := &http.Server{
		Handler:           http.HandlerFunc(s.serveHTTP),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, connKey{}, c)
		},
		ErrorLog: nil,
	}
	go func() { _ = srv.Serve(cl) }()
	go func() {
		<-ctx.Done()
		_ = l.Close()
		cl.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	for {
		c, err := l.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go s.handshake(c, cl)
	}
}

func (s *Server) checkPeer(c *net.UnixConn) error {
	if s.peerFilter != nil {
		return s.peerFilter(c)
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return credErr
	}
	if cred.Uid != s.PeerUID {
		return fmt.Errorf("connection from uid %d refused", cred.Uid)
	}
	return nil
}

// refuseConn answers a connection that never reaches the proxy.
func refuseConn(c net.Conn, status int) {
	_ = c.SetWriteDeadline(time.Now().Add(time.Second))
	fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status, http.StatusText(status))
	_ = c.Close()
}

func (s *Server) handshake(c *net.UnixConn, cl *chanListener) {
	if err := s.checkPeer(c); err != nil {
		s.logf("host services: %v", err)
		_ = c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Now().Add(headerTimeout))
	r := bufio.NewReaderSize(c, 4096)
	line, err := r.ReadSlice('\n')
	if err != nil || len(line) > maxHeaderLine {
		_ = c.Close()
		return
	}
	m := headerPattern.FindSubmatch(line)
	if m == nil || !strings.HasPrefix(string(m[2]), string(m[1])+"-dep-") {
		_ = c.Close()
		return
	}
	appID, deploymentID := string(m[1]), string(m[2])
	if _, ok := s.Lookup(appID, deploymentID); !ok {
		refuseConn(c, http.StatusForbidden)
		return
	}
	s.mu.Lock()
	if s.total >= s.MaxConns || s.active[appID] >= s.MaxPerApp {
		s.mu.Unlock()
		refuseConn(c, http.StatusServiceUnavailable)
		return
	}
	s.total++
	s.active[appID]++
	s.mu.Unlock()
	_ = c.SetReadDeadline(time.Time{})
	gc := &guestConn{Conn: c, r: r, appID: appID, deploymentID: deploymentID, release: func() {
		s.mu.Lock()
		s.total--
		s.active[appID]--
		if s.active[appID] == 0 {
			delete(s.active, appID)
		}
		s.mu.Unlock()
	}}
	select {
	case cl.conns <- gc:
	case <-cl.done:
		_ = gc.Close()
	}
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	gc, _ := r.Context().Value(connKey{}).(*guestConn)
	if gc == nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	app, ok := s.Lookup(gc.appID, gc.deploymentID)
	if !ok {
		w.Header().Set("Connection", "close")
		writeError(w, accessDenied("this deployment is no longer current"))
		return
	}
	s.Proxy.ServeApp(w, r, app)
}
