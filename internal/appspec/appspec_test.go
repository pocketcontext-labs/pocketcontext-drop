package appspec

import (
	"strings"
	"testing"
)

func TestEnvRules(t *testing.T) {
	for name, ok := range map[string]bool{
		"A": true, "API_KEY": true, "X" + strings.Repeat("Y", 63): true, "X" + strings.Repeat("Y", 64): false,
		"lower": false, "1A": false, "A-B": false, "HOME": false, "PATH": false, "NIBRUN_HOSTNAME": false, "DROP_X": false,
		"HOMEPAGE": true, "PATHS": true,
	} {
		if got := CheckEnvName(name) == ""; got != ok {
			t.Errorf("CheckEnvName(%q) ok = %v", name, got)
		}
	}
	for value, ok := range map[string]bool{
		"": true, "plain": true, "https://${NIBRUN_HOSTNAME}:${NIBRUN_HTTP_PORT}/": true, "${OTHER}": true,
		"${NIBRUN_DATA_DIR}": false, "a\x00b": false, strings.Repeat("v", MaxEnvValueBytes): true, strings.Repeat("v", MaxEnvValueBytes+1): false,
	} {
		reason := CheckEnvValue("NAME", value)
		if (reason == "") != ok {
			t.Errorf("CheckEnvValue(%q) = %q", value, reason)
		}
		if reason != "" && value != "" && strings.Contains(reason, value) {
			t.Errorf("the reason repeats the value: %q", reason)
		}
	}
}

func TestArgsHealthAndIDs(t *testing.T) {
	if f, _ := CheckArgs(make([]string, MaxArgs+1)); f != "args" {
		t.Error("too many arguments accepted")
	}
	if f, _ := CheckArgs([]string{strings.Repeat("a", MaxArgsTotal/2), strings.Repeat("b", MaxArgsTotal/2+1)}); f != "args" {
		t.Error("too many argument bytes accepted")
	}
	if f, _ := CheckArgs([]string{"serve", "--port", "8080"}); f != "" {
		t.Error("plain arguments refused")
	}
	for _, c := range []struct {
		kind, path, field string
	}{{"tcp", "", ""}, {"tcp", "/", "path"}, {"http", "/up", ""}, {"http", "", "path"}, {"http", "/a b", "path"}, {"grpc", "", "kind"}} {
		if f, _ := CheckHealth(c.kind, c.path); f != c.field {
			t.Errorf("CheckHealth(%q, %q) field %q", c.kind, c.path, f)
		}
	}
	app := AppID("abcdefghijklmno")
	if !AppIDPattern.MatchString(app) || VolumeID(app) != app+"-vol" || !DeploymentIDOK(app, DeploymentID(app, 42)) ||
		DeploymentIDOK(app, DeploymentID(app, 1234567890)) || DeploymentIDOK(app, app+"-dep-") {
		t.Fatal("ids")
	}
}
