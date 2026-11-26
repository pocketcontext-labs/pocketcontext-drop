package dropapi

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestReaderKnowsEveryOperation: ui/src/dropApi.ts lists the registry's
// operation names in the same order.
func TestReaderKnowsEveryOperation(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "ui", "src", "dropApi.ts"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)export const OPS = \[(.*?)\] as const;`).FindSubmatch(source)
	if block == nil {
		t.Fatal("ui/src/dropApi.ts has no OPS list")
	}
	var reader []string
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(block[1], -1) {
		reader = append(reader, string(m[1]))
	}
	var registry []string
	for _, s := range Specs() {
		registry = append(registry, s.Name)
	}
	if !slices.Equal(reader, registry) {
		t.Fatalf("reader OPS %v, registry %v", reader, registry)
	}
}

func TestBuildChecksTheInputType(t *testing.T) {
	r, err := Build("invite.create", InviteCreateInput{Email: "a@example.test"}, "k-1")
	if err != nil || r.Op != "invite.create" || string(r.Input) != `{"email":"a@example.test"}` || r.IdempotencyKey != "k-1" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := Build("invite.create", &InviteCreateInput{}, ""); err != nil {
		t.Fatal("a pointer input is accepted:", err)
	}
	if _, err := Build("invite.create", UserSetInput{}, ""); err == nil {
		t.Fatal("the wrong input type was accepted")
	}
	if _, err := Build("no.such", MeInput{}, ""); err == nil {
		t.Fatal("an unknown operation was accepted")
	}
	if _, err := Build("me", MeInput{}, "bad key"); err == nil {
		t.Fatal("a malformed idempotency key was accepted")
	}
}

func TestNamesByRole(t *testing.T) {
	user, admin := Names(RoleUser), Names(RoleAdmin)
	if !slices.Contains(user, "me") || slices.Contains(user, "invite.create") || slices.Contains(admin, "app.create") || !slices.Contains(admin, "app.list") {
		t.Fatalf("user %v admin %v", user, admin)
	}
	for _, s := range Specs() {
		if strings.HasPrefix(s.Name, "invite.") || strings.HasPrefix(s.Name, "user.") {
			if s.Role != RoleAdmin {
				t.Errorf("%s is not admin-only", s.Name)
			}
		}
	}
}

func TestAppAndSecretSpecs(t *testing.T) {
	for _, s := range Specs() {
		if strings.HasPrefix(s.Name, "app.") || strings.HasPrefix(s.Name, "secret.") {
			if s.Role != RoleUser {
				t.Errorf("%s is not a user operation", s.Name)
			}
			if s.SecretInput != (s.Name == "secret.set") {
				t.Errorf("%s: SecretInput %v", s.Name, s.SecretInput)
			}
		}
	}
	if _, err := Build("secret.set", SecretSetInput{App: "a", Name: "B", Value: "c"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Build("app.start", AppRef{App: "a"}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseSpecs(t *testing.T) {
	for _, name := range []string{"release.list", "release.upload_link", "release.deploy"} {
		s, ok := Lookup(name)
		if !ok || s.Role != RoleUser || s.SecretInput || s.Read != (name == "release.list") || s.OnceResult != (name == "release.upload_link") {
			t.Errorf("%s: %+v %v", name, s, ok)
		}
	}
	if UploadTokenPattern.String() != `^[A-Za-z0-9]{32}$` || MaxReleaseBytes != 90<<20 || UploadLinkTTL.Minutes() != 15 {
		t.Fatal("upload limits changed")
	}
}

func TestReleaseHeaderProblem(t *testing.T) {
	good := make([]byte, ELFHeaderBytes)
	copy(good, "\x7fELF\x02\x01\x01")
	good[16], good[18] = 2, 0x3E
	if p := ReleaseHeaderProblem(good); p != "" {
		t.Fatal(p)
	}
	pie := append([]byte(nil), good...)
	pie[16], pie[7] = 3, 3
	if p := ReleaseHeaderProblem(pie); p != "" {
		t.Fatal("a Linux-ABI position-independent executable is refused:", p)
	}
	for name, change := range map[string]func(b []byte){
		"magic":   func(b []byte) { b[1] = 'X' },
		"class":   func(b []byte) { b[4] = 1 },
		"endian":  func(b []byte) { b[5] = 2 },
		"version": func(b []byte) { b[6] = 0 },
		"os":      func(b []byte) { b[7] = 9 },
		"type":    func(b []byte) { b[16] = 1 },
		"machine": func(b []byte) { b[18] = 0xB7 },
	} {
		b := append([]byte(nil), good...)
		change(b)
		if ReleaseHeaderProblem(b) == "" {
			t.Errorf("%s: accepted", name)
		}
	}
	if ReleaseHeaderProblem(good[:63]) == "" {
		t.Error("a short header was accepted")
	}
}
