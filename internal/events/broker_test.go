package events

import (
	"fmt"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/storage"
)

func publishN(b *Broker, n int) []Event {
	out := make([]Event, n)
	for i := range n {
		out[i] = b.Publish(Event{Type: "join", Kind: KindMember, Member: &membership.Member{ID: fmt.Sprint(i)}})
	}
	return out
}

func TestBrokerDeliversAndResumes(t *testing.T) {
	b := NewBroker(8)
	published := publishN(b, 5)

	sub, backlog, reset := b.Subscribe(published[1].ID, 4)
	defer sub.Close()
	if reset || len(backlog) != 3 || backlog[0].ID != published[2].ID || backlog[2].ID != published[4].ID {
		t.Fatalf("backlog = %+v reset=%v, want events 2..4", backlog, reset)
	}
	next := b.Publish(Event{Type: "update", Kind: KindMember})
	select {
	case got := <-sub.C:
		if got.ID != next.ID {
			t.Fatalf("live event = %s, want %s", got.ID, next.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("no live event")
	}
	if _, backlog, reset := b.Subscribe(next.ID, 1); reset || len(backlog) != 0 {
		t.Fatalf("resume at head = %d events reset=%v", len(backlog), reset)
	}
}

func TestBrokerResetsOnUnknownOrExpiredID(t *testing.T) {
	b := NewBroker(4)
	published := publishN(b, 10)
	for _, id := range []string{"other.1", "garbage", published[0].ID, b.epoch + ".999"} {
		if _, _, reset := b.Subscribe(id, 1); !reset {
			t.Errorf("Subscribe(%q) did not reset", id)
		}
	}
	_, backlog, reset := b.Subscribe(published[5].ID, 1)
	if reset || len(backlog) != 4 {
		t.Fatalf("resume within ring = %d reset=%v, want 4 events", len(backlog), reset)
	}
}

func TestBrokerClosesLaggingSubscriber(t *testing.T) {
	b := NewBroker(16)
	sub, _, _ := b.Subscribe("", 1)
	publishN(b, 3)
	<-sub.C // the one buffered event
	if _, open := <-sub.C; open {
		t.Fatal("lagging subscription still open")
	}
	if !sub.Lagged() {
		t.Fatal("Lagged() = false")
	}
	sub.Close() // idempotent
}

func TestFromServiceChangeClassifies(t *testing.T) {
	active := storage.Service{ID: "a", Name: "api", Address: "10.0.0.1:80", Health: storage.HealthUnknown}
	healthy := active
	healthy.Health = storage.HealthHealthy
	tagged := healthy
	tagged.Tags = []string{"x"}
	deleted := tagged
	deleted.Deleted = true
	cases := []struct {
		change storage.Change
		want   string
	}{
		{storage.Change{After: active}, ServiceRegistered},
		{storage.Change{Before: &deleted, After: active}, ServiceRegistered},
		{storage.Change{Before: &active, After: healthy}, ServiceHealth},
		{storage.Change{Before: &healthy, After: tagged}, ServiceUpdated},
		{storage.Change{Before: &tagged, After: deleted}, ServiceDeregistered},
		{storage.Change{After: deleted}, ""},
	}
	for i, c := range cases {
		event, ok := FromServiceChange(c.change)
		if c.want == "" {
			if ok {
				t.Errorf("case %d: got %s, want no event", i, event.Type)
			}
			continue
		}
		if !ok || event.Type != c.want || event.Kind != KindService || event.Service == nil {
			t.Errorf("case %d: got %+v ok=%v, want %s", i, event, ok, c.want)
		}
	}
	member := FromMember(membership.Event{Type: membership.EventJoin, Member: membership.Member{ID: "n"}})
	if member.Type != "join" || member.Kind != KindMember || member.Member.ID != "n" {
		t.Fatalf("member event = %+v", member)
	}
}
