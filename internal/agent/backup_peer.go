package agent

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
)

var errBackupGuestAsleep = errors.New("backup guest is asleep")

// backupPeer pins the proof and archive requests to one TCP connection. A recycled
// guest address must never receive another guest's bearer token or archive.
type backupPeer struct {
	client      *http.Client
	transport   *http.Transport
	base, token string
}

func (a *Agent) backupPeer(ctx context.Context, app, deployment string) (*backupPeer, error) {
	if !appspec.AppIDPattern.MatchString(app) || !appspec.DeploymentIDOK(app, deployment) {
		return nil, errors.New("invalid backup target")
	}
	rep, err := readReported(a.cfg.Reported)
	if err != nil || rep == nil {
		return nil, errors.New("guest report unavailable")
	}
	at, err := time.Parse(time.RFC3339Nano, rep.ReportedAt)
	if err != nil || at.After(a.now()) || a.now().Sub(at) > StaleReport || rep.HostID != a.hostID {
		return nil, errors.New("guest report is stale")
	}
	var addr netip.Addr
	for _, inst := range rep.Instances {
		if inst.AppID != app || inst.DeploymentID != deployment {
			continue
		}
		switch inst.State {
		case "idle", "stopped":
			return nil, errBackupGuestAsleep
		case "running":
			addr, _ = netip.ParseAddr(inst.GuestIPv4)
		}
		break
	}
	if !addr.Is4() || !addr.IsPrivate() {
		return nil, errors.New("guest unavailable")
	}
	var hooks struct {
		Port              int
		Deployment, Token string
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(a.cfg.Reported), "vm", app, "lifecycle-hooks.json"))
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &hooks) != nil || hooks.Deployment != deployment || hooks.Port < 1 || hooks.Port > 65535 || len(hooks.Token) != 32 {
		return nil, errors.New("guest hooks unavailable")
	}
	address := net.JoinHostPort(addr.String(), strconv.Itoa(hooks.Port))
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	peer, err := authenticateBackupPeer(ctx, conn, address, hooks.Token)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return peer, nil
}

func authenticateBackupPeer(ctx context.Context, conn net.Conn, address, token string) (*backupPeer, error) {
	connections := make(chan net.Conn, 1)
	connections <- conn
	tr := &http.Transport{MaxConnsPerHost: 1, MaxResponseHeaderBytes: 8192, DialContext: func(context.Context, string, string) (net.Conn, error) {
		select {
		case c := <-connections:
			return c, nil
		default:
			return nil, errors.New("backup guest connection closed")
		}
	}}
	p := &backupPeer{transport: tr, client: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, base: "http://" + address, token: token}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	challenge := hex.EncodeToString(nonce[:])
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/control/backup-peer", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Drop-Challenge", challenge)
	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	proof, err := hex.DecodeString(res.Header.Get("X-Drop-Proof"))
	mac := hmac.New(sha256.New, []byte(token))
	fmt.Fprint(mac, "drop-backup-peer/v1:", challenge)
	_, drainErr := io.Copy(io.Discard, io.LimitReader(res.Body, 1024))
	res.Body.Close()
	if drainErr != nil || res.StatusCode/100 != 2 || err != nil || !hmac.Equal(mac.Sum(nil), proof) {
		tr.CloseIdleConnections()
		return nil, errors.New("guest proof failed")
	}
	return p, nil
}

func (p *backupPeer) request(ctx context.Context, method, path, source, job string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("X-Drop-Job", job)
	if body != nil {
		req.Header.Set("Content-Type", "application/zip")
		req.Header.Set("X-Drop-Source-App", source)
	}
	return p.client.Do(req)
}
