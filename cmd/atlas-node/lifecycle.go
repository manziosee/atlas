package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/manziosee/atlas/internal/events"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/storage"
)

// publishEvents feeds membership changes and registry writes into broker.
func publishEvents(ctx context.Context, ml *membership.Memberlist, registry *storage.Store, broker *events.Broker) {
	registry.OnChange(func(change storage.Change) {
		if event, ok := events.FromServiceChange(change); ok {
			broker.Publish(event)
		}
	})
	memberEvents, cancel := ml.Subscribe(256)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-memberEvents:
			if !ok {
				return
			}
			broker.Publish(events.FromMember(event))
		}
	}
}

// expireTTLServices marks this node's silent TTL services unhealthy and then
// deregisters them (storage.TTLDeregisterFactor).
func expireTTLServices(ctx context.Context, registry *storage.Store, nodeID string, log *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			unhealthy, deregistered, err := registry.ExpireTTL(ctx, nodeID, now)
			if err != nil && ctx.Err() == nil {
				log.Warn("TTL expiry failed", "err", err)
			}
			if unhealthy+deregistered > 0 {
				log.Info("TTL services expired", "unhealthy", unhealthy, "deregistered", deregistered)
			}
		}
	}
}

// orphanReaper deletes services whose owner has been missing from the live
// member list for longer than after. Only the live member with the lowest ID
// reaps, so one node normally writes each tombstone; duplicates from a brief
// disagreement are harmless because tombstones merge by revision.
type orphanReaper struct {
	self   string
	after  time.Duration
	absent map[string]time.Time // owner -> first time seen missing
}

func newOrphanReaper(self string, after time.Duration) *orphanReaper {
	return &orphanReaper{self: self, after: after, absent: make(map[string]time.Time)}
}

// due returns owners whose services should be deleted now. The absence clock
// lives in memory, so a restarted node waits a full period before reaping.
func (o *orphanReaper) due(owners []string, members []membership.Member, now time.Time) []string {
	live := map[string]bool{o.self: true}
	leader := o.self
	for _, member := range members {
		if member.State == membership.StateAlive || member.State == membership.StateSuspect {
			live[member.ID] = true
			if member.State == membership.StateAlive && member.ID < leader {
				leader = member.ID
			}
		}
	}
	var reap []string
	present := make(map[string]bool, len(owners))
	for _, owner := range owners {
		present[owner] = true
		if live[owner] {
			delete(o.absent, owner)
			continue
		}
		since, seen := o.absent[owner]
		if !seen {
			o.absent[owner] = now
			continue
		}
		if leader == o.self && now.Sub(since) >= o.after {
			reap = append(reap, owner)
			delete(o.absent, owner)
		}
	}
	for owner := range o.absent {
		if !present[owner] {
			delete(o.absent, owner)
		}
	}
	return reap
}

func reapOrphanedServices(ctx context.Context, registry *storage.Store, ml *membership.Memberlist, after time.Duration, log *slog.Logger) {
	if after <= 0 {
		return
	}
	reaper := newOrphanReaper(ml.LocalNode().ID, after)
	ticker := time.NewTicker(min(after/10, 30*time.Second))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			owners, err := registry.OwnerNodes(ctx)
			if err != nil {
				if ctx.Err() == nil {
					log.Warn("could not list service owners", "err", err)
				}
				continue
			}
			for _, owner := range reaper.due(owners, ml.Members(), now) {
				removed, err := registry.TombstoneOwnedBy(ctx, owner)
				if err != nil {
					log.Warn("could not delete orphaned services", "owner", owner, "err", err)
					continue
				}
				log.Info("deleted services of departed node", "owner", owner, "services", removed, "absent_for", after)
			}
		}
	}
}
