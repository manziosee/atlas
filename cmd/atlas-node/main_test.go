package main

import (
	"crypto/rand"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/manziosee/atlas/internal/config"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/transport"
)

func TestPprofHandlerServesProfiles(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	newPprofHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "goroutine") {
		t.Fatalf("pprof index status=%d body=%q", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	newPprofHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("pprof handler exposed unrelated endpoint, status=%d", recorder.Code)
	}
}

type reloadTransport struct{ packets chan transport.Packet }

func (t reloadTransport) Send(string, []byte) error        { return nil }
func (t reloadTransport) Packets() <-chan transport.Packet { return t.packets }

func TestHasLivePeerRequiresAnAliveRemoteNode(t *testing.T) {
	states := []membership.Member{
		{ID: "self", State: membership.StateAlive},
		{ID: "dead", State: membership.StateDead},
		{ID: "left", State: membership.StateLeft},
	}
	if hasLivePeer(states, "self") {
		t.Fatal("self or stale peers must not count as a successful join")
	}
	states = append(states, membership.Member{ID: "peer", State: membership.StateAlive})
	if !hasLivePeer(states, "self") {
		t.Fatal("live remote peer should satisfy join")
	}
}

func TestApplyRuntimeConfigRotatesSecretsAndLoggingLevel(t *testing.T) {
	oldKey, newKey := make([]byte, security.KeySize), make([]byte, security.KeySize)
	if _, err := rand.Read(oldKey); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(newKey); err != nil {
		t.Fatal(err)
	}
	keyring, err := security.NewKeyring(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	oldCipher, err := keyring.Seal([]byte("before reload"), nil)
	if err != nil {
		t.Fatal(err)
	}
	oldToken, newToken := strings.Repeat("o", config.MinTokenLength), strings.Repeat("n", config.MinTokenLength)
	auth := security.NewTokenAuthenticator([]string{oldToken})
	limiter := security.NewRateLimiter(1, 1)
	cluster := membership.New(membership.Config{NodeID: "self", AdvertiseAddr: "127.0.0.1:1"}, reloadTransport{packets: make(chan transport.Packet)}, slog.Default())
	level := &slog.LevelVar{}
	next := &config.Config{
		GossipKeys: [][]byte{newKey, oldKey}, APITokens: []string{newToken},
		AllowedNodeIDs: []string{"self", "peer"}, RateLimitRPS: 20, RateLimitBurst: 5, LogLevel: "debug",
	}
	if err := applyRuntimeConfig(next, keyring, auth, limiter, cluster, level); err != nil {
		t.Fatal(err)
	}
	if auth.Valid(oldToken) || !auth.Valid(newToken) {
		t.Fatal("API token replacement did not apply")
	}
	if _, err := keyring.Open(oldCipher, nil); err != nil {
		t.Fatalf("secondary gossip key was not retained: %v", err)
	}
	sealed, err := keyring.Seal([]byte("after reload"), nil)
	if err != nil {
		t.Fatal(err)
	}
	newOnly, _ := security.NewKeyring(newKey)
	if _, err := newOnly.Open(sealed, nil); err != nil {
		t.Fatalf("new primary key was not applied: %v", err)
	}
	if level.Level() != slog.LevelDebug {
		t.Fatalf("log level = %v, want debug", level.Level())
	}

	if err := applyRuntimeConfig(&config.Config{GossipKeys: [][]byte{[]byte("invalid")}, APITokens: []string{oldToken}}, keyring, auth, limiter, cluster, level); err == nil {
		t.Fatal("invalid key reload was accepted")
	}
	if !auth.Valid(newToken) {
		t.Fatal("failed key replacement partially updated API tokens")
	}
}
