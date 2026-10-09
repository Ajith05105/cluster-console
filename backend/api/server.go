// Package api is the console's web API: the addresses the React page calls,
// the live stream it listens to, and the once-a-second loop that gathers the
// numbers, works out the warnings and writes the CSV files.
//
// Reading is open; changing is locked:
//
//   - every GET works without a token (looking is harmless)
//   - every POST needs the shared access token, and is refused outright in
//     read-only mode or when no token has been configured
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"cluster-console/backend/catalog"
	"cluster-console/backend/cluster"
	"cluster-console/backend/loadgen"
	"cluster-console/backend/record"
	"cluster-console/backend/warnings"
)

// Config is the API's settings, read from environment variables in main.
type Config struct {
	// Token is the shared access token every POST must present. It comes
	// from a Kubernetes Secret. If it is empty, every POST is refused.
	Token string
	// ReadOnly switches every POST off, token or no token.
	ReadOnly bool
	// DataDir is the folder the CSV files are written to and downloaded
	// from. Empty means there are no files.
	DataDir string
}

// Server ties the pieces together. Make one with New.
type Server struct {
	config  Config
	catalog *catalog.Catalog
	cluster *cluster.Cluster
	load    *loadgen.Generator
	events  *record.EventLog
	stats   *record.StatsWriter
	engine  *warnings.Engine
	hub     *hub

	// mu guards the three fields below.
	mu sync.Mutex
	// recording is whether last second's stats row was written to disk.
	// It is only used to notice the moment recording starts or stops.
	recording bool
	// warnings is the list worked out at the last tick.
	warnings []warnings.Warning
	// window is the last few seconds of load counts, which the
	// failed-frames rule looks at.
	window []warnings.LoadSecond

	// changed is a one-slot mailbox: the cluster watcher drops a note in it
	// to say "something changed", and the publisher loop picks it up.
	changed chan struct{}
}

// Snapshot is the answer to GET /api/state and the "state" stream message.
type Snapshot struct {
	Time     time.Time          `json:"time"`
	ReadOnly bool               `json:"read_only"`
	Cluster  cluster.State      `json:"cluster"`
	Load     loadgen.Status     `json:"load"`
	Warnings []warnings.Warning `json:"warnings"`
}

// New builds the Server. Call Run to start its background loops and Handler
// to get something to serve HTTP with.
func New(config Config, cat *catalog.Catalog, cl *cluster.Cluster, load *loadgen.Generator,
	events *record.EventLog, stats *record.StatsWriter) *Server {
	s := &Server{
		config:   config,
		catalog:  cat,
		cluster:  cl,
		load:     load,
		events:   events,
		stats:    stats,
		engine:   warnings.NewEngine(),
		hub:      newHub(),
		warnings: []warnings.Warning{},
		changed:  make(chan struct{}, 1),
	}
	// Every new event-log line goes straight out on the live stream.
	events.Subscribe(func(event record.Event) { s.hub.publish("event", event) })
	return s
}

// ClusterChanged is called by the cluster watcher whenever anything changes.
// It only leaves a note; the actual work happens in Run. If a note is
// already waiting, this one is not needed.
func (s *Server) ClusterChanged() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// Run starts the two background loops and returns when ctx is cancelled.
func (s *Server) Run(ctx context.Context) {
	// Loop 1: when the cluster changes, send browsers a fresh snapshot.
	// Changes often arrive in bursts (twenty pods starting at once), so
	// wait a fifth of a second and send one snapshot for the whole burst.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.changed:
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			s.hub.publish("state", s.snapshot())
		}
	}()

	// Loop 2: once a second, on the second.
	untilNextSecond := time.Until(time.Now().Truncate(time.Second).Add(time.Second))
	select {
	case <-ctx.Done():
		return
	case <-time.After(untilNextSecond):
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for count := 0; ; count++ {
		s.tick(time.Now())
		// Also send a full snapshot every five seconds even if nothing
		// changed, as a safety net for a browser that missed a message.
		if count%5 == 0 {
			s.ClusterChanged()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// tick is the once-a-second job: collect the load generator's counts for the
// second just ended, note what the cluster looks like, write the CSV row,
// and work out the warnings.
func (s *Server) tick(now time.Time) {
	second := s.load.TakeSecond()
	state := s.cluster.Snapshot()

	row := record.StatsRow{
		Time:          now.UTC(),
		Cameras:       second.Cameras,
		FPS:           second.FPS,
		Demand:        second.Demand,
		Sent:          second.Sent,
		Processed:     second.Processed,
		Failed:        second.Failed,
		FailedBusy:    second.FailedBusy,
		FailedTimeout: second.FailedTimeout,
		FailedOther:   second.FailedOther,
		LatencyMeanMs: second.LatencyMeanMs,
		LatencyP95Ms:  second.LatencyP95Ms,
		PerNode:       second.PerNode,
		PendingPods:   len(state.Pending),
	}
	if state.Workload != nil {
		row.DesiredPods = state.Workload.Desired
		row.ReadyPods = state.Workload.Ready
	}
	for _, node := range state.Nodes {
		if node.Pool == "test" && node.Ready {
			row.ReadyNodes++
		}
	}
	// Write the row to the CSV file only when there is something to measure:
	// a workload is deployed, load is running, or frames sent a moment ago
	// are still being answered. An idle console writes nothing to disk, so
	// it does not wear the card it runs on or fill it with rows of zeros.
	// The row is always kept in memory and sent to browsers, so the page's
	// charts keep moving either way.
	active := state.Workload != nil || s.load.Status().Running ||
		second.Sent > 0 || second.Processed > 0 || second.Failed > 0
	if active {
		s.stats.Write(row)
	} else {
		s.stats.Remember(row)
	}
	s.hub.publish("stats", row)

	// Say so in the event log when recording starts or stops, so that a gap
	// in the stats CSV can be explained later.
	s.mu.Lock()
	changed := active != s.recording
	s.recording = active
	s.mu.Unlock()
	if changed && active {
		s.events.Add("action", "recording stats to the CSV file")
	}
	if changed && !active {
		s.events.Add("action", "stopped recording stats to the CSV file (nothing deployed and no load)")
	}

	// Work out the warnings and compare with last second's list.
	s.mu.Lock()
	s.window = append(s.window, warnings.LoadSecond{
		Sent: second.Sent, Failed: second.Failed,
		FailedBusy: second.FailedBusy, FailedTimeout: second.FailedTimeout, FailedOther: second.FailedOther,
	})
	if len(s.window) > warnings.FailedFramesWindow {
		s.window = s.window[len(s.window)-warnings.FailedFramesWindow:]
	}
	before := s.warnings
	after := s.engine.Evaluate(warningInput(now, state, s.window))
	s.warnings = after
	s.mu.Unlock()

	// A warning that is new, or has gone, gets a line in the event log.
	for _, warning := range after {
		if !containsWarning(before, warning.ID) {
			s.events.Add("warning", "WARNING: "+warning.Reason)
		}
	}
	for _, warning := range before {
		if !containsWarning(after, warning.ID) {
			s.events.Add("warning", "cleared: "+warning.Reason)
		}
	}
	// The wording changes every second ("for 23 s", "for 24 s"), so send
	// the list whenever there is anything in it, and once more when it
	// empties.
	if len(after) > 0 || len(before) > 0 {
		s.hub.publish("warnings", after)
	}
}

// warningInput translates the cluster snapshot into the plain facts the
// warnings package works from.
func warningInput(now time.Time, state cluster.State, window []warnings.LoadSecond) warnings.Input {
	in := warnings.Input{Now: now, Recent: append([]warnings.LoadSecond{}, window...)}

	for _, pod := range state.Pending {
		pending := warnings.PendingPod{Name: pod.Name, Unschedulable: pod.Unschedulable, Reason: pod.PendingReason}
		if pod.PendingSince != nil {
			pending.Since = *pod.PendingSince
		}
		in.Pending = append(in.Pending, pending)
	}
	for _, node := range state.Nodes {
		if !node.Known {
			continue // we know nothing about its health
		}
		entry := warnings.Node{Name: node.Name, Ready: node.Ready}
		if node.NotReadySince != nil {
			entry.NotReadySince = *node.NotReadySince
		}
		in.Nodes = append(in.Nodes, entry)
	}
	if state.Workload != nil {
		in.Workload = &warnings.Workload{Name: state.Workload.Key, Desired: state.Workload.Desired, Ready: state.Workload.Ready}
	}
	for _, app := range state.Argo {
		in.ArgoApps = append(in.ArgoApps, warnings.ArgoApp{Name: app.Name, Sync: app.Sync})
	}
	return in
}

// containsWarning reports whether a list has a warning with the given ID.
func containsWarning(list []warnings.Warning, id string) bool {
	for _, warning := range list {
		if warning.ID == id {
			return true
		}
	}
	return false
}

// snapshot gathers the current picture.
func (s *Server) snapshot() Snapshot {
	return Snapshot{
		Time:     time.Now().UTC(),
		ReadOnly: s.changesAreOff(),
		Cluster:  s.cluster.Snapshot(),
		Load:     s.load.Status(),
		Warnings: s.currentWarnings(),
	}
}

// currentWarnings returns a copy of the list worked out at the last tick.
func (s *Server) currentWarnings() []warnings.Warning {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]warnings.Warning{}, s.warnings...)
}

// changesAreOff is true when POSTs are refused whatever token is sent:
// read-only mode, or no token configured.
func (s *Server) changesAreOff() bool {
	return s.config.ReadOnly || s.config.Token == ""
}

// hello is the fixed information a browser needs once: what can be deployed,
// what the limits are, and whether changes are switched off.
func (s *Server) hello() map[string]any {
	return map[string]any{
		"read_only": s.changesAreOff(),
		"catalog":   s.catalog,
		"limits": map[string]any{
			"max_rate":     s.load.Status().MaxRate,
			"max_cameras":  loadgen.MaxCameras,
			"min_fps":      loadgen.MinFPS,
			"max_fps":      loadgen.MaxFPS,
			"max_replicas": catalog.MaxReplicasCeiling,
		},
		"route_host": catalog.RouteHost,
		"namespace":  catalog.Namespace,
	}
}

// Handler returns the thing that answers HTTP requests: a table of
// "method and path" to "function that handles it".
//
// page serves the React page for every address that is not part of the API.
// It may be nil (the tests do not need a page), in which case only the API
// is served.
func (s *Server) Handler(page http.Handler) http.Handler {
	mux := http.NewServeMux()
	if page != nil {
		mux.Handle("/", page)
	}
	// An address under /api/ that matches nothing below is a mistake in the
	// caller, so answer with a JSON error instead of the page.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "no such API address: "+r.Method+" "+r.URL.Path)
	})

	// Reading: no token needed.
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, s.snapshot()) })
	mux.HandleFunc("GET /api/catalog", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, s.hello()) })
	mux.HandleFunc("GET /api/stream", s.handleStream)
	mux.HandleFunc("GET /api/files", s.handleFileList)
	mux.HandleFunc("GET /api/files/{name}", s.handleFileDownload)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok\n")) })

	// Changing: every one of these goes through guard. They are all
	// registered through this one helper so that none can be added without it.
	post := func(path string, handler http.HandlerFunc) {
		mux.HandleFunc("POST "+path, s.guard(handler))
	}
	for path, handler := range s.postRoutes() {
		post(path, handler)
	}
	return mux
}

// postRoutes lists every address that changes something. Keeping them in one
// table lets the tests check that all of them are protected.
func (s *Server) postRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/api/deploy":       s.handleDeploy,
		"/api/undeploy":     s.handleUndeploy,
		"/api/scale":        s.handleScale,
		"/api/pod/delete":   s.handlePodDelete,
		"/api/load/start":   s.handleLoadStart,
		"/api/load/cameras": s.handleLoadCameras,
		"/api/load/stop":    s.handleLoadStop,
		"/api/mark":         s.handleMark,
		// Does nothing; lets the page check a token the user has typed in.
		"/api/auth/check": func(w http.ResponseWriter, r *http.Request) { ok(w, "token accepted") },
	}
}

// guard wraps a handler so it only runs for requests allowed to change
// things. It is the single place the access rules are enforced.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.config.ReadOnly {
			fail(w, http.StatusForbidden, "the console is in read-only mode")
			return
		}
		if s.config.Token == "" {
			fail(w, http.StatusForbidden, "no access token is configured, so changes are switched off")
			return
		}
		// The token arrives in a header:  Authorization: Bearer <token>
		given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		// ConstantTimeCompare takes the same time whether the first or the
		// last character is wrong, so the time taken gives nothing away.
		if subtle.ConstantTimeCompare([]byte(given), []byte(s.config.Token)) != 1 {
			fail(w, http.StatusUnauthorized, "missing or wrong access token")
			return
		}
		next(w, r)
	}
}

// readJSON reads the request body into `into`. It refuses bodies that are
// too large, are not JSON, or contain a field the endpoint does not know.
// That last rule matters for safety: a request that tries to slip in, say,
// "namespace": "kube-system" is rejected instead of having the extra field
// quietly ignored. It returns false if it has already sent an error reply.
func readJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		fail(w, http.StatusBadRequest, "the request is not the JSON this address expects: "+err.Error())
		return false
	}
	return true
}

// writeJSON sends a value as a JSON reply.
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// ok sends the standard "it worked" reply.
func ok(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message})
}

// fail sends the standard error reply.
func fail(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": message})
}

// failWith sends the right error reply for an error from the cluster
// package: the caller's own mistakes keep their status (400, 404, 409);
// anything else means Kubernetes refused or failed, reported as 502.
func failWith(w http.ResponseWriter, err error) {
	var mistake *cluster.UserError
	if errors.As(err, &mistake) {
		fail(w, mistake.Status, mistake.Message)
		return
	}
	log.Printf("api: cluster action failed: %v", err)
	fail(w, http.StatusBadGateway, "Kubernetes did not accept that: "+err.Error())
}
