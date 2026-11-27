package render

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"testing"
)

func TestPrivateRouteIsAppScopedAndFailClosed(t *testing.T) {
	in := loadInput(t, "crew-dev.input.json")
	app := &in.Apps[0]
	hostname := app.Hostnames[0].Hostname
	app.Hostnames = nil
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	app.PrivateRoute = &PrivateRoute{Hostname: hostname, PublicKey: pub}
	out := mustRender(t, in)
	if len(out.Rejected) != 0 {
		t.Fatal(out.Rejected)
	}
	var doc document
	if err := json.Unmarshal(out.Document, &doc); err != nil {
		t.Fatal(err)
	}
	inst := doc.Instances[0]
	if len(inst.Hostnames) != 1 || inst.Hostnames[0].Hostname != hostname || inst.Access.Kind != "jwt" || inst.Access.Issuer != PrivateAccessIssuer || len(inst.Access.Audiences) != 1 || inst.Access.Audiences[0] != app.AppID || inst.Access.Token.Header != PrivateAccessHeader || inst.Access.Token.Format != "raw" || len(inst.Access.PublicKeys) != 1 {
		t.Fatal("private hostname lacks exact JWT enforcement")
	}
	b, _ := pem.Decode([]byte(inst.Access.PublicKeys[0]))
	if b == nil {
		t.Fatal("invalid verification key")
	}
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil || !pub.Equal(k) {
		t.Fatal("wrong public verification key")
	}
	app.Hostnames = []Hostname{{Hostname: hostname, Kind: "platform"}}
	if bad := mustRender(t, in); len(bad.Rejected) != 1 {
		t.Fatal("private route accepted with a public alias")
	}
	app.Hostnames = nil
	app.PrivateRoute.PublicKey = nil
	if bad := mustRender(t, in); len(bad.Rejected) != 1 {
		t.Fatal("private route accepted without verification key")
	}
}
