package dnsdiscovery

import (
	"context"
	"testing"
	"time"

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
