package dnsdiscovery

import (
	"context"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/manziosee/atlas/internal/routing"
	"github.com/manziosee/atlas/internal/storage"
)

type emptyRegistry struct{}

func (emptyRegistry) List(context.Context, storage.Filter) ([]storage.Service, error) {
	return nil, nil
}

func (emptyRegistry) Get(context.Context, string) (storage.Service, error) {
	return storage.Service{}, nil
}

func TestNewAcceptsValidZones(t *testing.T) {
	for _, zone := range []string{"", "atlas", "atlas.", "svc.Atlas-1.internal"} {
		if _, err := New(emptyRegistry{}, zone, 5*time.Second); err != nil {
			t.Errorf("New(%q) = %v, want nil", zone, err)
		}
	}
}

func TestNewRejectsInvalidZones(t *testing.T) {
	for _, zone := range []string{"-atlas", "atlas-", "at_las", "atl@s", "a..b"} {
		if _, err := New(emptyRegistry{}, zone, 5*time.Second); err == nil {
			t.Errorf("New(%q) = nil, want error", zone)
		}
	}
}

func TestLabelValidators(t *testing.T) {
	cases := []struct {
		label          string
		labelOK, svcOK bool
	}{
		{"atlas", true, true},
		{"API-v2", true, true},
		{"_grpc", false, true},
		{"has space", false, false},
		{"-lead", false, false},
	}
	for _, c := range cases {
		if got := validLabel(c.label); got != c.labelOK {
			t.Errorf("validLabel(%q) = %v, want %v", c.label, got, c.labelOK)
		}
		if got := validServiceLabel(c.label); got != c.svcOK {
			t.Errorf("validServiceLabel(%q) = %v, want %v", c.label, got, c.svcOK)
		}
	}
}

type listRegistry []storage.Service

func (r listRegistry) List(_ context.Context, f storage.Filter) ([]storage.Service, error) {
	var out []storage.Service
	for _, s := range r {
		if f.Name == "" || s.Name == f.Name {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r listRegistry) Get(_ context.Context, id string) (storage.Service, error) {
	for _, s := range r {
		if s.ID == id {
			return s, nil
		}
	}
	return storage.Service{}, storage.ErrNotFound
}

// ask sends one A query and returns the answer count and response code.
func ask(t *testing.T, s *Server, name string) (int, dnsmessage.RCode) {
	t.Helper()
	q, err := dnsmessage.NewName(name)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := (&dnsmessage.Message{
		Header:    dnsmessage.Header{ID: 7, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: q, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
	}).Pack()
	if err != nil {
		t.Fatal(err)
	}
	var reply dnsmessage.Message
	if err := reply.Unpack(s.answer(context.Background(), packet)); err != nil {
		t.Fatal(err)
	}
	return len(reply.Answers), reply.RCode
}

func TestDNSStalePolicyMatchesLookup(t *testing.T) {
	registry := listRegistry{
		// A draining instance on a live owner must not hide the healthy
		// instance whose owner is gone (prefer-live falls back to it).
		{ID: "live-draining", Name: "api", NodeID: "alive", Address: "10.0.0.1:80", Health: storage.HealthHealthy, Draining: true},
		{ID: "stale-healthy", Name: "api", NodeID: "gone", Address: "10.0.0.2:80", Health: storage.HealthHealthy},
	}
	live := func() map[string]bool { return map[string]bool{"alive": true} }
	server, err := New(registry, "atlas", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	server.SetStalePolicy(routing.StalePreferLive, live)
	if n, code := ask(t, server, "api.atlas."); n != 1 || code != dnsmessage.RCodeSuccess {
		t.Fatalf("prefer-live = %d answers, %v; want the stale fallback", n, code)
	}
	if n, _ := ask(t, server, "stale-healthy.instances.atlas."); n != 1 {
		t.Fatalf("prefer-live instance name = %d answers, want 1", n)
	}

	server.SetStalePolicy(routing.StaleExclude, live)
	if _, code := ask(t, server, "api.atlas."); code != dnsmessage.RCodeNameError {
		t.Fatalf("exclude = %v, want NXDOMAIN", code)
	}
	if _, code := ask(t, server, "stale-healthy.instances.atlas."); code != dnsmessage.RCodeNameError {
		t.Fatalf("exclude instance name = %v, want NXDOMAIN", code)
	}
	if _, code := ask(t, server, "live-draining.instances.atlas."); code != dnsmessage.RCodeNameError {
		t.Fatalf("draining instance name = %v, want NXDOMAIN", code)
	}
}
