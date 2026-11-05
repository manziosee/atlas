package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func devEnv(t *testing.T, extra map[string]string) (map[string]string, string) {
	t.Helper()
	dir := t.TempDir()
	values := map[string]string{"ATLAS_NODE_ID": "dev", "ATLAS_DB_PATH": filepath.Join(dir, "atlas.db")}
	for k, v := range extra {
		values[k] = v
	}
	return values, filepath.Join(dir, devCredentialsFile)
}

func TestDevModeStartsWithNothingConfigured(t *testing.T) {
	values, credsPath := devEnv(t, nil)
	cfg, err := Load(env(values))
	if err != nil {
		t.Fatalf("Load with nothing configured: %v", err)
	}
	if cfg.Dev == nil || len(cfg.Dev.Token) < MinTokenLength || cfg.Dev.CredentialsPath != credsPath {
		t.Fatalf("dev info = %+v", cfg.Dev)
	}
	if len(cfg.GossipKeys) != 1 || len(cfg.APITokens) != 1 || cfg.APITokens[0] != cfg.Dev.Token {
		t.Fatalf("generated secrets not applied: keys=%d tokens=%v", len(cfg.GossipKeys), cfg.APITokens)
	}
	if cfg.DNSAddr != "0.0.0.0:5353" || cfg.LogFormat != "text" {
		t.Fatalf("dev defaults = DNS %q, log format %q", cfg.DNSAddr, cfg.LogFormat)
	}
	info, err := os.Stat(credsPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("credentials file mode = %v, want owner-only", mode)
	}

	again, err := Load(env(values))
	if err != nil || again.Dev.Token != cfg.Dev.Token || string(again.GossipKeys[0]) != string(cfg.GossipKeys[0]) {
		t.Fatalf("restart did not reuse dev secrets: %v", err)
	}
}

func TestDevModeKeepsExplicitSettings(t *testing.T) {
	values, _ := devEnv(t, map[string]string{"ATLAS_DEV": "true", "ATLAS_API_TOKENS": testToken, "ATLAS_DNS_ADDR": "", "ATLAS_LOG_FORMAT": "json"})
	cfg, err := Load(env(values))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dev == nil || cfg.Dev.Token != "" || cfg.APITokens[0] != testToken || len(cfg.GossipKeys) != 1 {
		t.Fatalf("forced dev mode with a supplied token = %+v tokens=%v", cfg.Dev, cfg.APITokens)
	}
	if cfg.DNSAddr != "" || cfg.LogFormat != "json" {
		t.Fatalf("explicit settings overridden: DNS %q, log %q", cfg.DNSAddr, cfg.LogFormat)
	}
}

func TestDevModeNeverMasksAMissingSecret(t *testing.T) {
	cases := map[string]map[string]string{
		"production":        {"ATLAS_ENV": "production"},
		"turned off":        {"ATLAS_DEV": "false"},
		"key without token": {"ATLAS_GOSSIP_KEY": testKey},
		"token without key": {"ATLAS_API_TOKENS": testToken},
		"seeds configured":  {"ATLAS_SEEDS": "10.0.0.1:7946"},
	}
	for name, extra := range cases {
		values, credsPath := devEnv(t, extra)
		if _, err := Load(env(values)); err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("%s: err = %v, want a missing-secret error", name, err)
		}
		if _, err := os.Stat(credsPath); err == nil {
			t.Errorf("%s: dev secrets were written", name)
		}
	}
}

func TestDevModeRejectsBadSettings(t *testing.T) {
	values, _ := devEnv(t, map[string]string{"ATLAS_DEV": "true", "ATLAS_ENV": "production"})
	if _, err := Load(env(values)); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("dev in production err = %v", err)
	}
	values, _ = devEnv(t, map[string]string{"ATLAS_DEV": "maybe"})
	if _, err := Load(env(values)); err == nil || !strings.Contains(err.Error(), "ATLAS_DEV") {
		t.Fatalf("bad ATLAS_DEV err = %v", err)
	}
	values, credsPath := devEnv(t, nil)
	if err := os.WriteFile(credsPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(env(values)); err == nil || !strings.Contains(err.Error(), "delete it") {
		t.Fatalf("corrupt credentials err = %v", err)
	}
}

func TestCORSAndConsoleSettings(t *testing.T) {
	values, _ := devEnv(t, nil)
	cfg, err := Load(env(values))
	if err != nil || len(cfg.CORSOrigins) != 1 || cfg.CORSOrigins[0] != "*" {
		t.Fatalf("dev mode CORS = %v %v, want [*]", cfg.CORSOrigins, err)
	}

	values, _ = devEnv(t, map[string]string{
		"ATLAS_CORS_ORIGINS": "https://Console.Example/, http://localhost:3000",
		"ATLAS_CONSOLE_URL":  "https://console.example/",
	})
	cfg, err = Load(env(values))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.CORSOrigins, ",") != "https://console.example,http://localhost:3000" || cfg.ConsoleURL != "https://console.example" {
		t.Fatalf("origins %v console %q", cfg.CORSOrigins, cfg.ConsoleURL)
	}

	for _, bad := range []map[string]string{
		{"ATLAS_CORS_ORIGINS": "https://console.example/app"},
		{"ATLAS_CORS_ORIGINS": "ftp://x"},
		{"ATLAS_CONSOLE_URL": "console.example"},
	} {
		values, _ = devEnv(t, bad)
		if _, err := Load(env(values)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
