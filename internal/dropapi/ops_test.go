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
	if !slices.Contains(user, "me") || slices.Contains(user, "invite.create") || len(admin) != len(Specs()) {
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
