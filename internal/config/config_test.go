package config

import (
	"encoding/base64"
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

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(base()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Env != EnvDevelopment || cfg.GossipFanout != 3 || len(cfg.GossipKeys) != 1 {
		t.Fatalf("unexpected defaults: %+v", cfg)
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
		"missing tokens":      {map[string]string{"ATLAS_API_TOKENS": ""}, "ATLAS_API_TOKENS is required"},
		"weak token":          {map[string]string{"ATLAS_API_TOKENS": "abc"}, "too short"},
		"prod without tls":    {map[string]string{"ATLAS_ENV": "production"}, "production requires"},
		"timeout >= interval": {map[string]string{"ATLAS_PROBE_TIMEOUT": "2s", "ATLAS_PROBE_INTERVAL": "1s"}, "ATLAS_PROBE_TIMEOUT"},
		"mtls without ca": {map[string]string{
			"ATLAS_TLS_ENABLED": "true", "ATLAS_TLS_CERT_FILE": "c", "ATLAS_TLS_KEY_FILE": "k", "ATLAS_TLS_CLIENT_AUTH": "true",
		}, "ATLAS_TLS_CA_FILE is required"},
		"bad seed": {map[string]string{"ATLAS_SEEDS": "no-port"}, "ATLAS_SEEDS"},
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

func TestProductionWithUpstreamTLS(t *testing.T) {
	m := base()
	m["ATLAS_ENV"] = "production"
	m["ATLAS_TLS_TERMINATED_UPSTREAM"] = "true"
	if _, err := Load(env(m)); err != nil {
		t.Fatalf("Load: %v", err)
	}
}
