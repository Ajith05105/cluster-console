// Package record keeps the two records of a run that the poster graphs are
// made from:
//
//   - the EVENT LOG: one line each time something happens (a button was
//     pressed, a node went away, a pod became ready, a warning started...)
//   - the STATS: one line per second with the load numbers and pod counts
//
// Both are written to CSV files as they happen, with UTC timestamps, and the
// most recent entries are also kept in memory so a browser that has just
// connected can be shown what happened so far.
//
// The stats are only written to disk while there is something to measure (a
// workload is deployed or load is running). An idle console writes nothing,
// which spares the disk it runs on. See the tick function in api/server.go.
package record

import (
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// timeLayout is how timestamps are written in the CSV files: UTC, to the
// millisecond, for example 2026-10-08T05:12:33.250Z.
const timeLayout = "2006-01-02T15:04:05.000Z"

// How many recent entries are kept in memory.
const (
	recentEvents = 500
	recentStats  = 900 // 15 minutes at one per second
)

// Event is one line of the event log.
type Event struct {
	Time time.Time `json:"time"`
	// Kind groups events so they can be filtered:
	//   mark     a named moment added by a person ("power cut")
	//   action   something done through the console (deploy, scale...)
	//   cluster  something the console saw happen in the cluster
	//   load     the load generator was started, changed or stopped
	//   warning  a warning started or ended
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// EventLog collects events, writes them to a CSV file and tells anyone who
// has asked to be told (for example the live stream to browsers).
type EventLog struct {
	mu        sync.Mutex // guards everything below; only one writer at a time
	recent    []Event
	file      *os.File
	writer    *csv.Writer
	listeners []func(Event)
}

// NewEventLog opens (or creates) the events CSV at path. An empty path means
// "keep events in memory only", which the tests use.
func NewEventLog(path string) (*EventLog, error) {
	log := &EventLog{}
	if path == "" {
		return log, nil
	}
	file, writer, err := openCSV(path, []string{"time_utc", "kind", "message"})
	if err != nil {
		return nil, err
	}
	log.file, log.writer = file, writer
	return log, nil
}

// Add records one event, stamped with the current time.
func (l *EventLog) Add(kind, message string) Event {
	event := Event{Time: time.Now().UTC(), Kind: kind, Message: message}

	l.mu.Lock()
	l.recent = append(l.recent, event)
	if len(l.recent) > recentEvents {
		l.recent = l.recent[len(l.recent)-recentEvents:]
	}
	if l.writer != nil {
		_ = l.writer.Write([]string{event.Time.Format(timeLayout), event.Kind, event.Message})
		l.writer.Flush() // write to disk now, so nothing is lost if the pod dies
	}
	// Copy the list of listeners so they can be called after unlocking.
	listeners := append([]func(Event){}, l.listeners...)
	l.mu.Unlock()

	for _, tell := range listeners {
		tell(event)
	}
	return event
}

// Addf is Add with a fmt-style message.
func (l *EventLog) Addf(kind, format string, args ...any) Event {
	return l.Add(kind, fmt.Sprintf(format, args...))
}

// Recent returns a copy of the events still held in memory, oldest first.
func (l *EventLog) Recent() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Event{}, l.recent...)
}

// Subscribe asks for tell to be called with every future event.
func (l *EventLog) Subscribe(tell func(Event)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.listeners = append(l.listeners, tell)
}

// Close flushes and closes the CSV file.
func (l *EventLog) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.writer.Flush()
		_ = l.file.Close()
	}
}

// StatsRow is one second of numbers: what the load generator did, and what
// the cluster looked like at the end of that second.
type StatsRow struct {
	Time time.Time `json:"time"`

	// What was asked of the load generator.
	Cameras int     `json:"cameras"`
	FPS     float64 `json:"fps"`
	Demand  float64 `json:"demand"` // cameras x fps, in frames per second

	// What happened during this second.
	Sent          int `json:"sent"`           // frames sent
	Processed     int `json:"processed"`      // frames answered with 200 OK
	Failed        int `json:"failed"`         // all failures (sum of the three below)
	FailedBusy    int `json:"failed_busy"`    // answered 503: pod was full
	FailedTimeout int `json:"failed_timeout"` // no answer in time
	FailedOther   int `json:"failed_other"`   // any other error

	LatencyMeanMs float64 `json:"latency_mean_ms"` // of the processed frames
	LatencyP95Ms  float64 `json:"latency_p95_ms"`  // 95% were faster than this

	// PerNode is how many processed frames each node answered, taken from
	// the X-Node header of the replies.
	PerNode map[string]int `json:"per_node"`

	// The cluster at the end of this second.
	DesiredPods int `json:"desired_pods"`
	ReadyPods   int `json:"ready_pods"`
	PendingPods int `json:"pending_pods"`
	ReadyNodes  int `json:"ready_nodes"` // ready nodes in the test pool
}

// StatsWriter writes StatsRows to a CSV file and remembers the recent ones.
type StatsWriter struct {
	mu     sync.Mutex
	recent []StatsRow
	file   *os.File
	writer *csv.Writer
}

// statsColumns is the header line of the stats CSV.
var statsColumns = []string{
	"time_utc", "cameras", "fps", "demand_fps",
	"sent", "processed", "failed", "failed_busy", "failed_timeout", "failed_other",
	"latency_mean_ms", "latency_p95_ms",
	"desired_pods", "ready_pods", "pending_pods", "ready_nodes",
	"per_node",
}

// NewStatsWriter opens (or creates) the stats CSV at path. An empty path
// means "memory only".
func NewStatsWriter(path string) (*StatsWriter, error) {
	stats := &StatsWriter{}
	if path == "" {
		return stats, nil
	}
	file, writer, err := openCSV(path, statsColumns)
	if err != nil {
		return nil, err
	}
	stats.file, stats.writer = file, writer
	return stats, nil
}

// Remember keeps one second in memory only. Nothing is written to disk.
//
// The page's charts are fed from memory, so they carry on updating every
// second whether or not the row is also written to the CSV file.
func (s *StatsWriter) Remember(row StatsRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remember(row)
}

// Write keeps one second in memory AND adds it to the CSV file on disk.
func (s *StatsWriter) Write(row StatsRow) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.remember(row)
	if s.writer == nil {
		return
	}
	_ = s.writer.Write([]string{
		row.Time.UTC().Format(timeLayout),
		strconv.Itoa(row.Cameras),
		formatNumber(row.FPS),
		formatNumber(row.Demand),
		strconv.Itoa(row.Sent),
		strconv.Itoa(row.Processed),
		strconv.Itoa(row.Failed),
		strconv.Itoa(row.FailedBusy),
		strconv.Itoa(row.FailedTimeout),
		strconv.Itoa(row.FailedOther),
		formatNumber(row.LatencyMeanMs),
		formatNumber(row.LatencyP95Ms),
		strconv.Itoa(row.DesiredPods),
		strconv.Itoa(row.ReadyPods),
		strconv.Itoa(row.PendingPods),
		strconv.Itoa(row.ReadyNodes),
		FormatPerNode(row.PerNode),
	})
	s.writer.Flush()
}

// remember adds a row to the in-memory list, dropping the oldest once the
// list is full. The caller must hold s.mu.
func (s *StatsWriter) remember(row StatsRow) {
	s.recent = append(s.recent, row)
	if len(s.recent) > recentStats {
		s.recent = s.recent[len(s.recent)-recentStats:]
	}
}

// Recent returns a copy of the rows still held in memory, oldest first.
func (s *StatsWriter) Recent() []StatsRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]StatsRow{}, s.recent...)
}

// Close flushes and closes the CSV file.
func (s *StatsWriter) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		s.writer.Flush()
		_ = s.file.Close()
	}
}

// FormatPerNode writes the per-node counts as one CSV cell, for example
// "agent-10=12;agent-7=13", with the node names in alphabetical order so the
// same nodes always appear in the same order.
func FormatPerNode(perNode map[string]int) string {
	names := make([]string, 0, len(perNode))
	for name := range perNode {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+strconv.Itoa(perNode[name]))
	}
	return strings.Join(parts, ";")
}

// formatNumber writes a number with at most two decimal places and no
// trailing zeros: 12.50 becomes "12.5", 3.00 becomes "3".
func formatNumber(value float64) string {
	return strconv.FormatFloat(float64(int64(value*100+0.5))/100, 'f', -1, 64)
}

// openCSV opens a CSV file for adding lines to the end, and writes the header
// line if the file is new.
func openCSV(path string, header []string) (*os.File, *csv.Writer, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot open %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("cannot inspect %s: %w", path, err)
	}
	writer := csv.NewWriter(file)
	if info.Size() == 0 {
		_ = writer.Write(header)
		writer.Flush()
	}
	return file, writer, nil
}
