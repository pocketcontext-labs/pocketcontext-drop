package awsx

import (
	"errors"
	"strings"
	"testing"
)

func TestReleaseKey(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	key, err := ReleaseKey(sha)
	if err != nil || key != "releases/"+sha {
		t.Fatalf("ReleaseKey = %q %v", key, err)
	}
	if err := ValidateKey(key); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", strings.ToUpper(sha), sha[:10], "../" + sha[3:]} {
		if _, err := ReleaseKey(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("ReleaseKey(%q): %v", bad, err)
		}
	}
}

func TestSecretParameter(t *testing.T) {
	got, err := SecretParameter("/drop", "d-abc123def456ghi", "DATABASE_URL")
	if err != nil || got != "/drop/apps/abc123def456ghi/DATABASE_URL" {
		t.Fatalf("SecretParameter = %q %v", got, err)
	}
	if err := ValidateParameterName(got); err != nil {
		t.Fatal(err)
	}
	bad := [][3]string{
		{"drop", "d-abc123def456ghi", "X"},
		{"/drop/", "d-abc123def456ghi", "X"},
		{"/Drop", "d-abc123def456ghi", "X"},
		{"/drop", "abc123def456ghi", "X"},
		{"/drop", "d-abc123def456gh", "X"},
		{"/drop", "d-ABC123DEF456GHI", "X"},
		{"/drop", "d-abc123def456ghi", "lower"},
		{"/drop", "d-abc123def456ghi", "1X"},
		{"/drop", "d-abc123def456ghi", "A/B"},
		{"/drop", "d-abc123def456ghi", strings.Repeat("A", 65)},
	}
	for _, b := range bad {
		if _, err := SecretParameter(b[0], b[1], b[2]); !errors.Is(err, ErrInvalid) {
			t.Errorf("SecretParameter%v: %v, want ErrInvalid", b, err)
		}
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{EnvRegion: "eu-north-1", EnvBucket: "drop-releases-dev"}
	cfg, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil || cfg.SSMPrefix != "/drop" || cfg.Region != "eu-north-1" || cfg.Bucket != "drop-releases-dev" {
		t.Fatalf("ConfigFromEnv = %+v %v", cfg, err)
	}
	for _, mutate := range []func(map[string]string){
		func(m map[string]string) { delete(m, EnvRegion) },
		func(m map[string]string) { delete(m, EnvBucket) },
		func(m map[string]string) { m[EnvBucket] = "Bad_Bucket" },
		func(m map[string]string) { m[EnvSSMPrefix] = "drop" },
	} {
		m := map[string]string{EnvRegion: "eu-north-1", EnvBucket: "drop-releases-dev"}
		mutate(m)
		if _, err := ConfigFromEnv(func(k string) string { return m[k] }); !errors.Is(err, ErrInvalid) {
			t.Errorf("ConfigFromEnv(%v): %v, want ErrInvalid", m, err)
		}
	}
}
