package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Dev mode lets `docker run ghcr.io/manziosee/atlas` start a working single
// node with no setup. It generates the gossip key and an admin API token,
// keeps them next to the database so restarts reuse them, and enables DNS.
//
// ATLAS_DEV=auto (the default) turns dev mode on only when nothing is
// configured: development environment, no gossip key, no API credentials and
// no seeds. Any real deployment that forgets one secret therefore still fails
// closed. ATLAS_DEV=true forces dev mode (not allowed in production);
// ATLAS_DEV=false turns it off.

// DevInfo describes a node started in dev mode.
type DevInfo struct {
	// Token is the admin API token dev mode generated, or empty when the
	// operator supplied credentials.
	Token string
	// CredentialsPath is where the generated secrets are kept.
	CredentialsPath string
}

const devCredentialsFile = "dev-credentials.json"

type devCredentials struct {
	GossipKey string `json:"gossip_key"`
	APIToken  string `json:"api_token"`
}

// applyDevMode fills in generated secrets and dev defaults when dev mode
// applies. It must run before Validate.
func (c *Config) applyDevMode(mode string, dnsAddrSet, logFormatSet bool) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto":
		nothingConfigured := len(c.GossipKeys) == 0 && len(c.APITokens) == 0 && len(c.APICredentials) == 0 && len(c.Seeds) == 0
		if c.Env != EnvDevelopment || !nothingConfigured {
			return nil
		}
	case "true", "1", "yes":
		if c.Env == EnvProduction {
			return errors.New("ATLAS_DEV=true is not allowed with ATLAS_ENV=production")
		}
	case "false", "0", "no":
		return nil
	default:
		return fmt.Errorf("ATLAS_DEV must be auto, true or false, got %q", mode)
	}

	path := filepath.Join(filepath.Dir(c.DatabasePath), devCredentialsFile)
	creds, err := loadOrCreateDevCredentials(path)
	if err != nil {
		return err
	}
	info := &DevInfo{CredentialsPath: path}
	if len(c.GossipKeys) == 0 {
		key, err := base64.StdEncoding.DecodeString(creds.GossipKey)
		if err != nil || len(key) != GossipKeySize {
			return fmt.Errorf("dev mode: %s holds an invalid gossip key; delete the file to regenerate it", path)
		}
		c.GossipKeys = [][]byte{key}
	}
	if len(c.APITokens) == 0 && len(c.APICredentials) == 0 {
		c.APITokens = []string{creds.APIToken}
		info.Token = creds.APIToken
	}
	if !dnsAddrSet {
		c.DNSAddr = "0.0.0.0:5353"
	}
	if !logFormatSet {
		c.LogFormat = "text"
	}
	c.Dev = info
	return nil
}

// loadOrCreateDevCredentials reuses saved dev secrets, or generates and saves
// new ones readable only by the node's user.
func loadOrCreateDevCredentials(path string) (devCredentials, error) {
	var creds devCredentials
	data, err := os.ReadFile(path) // #nosec G304 -- fixed file name next to the configured database.
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &creds); err != nil || creds.GossipKey == "" || len(creds.APIToken) < 32 {
			return creds, fmt.Errorf("dev mode: %s is unreadable; delete it to regenerate dev secrets", path)
		}
		return creds, nil
	case !errors.Is(err, os.ErrNotExist):
		return creds, fmt.Errorf("dev mode: read %s: %w", path, err)
	}
	key := make([]byte, GossipKeySize)
	token := make([]byte, 36)
	if _, err := rand.Read(key); err != nil {
		return creds, err
	}
	if _, err := rand.Read(token); err != nil {
		return creds, err
	}
	creds = devCredentials{GossipKey: base64.StdEncoding.EncodeToString(key), APIToken: base64.RawURLEncoding.EncodeToString(token)}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return creds, fmt.Errorf("dev mode: create %s: %w", filepath.Dir(path), err)
	}
	encoded, _ := json.MarshalIndent(creds, "", "  ") // #nosec G117 -- dev secrets are meant to be saved, to an owner-only file.
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		return creds, fmt.Errorf("dev mode: save %s: %w", path, err)
	}
	return creds, nil
}
