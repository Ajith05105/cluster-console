package record

// Automated tests for the CSV files. The poster graphs are made from these
// files, so their exact layout is worth pinning down.

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// readCSV reads a whole CSV file into rows of cells.
func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("cannot open %s: %v", path, err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatalf("%s is not valid CSV: %v", path, err)
	}
	return rows
}

// utcTimestamp is the timestamp format both files use.
var utcTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

// Events are written to the file as they happen, with a header and UTC
// timestamps, and listeners are told about each one.
func TestEventLogWritesCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.csv")
	log, err := NewEventLog(path)
	if err != nil {
		t.Fatalf("NewEventLog failed: %v", err)
	}
	var heard []string
	log.Subscribe(func(event Event) { heard = append(heard, event.Message) })

	log.Add("mark", "power cut")
	log.Addf("cluster", "node %s is NotReady", "agent-7")
	log.Add("action", `text with a comma, a "quote" and nothing else odd`)

	// Read the file while the log is still open: every event must already
	// be on disk, not waiting in a buffer.
	rows := readCSV(t, path)
	if len(rows) != 4 {
		t.Fatalf("file has %d rows, want a header and 3 events", len(rows))
	}
	if strings.Join(rows[0], ",") != "time_utc,kind,message" {
		t.Errorf("header = %v", rows[0])
	}
	for _, row := range rows[1:] {
		if !utcTimestamp.MatchString(row[0]) {
			t.Errorf("timestamp %q is not in the form 2026-10-08T05:12:33.250Z", row[0])
		}
	}
	if rows[1][1] != "mark" || rows[1][2] != "power cut" || rows[2][2] != "node agent-7 is NotReady" {
		t.Errorf("rows = %v", rows[1:])
	}
	if rows[3][2] != `text with a comma, a "quote" and nothing else odd` {
		t.Errorf("awkward text did not survive the round trip: %q", rows[3][2])
	}
	if len(heard) != 3 || len(log.Recent()) != 3 {
		t.Errorf("listeners heard %d events and Recent holds %d; want 3 and 3", len(heard), len(log.Recent()))
	}
	log.Close()

	// Opening the same file again adds to it without a second header.
	again, _ := NewEventLog(path)
	again.Add("mark", "power restored")
	again.Close()
	if rows := readCSV(t, path); len(rows) != 5 || rows[4][2] != "power restored" {
		t.Errorf("after reopening: %d rows, last %v; want 5 rows ending with the new event", len(rows), rows[len(rows)-1])
	}
}

// Stats rows are written with the documented columns, in order.
func TestStatsWriterWritesCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.csv")
	stats, err := NewStatsWriter(path)
	if err != nil {
		t.Fatalf("NewStatsWriter failed: %v", err)
	}
	// A time in a non-UTC zone, to check it is converted.
	zone := time.FixedZone("NZDT", 13*3600)
	stats.Write(StatsRow{
		Time:    time.Date(2026, 10, 8, 18, 12, 33, 250_000_000, zone),
		Cameras: 20, FPS: 5, Demand: 100,
		Sent: 100, Processed: 97, Failed: 3, FailedBusy: 2, FailedTimeout: 1, FailedOther: 0,
		LatencyMeanMs: 61.2345, LatencyP95Ms: 120,
		PerNode:     map[string]int{"agent-7": 48, "agent-10": 49},
		DesiredPods: 7, ReadyPods: 6, PendingPods: 1, ReadyNodes: 2,
	})
	stats.Write(StatsRow{Time: time.Date(2026, 10, 8, 5, 12, 34, 0, time.UTC), PerNode: map[string]int{}})

	rows := readCSV(t, path)
	if len(rows) != 3 {
		t.Fatalf("file has %d rows, want a header and 2 rows", len(rows))
	}
	wantHeader := "time_utc,cameras,fps,demand_fps,sent,processed,failed,failed_busy,failed_timeout,failed_other," +
		"latency_mean_ms,latency_p95_ms,desired_pods,ready_pods,pending_pods,ready_nodes,per_node"
	if strings.Join(rows[0], ",") != wantHeader {
		t.Errorf("header = %s\nwant     %s", strings.Join(rows[0], ","), wantHeader)
	}
	wantRow := "2026-10-08T05:12:33.250Z,20,5,100,100,97,3,2,1,0,61.23,120,7,6,1,2,agent-10=49;agent-7=48"
	if strings.Join(rows[1], ",") != wantRow {
		t.Errorf("row  = %s\nwant   %s", strings.Join(rows[1], ","), wantRow)
	}
	if rows[2][0] != "2026-10-08T05:12:34.000Z" || rows[2][16] != "" {
		t.Errorf("idle row = %v; want the timestamp and an empty per_node cell", rows[2])
	}
	if len(stats.Recent()) != 2 {
		t.Errorf("Recent holds %d rows, want 2", len(stats.Recent()))
	}
	stats.Close()
}

// Remember keeps a row for the charts without touching the file.
func TestRememberDoesNotWriteToDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.csv")
	stats, err := NewStatsWriter(path)
	if err != nil {
		t.Fatalf("NewStatsWriter failed: %v", err)
	}
	sizeBefore := fileSize(t, path) // just the header line

	for i := 0; i < 5; i++ {
		stats.Remember(StatsRow{Time: time.Date(2026, 10, 9, 3, 0, i, 0, time.UTC), ReadyNodes: 3})
	}
	if got := len(stats.Recent()); got != 5 {
		t.Errorf("memory holds %d rows after 5 Remember calls, want 5", got)
	}
	if got := fileSize(t, path); got != sizeBefore {
		t.Errorf("the file grew from %d to %d bytes; Remember must not write to disk", sizeBefore, got)
	}

	// Write does both, and the remembered rows are not written late.
	stats.Write(StatsRow{Time: time.Date(2026, 10, 9, 3, 0, 5, 0, time.UTC), Sent: 50})
	if got := len(stats.Recent()); got != 6 {
		t.Errorf("memory holds %d rows, want 6", got)
	}
	if rows := readCSV(t, path); len(rows) != 2 || rows[1][0] != "2026-10-09T03:00:05.000Z" || rows[1][4] != "50" {
		t.Errorf("file rows = %v; want the header and only the one written row", rows)
	}
	stats.Close()
}

// fileSize returns a file's size in bytes.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("cannot inspect %s: %v", path, err)
	}
	return info.Size()
}

// Memory keeps only the most recent entries, so a long run cannot use it up.
func TestMemoryIsBounded(t *testing.T) {
	log, _ := NewEventLog("")
	for i := 0; i < recentEvents+50; i++ {
		log.Add("cluster", "event")
	}
	if got := len(log.Recent()); got != recentEvents {
		t.Errorf("event log holds %d events in memory, want %d", got, recentEvents)
	}
	stats, _ := NewStatsWriter("")
	for i := 0; i < recentStats+50; i++ {
		stats.Write(StatsRow{})
	}
	if got := len(stats.Recent()); got != recentStats {
		t.Errorf("stats holds %d rows in memory, want %d", got, recentStats)
	}
}
