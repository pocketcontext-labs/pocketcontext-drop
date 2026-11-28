package hostservices

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
)

type smtpCredential struct {
	Host     string
	Port     uint16
	Username string
	Password string
	Sender   string
	CAFile   string `toml:"ca_file"`
}

// SignInEmail handles one code per guest connection. Recipient and sender are
// supplied exclusively by the applied deployment and the service credential.
type SignInEmail struct {
	credential       smtpCredential
	tlsConfig        *tls.Config
	desiredPath      string
	submissions      chan struct{}
	mu               sync.Mutex
	apps, recipients mailWindow
}

func NewSignInEmail(credentialPath, desiredPath string) (*SignInEmail, error) {
	file, err := os.Open(credentialPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 16385))
	invalid := errors.New("invalid sign-in email credential")
	if err != nil || len(data) > 16384 {
		return nil, invalid
	}
	cred := smtpCredential{Port: 465}
	meta, err := toml.Decode(string(data), &cred)
	if err != nil || len(meta.Undecoded()) != 0 || cred.Host == "" || cred.Port == 0 || cred.Username == "" || cred.Password == "" || !appspec.ValidSignInMailbox(cred.Sender) {
		return nil, invalid
	}
	cfg := &tls.Config{ServerName: cred.Host, MinVersion: tls.VersionTLS12}
	if cred.CAFile != "" {
		certs, err := os.ReadFile(cred.CAFile)
		if err != nil {
			return nil, errors.New("cannot read sign-in email TLS CA")
		}
		roots, err := x509.SystemCertPool()
		if err != nil || !roots.AppendCertsFromPEM(certs) {
			return nil, errors.New("invalid sign-in email TLS CA")
		}
		cfg.RootCAs = roots
	}
	return &SignInEmail{credential: cred, tlsConfig: cfg, desiredPath: desiredPath, submissions: make(chan struct{}, 16), apps: make(mailWindow), recipients: make(mailWindow)}, nil
}

func (s *SignInEmail) recipient(app, dep string) string {
	raw, err := os.ReadFile(s.desiredPath)
	if err != nil {
		return ""
	}
	var desired struct {
		Instances []struct {
			AppID        string                     `json:"appId"`
			DeploymentID string                     `json:"deploymentId"`
			DesiredState string                     `json:"desiredState"`
			SignInEmail  struct{ Recipient string } `json:"signInEmail"`
		}
	}
	if json.Unmarshal(raw, &desired) != nil {
		return ""
	}
	for _, i := range desired.Instances {
		if i.AppID == app && i.DeploymentID == dep && (i.DesiredState == "on-request" || i.DesiredState == "running") && appspec.ValidSignInMailbox(i.SignInEmail.Recipient) {
			return i.SignInEmail.Recipient
		}
	}
	return ""
}

type mailWindow map[string][]time.Time

func (window mailWindow) hasRoom(key string, now time.Time) bool {
	for principal, times := range window {
		for len(times) > 0 && now.Sub(times[0]) >= time.Minute {
			times = times[1:]
		}
		if len(times) == 0 {
			delete(window, principal)
		} else {
			window[principal] = times
		}
	}
	times, exists := window[key]
	return len(times) < 5 && (exists || len(window) < 4096)
}

func (s *SignInEmail) admit(app, recipient string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	recipient = strings.ToLower(recipient)
	if !s.apps.hasRoom(app, now) || !s.recipients.hasRoom(recipient, now) {
		return false
	}
	s.apps[app] = append(s.apps[app], now)
	s.recipients[recipient] = append(s.recipients[recipient], now)
	return true
}

var signInCode = regexp.MustCompile(`^[0-9]{6}$`)

// The guest sends exactly one code field on a newline-terminated JSON line.
// Decode tokens to reject duplicate fields as well as unknown fields.
func readMailCode(reader io.Reader) string {
	line, err := bufio.NewReader(io.LimitReader(reader, 128)).ReadBytes('\n')
	if err != nil {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	open, _ := dec.Token()
	key, _ := dec.Token()
	code, _ := dec.Token()
	closeBracket, _ := dec.Token()
	_, end := dec.Token()
	digits, ok := code.(string)
	if open != json.Delim('{') || key != "code" || !ok || !signInCode.MatchString(digits) || closeBracket != json.Delim('}') || end != io.EOF {
		return ""
	}
	return digits
}

func mailReply(c net.Conn, status string) {
	_ = c.SetWriteDeadline(time.Now().Add(time.Second))
	fmt.Fprintf(c, "{\"status\":%q}\n", status)
}

func (s *SignInEmail) serve(c *guestConn) {
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	code := readMailCode(c.r)
	if code == "" {
		mailReply(c, "invalid_request")
		return
	}
	recipient := s.recipient(c.appID, c.deploymentID)
	if recipient == "" {
		mailReply(c, "disabled")
		return
	}
	select {
	case s.submissions <- struct{}{}:
		defer func() { <-s.submissions }()
	default:
		mailReply(c, "throttled")
		return
	}
	if !s.admit(c.appID, recipient, time.Now()) {
		mailReply(c, "throttled")
		return
	}
	mailReply(c, s.submit(c.appID, c.deploymentID, recipient, code))
}

// Some existing implicit-TLS relays advertise LOGIN only.
type loginAuth struct{ host, user, password string }

func (a loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS || server.Name != a.host {
		return "", nil, errors.New("invalid SMTP peer")
	}
	return "LOGIN", nil, nil
}

func (a loginAuth) Next(prompt []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.TrimRight(strings.ToLower(string(prompt)), ":\x00") {
	case "user name", "username":
		return []byte(a.user), nil
	case "password":
		return []byte(a.password), nil
	}
	return nil, errors.New("unknown SMTP login challenge")
}

func (c smtpCredential) authenticate(client *smtp.Client) error {
	ok, offers := client.Extension("AUTH")
	methods := strings.Fields(strings.ToUpper(offers))
	switch {
	case ok && slices.Contains(methods, "PLAIN"):
		return client.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host))
	case ok && slices.Contains(methods, "LOGIN"):
		return client.Auth(loginAuth{c.Host, c.Username, c.Password})
	default:
		return errors.New("relay requires unsupported authentication")
	}
}

func (s *SignInEmail) submit(app, dep, recipient, code string) string {
	const failure = "temporarily_unavailable"
	cred := s.credential
	deadline := time.Now().Add(8 * time.Second)
	conn, err := tls.DialWithDialer(&net.Dialer{Deadline: deadline}, "tcp", net.JoinHostPort(cred.Host, strconv.Itoa(int(cred.Port))), s.tlsConfig)
	if err != nil {
		return failure
	}
	defer conn.Close()
	if conn.SetDeadline(deadline) != nil {
		return failure
	}
	client, err := smtp.NewClient(conn, cred.Host)
	if err != nil {
		return failure
	}
	defer client.Close()
	if cred.authenticate(client) != nil {
		return failure
	}
	if s.recipient(app, dep) != recipient {
		return "disabled"
	}
	if client.Mail(cred.Sender) != nil || client.Rcpt(recipient) != nil {
		return failure
	}
	data, err := client.Data()
	if err != nil {
		return failure
	}
	_, err = fmt.Fprintf(data, "From: %s\r\nTo: %s\r\nDate: %s\r\nSubject: Your sign-in code\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nYour sign-in code is %s.\r\n\r\nEnter this code in the app to sign in.\r\n", cred.Sender, recipient, time.Now().Format(time.RFC1123Z), code)
	if err != nil || data.Close() != nil {
		return failure
	}
	return "accepted"
}
