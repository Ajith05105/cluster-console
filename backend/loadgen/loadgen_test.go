package loadgen

// Automated tests for the load generator. They run it against a small fake
// web server started inside the test, so no cluster is needed.

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// newTestGenerator makes a generator pointed at the given fake server.
func newTestGenerator(server *httptest.Server, maxRate float64) *Generator {
	return New(Config{
		TargetURL: server.URL,
		Host:      "workload.cluster.local",
		MaxRate:   maxRate,
		Timeout:   300 * time.Millisecond,
		Frames:    [][]byte{[]byte("frame-a"), []byte("frame-b")},
		Shape:     func() (string, string) { return http.MethodPost, "/frame" },
	})
}

// runFor starts load, lets it run, stops it, waits for stragglers and
// returns everything that was counted.
func runFor(t *testing.T, g *Generator, cameras int, fps float64, duration time.Duration) Second {
	t.Helper()
	if err := g.Start(cameras, fps); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	time.Sleep(duration)
	g.Stop()
	time.Sleep(400 * time.Millisecond) // longer than the timeout, so every frame has finished
	return g.TakeSecond()
}

// Frames are sent at about cameras x fps, as separate POSTs with the right
// Host header, and counted per node from the X-Node header.
func TestSendsAtTheRequestedRate(t *testing.T) {
	var wrongRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/frame" || r.Host != "workload.cluster.local" ||
			r.Header.Get("Content-Type") != "image/jpeg" {
			wrongRequests.Add(1)
		}
		w.Header().Set("X-Node", "node-a")
	}))
	defer server.Close()

	// 5 cameras x 20 fps = 100 frames a second, for one second.
	got := runFor(t, newTestGenerator(server, 1000), 5, 20, time.Second)

	if got.Sent < 80 || got.Sent > 125 {
		t.Errorf("sent %d frames in about 1 s, want about 100", got.Sent)
	}
	if got.Processed != got.Sent || got.Failed != 0 {
		t.Errorf("sent %d, processed %d, failed %d; want all processed", got.Sent, got.Processed, got.Failed)
	}
	if got.PerNode["node-a"] != got.Processed {
		t.Errorf("per-node count for node-a = %d, want %d", got.PerNode["node-a"], got.Processed)
	}
	if wrongRequests.Load() != 0 {
		t.Errorf("%d requests had the wrong method, path, Host or Content-Type", wrongRequests.Load())
	}
	if got.LatencyMeanMs <= 0 || got.LatencyP95Ms < got.LatencyMeanMs/2 {
		t.Errorf("latency numbers look wrong: mean %.2f ms, p95 %.2f ms", got.LatencyMeanMs, got.LatencyP95Ms)
	}
}

// OPEN LOOP: a slow server must not slow the sending down. Each reply here
// takes 200 ms; a generator that waited for replies could send only 5 frames
// per camera per second.
func TestDoesNotWaitForReplies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	// 2 cameras x 25 fps = 50 frames a second.
	got := runFor(t, newTestGenerator(server, 1000), 2, 25, time.Second)

	if got.Sent < 40 {
		t.Errorf("sent only %d frames in about 1 s with a slow server, want about 50", got.Sent)
	}
	if got.Processed != got.Sent {
		t.Errorf("sent %d but processed %d; slow replies should still be counted", got.Sent, got.Processed)
	}
}

// Each kind of failure lands in its own counter.
func TestCountsFailuresByKind(t *testing.T) {
	// A server that answers 503 "busy".
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer busy.Close()
	got := runFor(t, newTestGenerator(busy, 1000), 2, 10, 500*time.Millisecond)
	if got.Sent == 0 || got.FailedBusy != got.Sent || got.Failed != got.Sent || got.Processed != 0 {
		t.Errorf("503 server: sent %d, failed %d (busy %d), processed %d; want every frame failed as busy",
			got.Sent, got.Failed, got.FailedBusy, got.Processed)
	}

	// A server that answers later than the 300 ms timeout.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(600 * time.Millisecond)
	}))
	defer slow.Close()
	got = runFor(t, newTestGenerator(slow, 1000), 2, 10, 500*time.Millisecond)
	if got.Sent == 0 || got.FailedTimeout != got.Sent {
		t.Errorf("slow server: sent %d, timed out %d; want every frame timed out", got.Sent, got.FailedTimeout)
	}

	// A server that is not there at all.
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	gone.Close()
	got = runFor(t, newTestGenerator(gone, 1000), 2, 10, 500*time.Millisecond)
	if got.Sent == 0 || got.FailedOther != got.Sent {
		t.Errorf("missing server: sent %d, other failures %d; want every frame failed as other", got.Sent, got.FailedOther)
	}
}

// The total rate limit and the other limits are enforced.
func TestRefusesTooMuchLoad(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	g := newTestGenerator(server, 100) // at most 100 frames a second
	defer g.Stop()

	if err := g.Start(11, 10); err == nil {
		t.Error("11 cameras x 10 fps = 110 was accepted, above the limit of 100")
	}
	if err := g.Start(0, 10); err == nil {
		t.Error("0 cameras was accepted by Start")
	}
	if err := g.Start(1, 0); err == nil {
		t.Error("0 fps was accepted")
	}
	if err := g.Start(1, 1000); err == nil {
		t.Error("1000 fps was accepted")
	}
	if g.Status().Running {
		t.Error("generator is running after only refused requests")
	}

	if err := g.Start(10, 10); err != nil {
		t.Fatalf("10 cameras x 10 fps = 100 was refused: %v", err)
	}
	if err := g.SetCameras(11); err == nil {
		t.Error("raising to 11 cameras while running was accepted, above the limit")
	}
	if got := g.Status(); got.Cameras != 10 || got.Demand != 100 {
		t.Errorf("status after a refused change: %d cameras, demand %g; want 10 and 100", got.Cameras, got.Demand)
	}
}

// The number of cameras can be changed while load is running.
func TestChangesCamerasWhileRunning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	g := newTestGenerator(server, 1000)

	if err := g.SetCameras(3); err == nil {
		t.Error("SetCameras was accepted while load was stopped")
	}
	if err := g.Start(2, 20); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	low := g.TakeSecond() // about 2 x 20 x 0.5 = 20 frames

	if err := g.SetCameras(10); err != nil {
		t.Fatalf("SetCameras failed: %v", err)
	}
	if got := g.Status(); got.Cameras != 10 || got.FPS != 20 || got.Demand != 200 || !got.Running {
		t.Errorf("status = %+v, want 10 cameras at 20 fps running", got)
	}
	time.Sleep(500 * time.Millisecond)
	high := g.TakeSecond() // about 10 x 20 x 0.5 = 100 frames

	g.Stop()
	if g.Status().Running {
		t.Error("still running after Stop")
	}
	if high.Sent < low.Sent*3 {
		t.Errorf("sent %d frames with 2 cameras and %d with 10; want about five times as many", low.Sent, high.Sent)
	}

	// After Stop, sending really does stop.
	time.Sleep(400 * time.Millisecond)
	g.TakeSecond()
	time.Sleep(300 * time.Millisecond)
	if after := g.TakeSecond(); after.Sent != 0 {
		t.Errorf("%d frames were sent after Stop", after.Sent)
	}
}

// GET workloads (the plain web servers) get a GET with no body.
func TestFollowsTheRequestShape(t *testing.T) {
	var gets atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" && r.ContentLength == 0 {
			gets.Add(1)
		}
	}))
	defer server.Close()

	g := newTestGenerator(server, 1000)
	g.config.Shape = func() (string, string) { return http.MethodGet, "/" }
	got := runFor(t, g, 2, 10, 500*time.Millisecond)

	if got.Sent == 0 || int(gets.Load()) != got.Sent {
		t.Errorf("sent %d, of which %d were plain GET /; want all of them", got.Sent, gets.Load())
	}
	if got.PerNode["(no X-Node header)"] != got.Processed {
		t.Errorf("replies without X-Node should be counted under their own label, got %v", got.PerNode)
	}
}
