package hostservices

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGuestMailFrame(t *testing.T) {
	for frame, want := range map[string]string{
		"{\"code\":\"123456\"}\n": "123456", "  { \"code\" : \"12345\\u0036\" }\r\n": "123456",
		"{\"code\":\"123456\"}": "", "{\"code\":\"123456\",\"to\":\"guest@example.test\"}\n": "",
		"{\"code\":\"123456\",\"code\":\"654321\"}\n": "", "{\"code\":123456}\n": "",
		"{\"code\":\"１２３４５６\"}\n": "", "{\"code\":\"123456\"} {}\n": "",
		strings.Repeat(" ", 128) + "{\"code\":\"123456\"}\n": "",
	} {
		if got := readMailCode(strings.NewReader(frame)); got != want {
			t.Errorf("frame parsed incorrectly, expected valid=%t", want != "")
		}
	}
}

func TestMailCredential(t *testing.T) {
	p := filepath.Join(t.TempDir(), "smtp.toml")
	const valid = "host='relay.test'\nusername='relay'\npassword='secret'\nsender='login@example.test'\n"
	for _, x := range []struct {
		extra string
		valid bool
	}{{"", true}, {"port=2525", true}, {"other='invalid'", false}, {"port=0", false}, {"port=65536", false}, {"ca_file='missing-file'", false}} {
		if err := os.WriteFile(p, []byte(valid+x.extra), 0600); err != nil {
			t.Fatal(err)
		}
		m, err := NewSignInEmail(p, "")
		if (err == nil) != x.valid {
			t.Fatal("unexpected SMTP credential validation")
		}
		if x.extra == "" && m.credential.Port != 465 {
			t.Fatal("wrong default port")
		}
	}
}

func TestMailRateLimit(t *testing.T) {
	s := SignInEmail{apps: make(mailWindow), recipients: make(mailWindow)}
	start := time.Now()
	for i := 0; i < 5; i++ {
		if !s.admit("app-a", "a@example.test", start) {
			t.Fatal("rate limit too early")
		}
	}
	if s.admit("app-b", "A@example.test", start) || s.admit("app-a", "b@example.test", start) {
		t.Fatal("missing per-recipient or per-app limit")
	}
	if !s.admit("app-a", "a@example.test", start.Add(time.Minute)) {
		t.Fatal("expired quota not released")
	}
}

func TestMailSocketUsesCurrentDeploymentWithoutStorage(t *testing.T) {
	root := t.TempDir()
	desired := filepath.Join(root, "desired.json")
	mail := &SignInEmail{desiredPath: desired, submissions: make(chan struct{}, 16), apps: make(mailWindow), recipients: make(mailWindow)}
	srv := &Server{Socket: filepath.Join(root, "hs.sock"), PeerUID: uint32(os.Getuid()), Mail: mail}
	l, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, l) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	writeDesired := func(state string) {
		content := fmt.Sprintf(`{"instances":[{"appId":%q,"deploymentId":%q,"desiredState":%q,"signInEmail":{"recipient":"owner@example.test"}}]}`, testApp, testDep, state)
		if err := os.WriteFile(desired, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	request := func(method, dep, body string) string {
		c, err := net.Dial("unix", srv.Socket)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(time.Second))
		if _, err = fmt.Fprintf(c, "%s %s %s\n%s", method, testApp, dep, body); err != nil {
			t.Fatal(err)
		}
		reply, err := bufio.NewReader(c).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return reply
	}
	writeDesired("on-request")
	if mail.recipient(testApp, testDep) != "owner@example.test" {
		t.Fatal("mail-only deployment not found")
	}
	const frame = "{\"code\":\"123456\"}\n"
	if got := request("CONNECT-MAIL", testDep+"2", frame); got != "{\"status\":\"disabled\"}\n" {
		t.Fatalf("stale deployment: %s", got)
	}
	if got := request("CONNECT", testDep, frame); !strings.Contains(got, "403") {
		t.Fatalf("mail enabled S3: %s", got)
	}
	if got := request("CONNECT-MAIL", testDep, "{\"recipient\":\"other@example.test\"}\n"); got != "{\"status\":\"invalid_request\"}\n" {
		t.Fatalf("guest-controlled recipient: %s", got)
	}
	writeDesired("stopped")
	if got := request("CONNECT-MAIL", testDep, frame); got != "{\"status\":\"disabled\"}\n" {
		t.Fatalf("stopped deployment: %s", got)
	}
}

func TestLoginOnlyRelayAuth(t *testing.T) {
	a := loginAuth{"localhost", "user", "pass"}
	if _, _, err := a.Start(&smtp.ServerInfo{Name: "other", TLS: true}); err == nil {
		t.Fatal("wrong peer")
	}
	if _, _, err := a.Start(&smtp.ServerInfo{Name: "localhost"}); err == nil {
		t.Fatal("cleartext peer")
	}
	if mechanism, _, err := a.Start(&smtp.ServerInfo{Name: "localhost", TLS: true}); err != nil || mechanism != "LOGIN" {
		t.Fatal("LOGIN negotiation failed")
	}
	for prompt, want := range map[string]string{"Username:": "user", "User Name\x00": "user", "Password:": "pass"} {
		response, err := a.Next([]byte(prompt), true)
		if err != nil || string(response) != want {
			t.Fatal("unhandled SMTP challenge")
		}
	}
	if _, err := a.Next([]byte("unrecognized"), true); err == nil {
		t.Fatal("accepted unexpected challenge")
	}
}
