package config

import (
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	testKey   = base64.StdEncoding.EncodeToString(make([]byte, GossipKeySize))
	testToken = strings.Repeat("t", MinTokenLength)
)

func env(m map[string]string) LookupFunc {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func base() map[string]string {
	return map[string]string{
		"ATLAS_NODE_ID":    "n1",
		"ATLAS_GOSSIP_KEY": testKey,
		"ATLAS_API_TOKENS": testToken,
	}
}

func FuzzLoadConfigValues(f *testing.F) {
	f.Add("1s", "500ms", "3", "20")
	f.Add("not-a-duration", "-1s", "999999999999999999", "NaN")
	f.Fuzz(func(t *testing.T, probeInterval, probeTimeout, fanout, rps string) {
		values := base()
		values["ATLAS_PROBE_INTERVAL"] = probeInterval
		values["ATLAS_PROBE_TIMEOUT"] = probeTimeout
		values["ATLAS_GOSSIP_FANOUT"] = fanout
		values["ATLAS_RATE_LIMIT_RPS"] = rps
		_, _ = Load(env(values))
	})
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(base()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Env != EnvDevelopment || cfg.GossipFanout != 3 || len(cfg.GossipKeys) != 1 || cfg.PprofAddr != "127.0.0.1:6060" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestPprofListenerMustUseLoopback(t *testing.T) {
	for _, address := range []string{"0.0.0.0:6060", "192.168.1.10:6060", "localhost:6060", "127.0.0.1:0", "127.0.0.1:65536"} {
		values := base()
		values["ATLAS_PPROF_ADDR"] = address
		if _, err := Load(env(values)); err == nil || !strings.Contains(err.Error(), "ATLAS_PPROF_ADDR") {
			t.Errorf("Load accepted unsafe pprof address %q (err %v)", address, err)
		}
	}
	values := base()
	values["ATLAS_PPROF_ADDR"] = "[::1]:6060"
	if _, err := Load(env(values)); err != nil {
		t.Fatalf("loopback IPv6 pprof address rejected: %v", err)
	}
}

func TestLoadConfigFileAndEnvironmentPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atlas.env")
	content := "ATLAS_NODE_ID=file-node\nATLAS_REGION=file-region\nATLAS_GOSSIP_KEY=" + testKey + "\nATLAS_API_TOKENS=" + testToken + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"ATLAS_CONFIG_FILE": path, "ATLAS_REGION": "env-region"}
	cfg, err := Load(env(values))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeID != "file-node" || cfg.Region != "env-region" {
		t.Fatalf("config file/env precedence = node %q region %q", cfg.NodeID, cfg.Region)
	}

	if err := os.WriteFile(path, []byte("ATLAS_UNKOWN_OPTION=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(env(values)); err == nil || !strings.Contains(err.Error(), "unknown setting") {
		t.Fatalf("unknown config setting error = %v", err)
	}
}

func TestTrustedProxyCIDRs(t *testing.T) {
	values := base()
	values["ATLAS_TRUSTED_PROXIES"] = "10.0.0.0/8, 2001:db8::/32"
	cfg, err := Load(env(values))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.TrustedProxies) != 2 || !cfg.TrustedProxies[0].Contains(net.ParseIP("10.1.2.3")) || !cfg.TrustedProxies[1].Contains(net.ParseIP("2001:db8::1")) {
		t.Fatalf("unexpected trusted proxy subnets: %v", cfg.TrustedProxies)
	}

	values["ATLAS_TRUSTED_PROXIES"] = "not-a-cidr"
	if _, err := Load(env(values)); err == nil {
		t.Fatal("invalid trusted proxy CIDR was accepted")
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]struct {
		set     map[string]string
		wantErr string
	}{
		"missing gossip key":  {map[string]string{"ATLAS_GOSSIP_KEY": ""}, "ATLAS_GOSSIP_KEY is required"},
		"short gossip key":    {map[string]string{"ATLAS_GOSSIP_KEY": base64.StdEncoding.EncodeToString([]byte("short"))}, "want 32"},
		"bad base64":          {map[string]string{"ATLAS_GOSSIP_KEY": "!!!"}, "not valid base64"},
		"missing tokens":      {map[string]string{"ATLAS_API_TOKENS": ""}, "ATLAS_API_TOKENS or ATLAS_API_CREDENTIALS is required"},
		"weak token":          {map[string]string{"ATLAS_API_TOKENS": "abc"}, "too short"},
		"prod without tls":    {map[string]string{"ATLAS_ENV": "production"}, "production requires"},
		"timeout >= interval": {map[string]string{"ATLAS_PROBE_TIMEOUT": "2s", "ATLAS_PROBE_INTERVAL": "1s"}, "ATLAS_PROBE_TIMEOUT"},
		"mtls without ca": {map[string]string{
			"ATLAS_TLS_ENABLED": "true", "ATLAS_TLS_CERT_FILE": "c", "ATLAS_TLS_KEY_FILE": "k", "ATLAS_TLS_CLIENT_AUTH": "true",
		}, "ATLAS_TLS_CA_FILE is required"},
		"bad seed":                {map[string]string{"ATLAS_SEEDS": "no-port"}, "ATLAS_SEEDS"},
		"admission excludes self": {map[string]string{"ATLAS_ALLOWED_NODE_IDS": "n2,n3"}, "must include this node"},
		"duplicate admission ID":  {map[string]string{"ATLAS_ALLOWED_NODE_IDS": "n1,n1"}, "duplicate node ID"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := base()
			for k, v := range tc.set {
				m[k] = v
			}
			_, err := Load(env(m))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got err %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadScopedCredentials(t *testing.T) {
	values := base()
	delete(values, "ATLAS_API_TOKENS")
	values["ATLAS_API_CREDENTIALS"] = `[{"token":"` + testToken + `","scopes":["read"],"expires_at":"2099-01-01T00:00:00Z"}]`
	cfg, err := Load(env(values))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.APICredentials) != 1 || len(cfg.APICredentials[0].Scopes) != 1 || cfg.APICredentials[0].Scopes[0] != "read" {
		t.Fatalf("credentials = %+v", cfg.APICredentials)
	}
	values["ATLAS_API_CREDENTIALS"] = `[{"token":"` + testToken + `","scopes":["root"]}]`
	if _, err := Load(env(values)); err == nil || !strings.Contains(err.Error(), "scope must be read, write, or admin") {
		t.Fatalf("invalid credential scope error = %v", err)
	}
	values["ATLAS_API_TOKENS"] = testToken
	if _, err := Load(env(values)); err == nil || !strings.Contains(err.Error(), "configure either") {
		t.Fatalf("ambiguous credential configuration error = %v", err)
	}
}

func TestProductionWithUpstreamTLS(t *testing.T) {
	m := base()
	m["ATLAS_ENV"] = "production"
	m["ATLAS_TLS_TERMINATED_UPSTREAM"] = "true"
	if _, err := Load(env(m)); err != nil {
		t.Fatalf("Load: %v", err)
	}
}
