// Package config loads and validates Atlas node configuration from the
// environment. Every setting has the ATLAS_ prefix; see .env.example for the
// full list with documentation.
//
// Validation fails closed: a node refuses to start with a missing or weak
// gossip key, no API tokens, or (in production) without TLS.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment selects how strict validation is.
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvProduction  Environment = "production"
)

// GossipKeySize is the required length of a decoded gossip key (AES-256).
const GossipKeySize = 32

// MinTokenLength is the minimum accepted API token length in characters.
const MinTokenLength = 32

// TLS holds HTTPS / mTLS settings for the API listener.
type TLS struct {
	Enabled           bool
	CertFile          string
	KeyFile           string
	CAFile            string
	RequireClientCert bool
	// TerminatedUpstream acknowledges that TLS is terminated by a trusted
	// proxy (e.g. the Fly.io edge) so production may run plain HTTP inside
	// the private network.
	TerminatedUpstream bool
}

// Config is the fully parsed node configuration.
type Config struct {
	Env    Environment
	NodeID string
	Region string

	// Gossip / membership
	GossipBindAddr      string
	GossipAdvertiseAddr string
	Seeds               []string
	GossipKeys          [][]byte // [0] is the primary (encrypting) key
	ProbeInterval       time.Duration
	ProbeTimeout        time.Duration
	SuspicionTimeout    time.Duration
	GossipInterval      time.Duration
	GossipFanout        int
	DeadNodeReap        time.Duration
	MaxClockSkew        time.Duration

	// API
	HTTPAddr       string
	MetricsAddr    string
	TLS            TLS
	APITokens      []string
	RateLimitRPS   float64
	RateLimitBurst int

	// Logging
	LogLevel  string
	LogFormat string
}

// LookupFunc matches os.LookupEnv; injectable for tests.
type LookupFunc func(key string) (string, bool)

// FromEnv loads configuration from the process environment.
func FromEnv() (*Config, error) { return Load(os.LookupEnv) }

// Load parses and validates configuration using lookup.
func Load(lookup LookupFunc) (*Config, error) {
	p := parser{lookup: lookup}

	hostname, _ := os.Hostname()
	cfg := &Config{
		Env:    Environment(strings.ToLower(p.str("ATLAS_ENV", string(EnvDevelopment)))),
		NodeID: p.str("ATLAS_NODE_ID", hostname),
		Region: p.str("ATLAS_REGION", p.str("FLY_REGION", "local")),

		GossipBindAddr:      p.str("ATLAS_GOSSIP_BIND_ADDR", "0.0.0.0:7946"),
		GossipAdvertiseAddr: p.str("ATLAS_GOSSIP_ADVERTISE_ADDR", ""),
		Seeds:               p.list("ATLAS_SEEDS"),
		ProbeInterval:       p.duration("ATLAS_PROBE_INTERVAL", time.Second),
		ProbeTimeout:        p.duration("ATLAS_PROBE_TIMEOUT", 500*time.Millisecond),
		SuspicionTimeout:    p.duration("ATLAS_SUSPICION_TIMEOUT", 5*time.Second),
		GossipInterval:      p.duration("ATLAS_GOSSIP_INTERVAL", 200*time.Millisecond),
		GossipFanout:        p.int("ATLAS_GOSSIP_FANOUT", 3),
		DeadNodeReap:        p.duration("ATLAS_DEAD_NODE_REAP", 5*time.Minute),
		MaxClockSkew:        p.duration("ATLAS_MAX_CLOCK_SKEW", 30*time.Second),

		HTTPAddr:    p.str("ATLAS_HTTP_ADDR", "0.0.0.0:8080"),
		MetricsAddr: p.str("ATLAS_METRICS_ADDR", "0.0.0.0:9090"),
		TLS: TLS{
			Enabled:            p.bool("ATLAS_TLS_ENABLED", false),
			CertFile:           p.str("ATLAS_TLS_CERT_FILE", ""),
			KeyFile:            p.str("ATLAS_TLS_KEY_FILE", ""),
			CAFile:             p.str("ATLAS_TLS_CA_FILE", ""),
			RequireClientCert:  p.bool("ATLAS_TLS_CLIENT_AUTH", false),
			TerminatedUpstream: p.bool("ATLAS_TLS_TERMINATED_UPSTREAM", false),
		},
		APITokens:      p.list("ATLAS_API_TOKENS"),
		RateLimitRPS:   p.float("ATLAS_RATE_LIMIT_RPS", 20),
		RateLimitBurst: p.int("ATLAS_RATE_LIMIT_BURST", 40),

		LogLevel:  p.str("ATLAS_LOG_LEVEL", "info"),
		LogFormat: p.str("ATLAS_LOG_FORMAT", "json"),
	}

	if primary := p.str("ATLAS_GOSSIP_KEY", ""); primary != "" {
		cfg.GossipKeys = append(cfg.GossipKeys, p.key("ATLAS_GOSSIP_KEY", primary))
	}
	for i, k := range p.list("ATLAS_GOSSIP_KEYS_SECONDARY") {
		cfg.GossipKeys = append(cfg.GossipKeys, p.key(fmt.Sprintf("ATLAS_GOSSIP_KEYS_SECONDARY[%d]", i), k))
	}

	if err := errors.Join(p.errs...); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks semantic constraints. It is called by Load.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	switch c.Env {
	case EnvDevelopment, EnvProduction:
	default:
		add("ATLAS_ENV must be %q or %q, got %q", EnvDevelopment, EnvProduction, c.Env)
	}
	if c.NodeID == "" {
		add("ATLAS_NODE_ID must not be empty")
	}
	for name, addr := range map[string]string{
		"ATLAS_GOSSIP_BIND_ADDR": c.GossipBindAddr,
		"ATLAS_HTTP_ADDR":        c.HTTPAddr,
		"ATLAS_METRICS_ADDR":     c.MetricsAddr,
	} {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			add("%s: invalid host:port %q", name, addr)
		}
	}
	if c.GossipAdvertiseAddr != "" {
		if _, _, err := net.SplitHostPort(c.GossipAdvertiseAddr); err != nil {
			add("ATLAS_GOSSIP_ADVERTISE_ADDR: invalid host:port %q", c.GossipAdvertiseAddr)
		}
	}
	for _, s := range c.Seeds {
		if _, _, err := net.SplitHostPort(s); err != nil {
			add("ATLAS_SEEDS: invalid host:port %q", s)
		}
	}

	if len(c.GossipKeys) == 0 {
		add("ATLAS_GOSSIP_KEY is required (generate one with `atlas-cli keygen`)")
	}
	if c.ProbeTimeout <= 0 || c.ProbeTimeout >= c.ProbeInterval {
		add("ATLAS_PROBE_TIMEOUT (%s) must be > 0 and < ATLAS_PROBE_INTERVAL (%s)", c.ProbeTimeout, c.ProbeInterval)
	}
	if c.SuspicionTimeout < c.ProbeInterval {
		add("ATLAS_SUSPICION_TIMEOUT (%s) must be >= ATLAS_PROBE_INTERVAL (%s)", c.SuspicionTimeout, c.ProbeInterval)
	}
	if c.GossipInterval <= 0 {
		add("ATLAS_GOSSIP_INTERVAL must be > 0")
	}
	if c.GossipFanout < 1 || c.GossipFanout > 10 {
		add("ATLAS_GOSSIP_FANOUT must be between 1 and 10, got %d", c.GossipFanout)
	}
	if c.DeadNodeReap < c.SuspicionTimeout {
		add("ATLAS_DEAD_NODE_REAP must be >= ATLAS_SUSPICION_TIMEOUT")
	}
	if c.MaxClockSkew <= 0 {
		add("ATLAS_MAX_CLOCK_SKEW must be > 0")
	}

	if len(c.APITokens) == 0 {
		add("ATLAS_API_TOKENS is required (generate one with `atlas-cli tokengen`)")
	}
	for i, t := range c.APITokens {
		if len(t) < MinTokenLength {
			add("ATLAS_API_TOKENS[%d] is too short (%d chars, minimum %d)", i, len(t), MinTokenLength)
		}
	}
	if c.RateLimitRPS <= 0 || c.RateLimitBurst < 1 {
		add("ATLAS_RATE_LIMIT_RPS must be > 0 and ATLAS_RATE_LIMIT_BURST >= 1")
	}

	if c.TLS.Enabled {
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			add("ATLAS_TLS_CERT_FILE and ATLAS_TLS_KEY_FILE are required when TLS is enabled")
		}
		if c.TLS.RequireClientCert && c.TLS.CAFile == "" {
			add("ATLAS_TLS_CA_FILE is required when ATLAS_TLS_CLIENT_AUTH=true")
		}
	}
	if c.Env == EnvProduction && !c.TLS.Enabled && !c.TLS.TerminatedUpstream {
		add("production requires ATLAS_TLS_ENABLED=true (or ATLAS_TLS_TERMINATED_UPSTREAM=true behind a trusted TLS proxy)")
	}

	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		add("ATLAS_LOG_LEVEL must be debug|info|warn|error, got %q", c.LogLevel)
	}

	return errors.Join(errs...)
}

// parser accumulates errors so all bad settings are reported at once.
type parser struct {
	lookup LookupFunc
	errs   []error
}

func (p *parser) str(key, def string) string {
	if v, ok := p.lookup(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func (p *parser) list(key string) []string {
	var out []string
	for _, s := range strings.Split(p.str(key, ""), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (p *parser) bool(key string, def bool) bool {
	v := p.str(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: invalid bool %q", key, v))
	}
	return b
}

func (p *parser) int(key string, def int) int {
	v := p.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: invalid integer %q", key, v))
	}
	return n
}

func (p *parser) float(key string, def float64) float64 {
	v := p.str(key, "")
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: invalid number %q", key, v))
	}
	return f
}

func (p *parser) duration(key string, def time.Duration) time.Duration {
	v := p.str(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: invalid duration %q", key, v))
	}
	return d
}

func (p *parser) key(name, encoded string) []byte {
	k, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: not valid base64", name))
		return nil
	}
	if len(k) != GossipKeySize {
		p.errs = append(p.errs, fmt.Errorf("%s: decoded key is %d bytes, want %d", name, len(k), GossipKeySize))
		return nil
	}
	return k
}
