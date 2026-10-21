package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/manziosee/atlas/internal/membership"
)

type eventSubscriber interface {
	Subscribe(int) (<-chan membership.Event, func())
}

func (d Deps) streamEvents(w http.ResponseWriter, r *http.Request) {
	source, ok := d.Cluster.(eventSubscriber)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "event stream unavailable"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	if err := controller.Flush(); err != nil {
		return
	}
	events, cancel := source.Subscribe(64)
	defer cancel()
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	if err := controller.Flush(); err != nil {
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			_ = controller.SetWriteDeadline(time.Now().Add(20 * time.Second))
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		case <-heartbeat.C:
			_ = controller.SetWriteDeadline(time.Now().Add(20 * time.Second))
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}
}
