package api

// This file is the live stream to browsers (GET /api/stream).
//
// It uses Server-Sent Events: the browser opens one long-lived connection and
// the console writes small named messages down it as things happen. In React
// this is read with the built-in EventSource object:
//
//	const stream = new EventSource("/api/stream");
//	stream.addEventListener("stats", e => { const row = JSON.parse(e.data); ... });
//
// The message names are:
//
//	hello     once, on connecting: catalog, limits, and whether read-only
//	history   once, on connecting: recent stats rows and events, to fill
//	          the charts and the event log
//	state     the full cluster snapshot, whenever something changes
//	stats     one row per second
//	warnings  the full list of current warnings, whenever it changes
//	event     one event-log line, as it happens

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// message is one named message waiting to be written to a browser.
type message struct {
	name string
	data []byte // JSON
}

// hub keeps track of the connected browsers and hands each new message to
// all of them.
type hub struct {
	mu sync.Mutex
	// clients holds one queue per connected browser.
	clients map[chan message]bool
}

func newHub() *hub {
	return &hub{clients: map[chan message]bool{}}
}

// publish sends a message to every connected browser.
func (h *hub) publish(name string, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for queue := range h.clients {
		select {
		case queue <- message{name, data}:
		default:
			// This browser's queue is full: it is not keeping up (a
			// laptop that went to sleep, say). Drop it instead of letting
			// it hold everyone else up. The browser reconnects by itself.
			delete(h.clients, queue)
			close(queue)
		}
	}
}

// join adds a browser and returns its queue.
func (h *hub) join() chan message {
	queue := make(chan message, 256)
	h.mu.Lock()
	h.clients[queue] = true
	h.mu.Unlock()
	return queue
}

// leave removes a browser. Safe to call even if publish already dropped it.
func (h *hub) leave(queue chan message) {
	h.mu.Lock()
	if h.clients[queue] {
		delete(h.clients, queue)
		close(queue)
	}
	h.mu.Unlock()
}

// handleStream serves GET /api/stream.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		http.Error(w, "streaming is not supported on this connection", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// Tells proxies (Traefik included) not to hold messages back in a buffer.
	w.Header().Set("X-Accel-Buffering", "no")

	// write puts one message on the wire in the Server-Sent Events format
	// and pushes it out immediately.
	write := func(name string, data []byte) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
		flusher.Flush()
	}
	writeValue := func(name string, value any) {
		if data, err := json.Marshal(value); err == nil {
			write(name, data)
		}
	}

	// Join first, then send the catch-up messages, so nothing published in
	// between is missed.
	queue := s.hub.join()
	defer s.hub.leave(queue)

	writeValue("hello", s.hello())
	writeValue("history", map[string]any{"stats": s.stats.Recent(), "events": s.events.Recent()})
	writeValue("state", s.snapshot())
	writeValue("warnings", s.currentWarnings())

	// A comment line every 15 seconds keeps the connection from being
	// closed as idle when nothing else is being sent.
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return // the browser went away
		case next, open := <-queue:
			if !open {
				return // dropped for being too slow
			}
			write(next.name, next.data)
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}
