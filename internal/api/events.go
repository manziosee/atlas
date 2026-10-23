package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/manziosee/atlas/internal/events"
	"github.com/manziosee/atlas/internal/httperr"
)

// streamEvents serves membership and registry changes as Server-Sent Events.
// Query parameters: kinds=member,service limits event kinds; service=<name>
// limits service events to one service name. Clients resume with the standard
// Last-Event-ID header (or last_event_id query parameter for clients that
// cannot set headers). An "event: reset" frame means the gap could not be
// replayed and the client should re-read current state.
func (d Deps) streamEvents(w http.ResponseWriter, r *http.Request) {
	if d.Events == nil {
		httperr.Write(w, http.StatusNotImplemented, httperr.CodeUnavailable, "event stream unavailable")
		return
	}
	query := r.URL.Query()
	kinds := map[string]bool{events.KindMember: true, events.KindService: true}
	if raw := query.Get("kinds"); raw != "" {
		kinds = map[string]bool{}
		for kind := range strings.SplitSeq(raw, ",") {
			kind = strings.TrimSpace(kind)
			if kind != events.KindMember && kind != events.KindService {
				httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "kinds must contain member and/or service")
				return
			}
			kinds[kind] = true
		}
	}
	serviceName := query.Get("service")
	lastID := r.Header.Get("Last-Event-ID")
	if lastID == "" {
		lastID = query.Get("last_event_id")
	}
	if len(serviceName) > 128 || len(lastID) > 64 {
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "event stream parameters exceed limits")
		return
	}
	wanted := func(event events.Event) bool {
		if !kinds[event.Kind] {
			return false
		}
		return serviceName == "" || event.Kind != events.KindService || event.Service.Name == serviceName
	}

	// Subscribe before the headers go out, so a client that sees the stream
	// open cannot miss an event published in between.
	sub, backlog, reset := d.Events.Subscribe(lastID, 256)
	defer sub.Close()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	if err := controller.Flush(); err != nil {
		return
	}
	write := func(frame string) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(20 * time.Second))
		if _, err := fmt.Fprint(w, frame); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if !write(": connected\nretry: 3000\n\n") {
		return
	}
	if reset && !write("event: reset\ndata: {\"type\":\"reset\"}\n\n") {
		return
	}
	send := func(event events.Event) bool {
		if !wanted(event) {
			return true
		}
		data, err := json.Marshal(event)
		if err != nil {
			return true
		}
		return write(fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.Type, data))
	}
	for _, event := range backlog {
		if !send(event) {
			return
		}
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-sub.C:
			// A closed channel means this reader lagged; ending the response
			// makes the client reconnect with Last-Event-ID and catch up.
			if !ok || !send(event) {
				return
			}
		case <-heartbeat.C:
			if !write(": keepalive\n\n") {
				return
			}
		}
	}
}
