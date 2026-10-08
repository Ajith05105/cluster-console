package api

// Automated tests for the web API, run against a fake Kubernetes and a fake
// workload. No real cluster is involved.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"cluster-console/backend/catalog"
	"cluster-console/backend/cluster/clustertest"
	"cluster-console/backend/loadgen"
	"cluster-console/backend/record"
)

// testToken is the access token used throughout the tests.
const testToken = "test-token-not-a-real-secret"

// testConsole is a whole console assembled for a test.
type testConsole struct {
	server  *Server
	handler http.Handler
	client  *fake.Clientset // the fake Kubernetes, with its list of calls
	events  *record.EventLog
}

// newTestConsole assembles a console around a fake Kubernetes holding the
// given objects. The "workload" the load generator sends to is a tiny fake
// web server that answers every frame as node-a.
func newTestConsole(t *testing.T, config Config, objects ...runtime.Object) *testConsole {
	t.Helper()
	cl, client := clustertest.New(t, objects...)

	workload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Node", "node-a")
	}))
	t.Cleanup(workload.Close)

	cat, _ := catalog.Load()
	load := loadgen.New(loadgen.Config{
		TargetURL: workload.URL,
		Host:      catalog.RouteHost,
		MaxRate:   100,
		Timeout:   time.Second,
		Frames:    [][]byte{[]byte("frame")},
		Shape:     cl.LoadShape,
	})
	t.Cleanup(load.Stop)

	events, _ := record.NewEventLog("")
	stats, _ := record.NewStatsWriter("")
	server := New(config, cat, cl, load, events, stats)
	cl.OnChange = server.ClusterChanged
	cl.Emit = func(kind, message string) { events.Add(kind, message) }
	return &testConsole{server: server, handler: server.Handler(), client: client, events: events}
}

// reply is a decoded JSON answer from the API.
type reply struct {
	Status  int
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Error   string `json:"error"`
}

// post sends a POST with the given token ("" for none) and JSON body.
func (c *testConsole) post(t *testing.T, path, token, body string) reply {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	c.handler.ServeHTTP(response, request)

	got := reply{Status: response.Code}
	_ = json.Unmarshal(response.Body.Bytes(), &got)
	return got
}

// get sends a GET with no token and returns the status and body.
func (c *testConsole) get(path string) (int, []byte) {
	response := httptest.NewRecorder()
	c.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	return response.Code, response.Body.Bytes()
}

// hasEvent reports whether the event log has a line of this kind containing
// this text.
func (c *testConsole) hasEvent(kind, text string) bool {
	for _, event := range c.events.Recent() {
		if event.Kind == kind && strings.Contains(event.Message, text) {
			return true
		}
	}
	return false
}

// Without the token, or with a wrong one, every POST is refused and nothing
// in the cluster is changed.
func TestEveryPostNeedsTheToken(t *testing.T) {
	c := newTestConsole(t, Config{Token: testToken})

	for path := range c.server.postRoutes() {
		for _, token := range []string{"", "wrong-token", testToken + "x", testToken[:len(testToken)-1]} {
			if got := c.post(t, path, token, `{}`); got.Status != http.StatusUnauthorized || got.OK {
				t.Errorf("POST %s with token %q: status %d, want 401", path, token, got.Status)
			}
		}
	}
	if count := clustertest.Writes(c.client); count != 0 {
		t.Errorf("%d changes reached the cluster from requests without a valid token", count)
	}
	if c.server.load.Status().Running {
		t.Error("load was started by a request without a valid token")
	}
	if len(c.events.Recent()) != 0 {
		t.Errorf("the event log has entries from refused requests: %+v", c.events.Recent())
	}

	// With the right token the same address is let through.
	if got := c.post(t, "/api/auth/check", testToken, ""); got.Status != http.StatusOK || !got.OK {
		t.Errorf("POST /api/auth/check with the right token: status %d, want 200", got.Status)
	}
}

// In read-only mode every POST is refused even with the right token.
func TestReadOnlyModeRefusesEveryPost(t *testing.T) {
	c := newTestConsole(t, Config{Token: testToken, ReadOnly: true})

	for path := range c.server.postRoutes() {
		if got := c.post(t, path, testToken, `{}`); got.Status != http.StatusForbidden {
			t.Errorf("POST %s in read-only mode: status %d, want 403", path, got.Status)
		}
	}
	if count := clustertest.Writes(c.client); count != 0 {
		t.Errorf("%d changes reached the cluster in read-only mode", count)
	}

	// Reading still works, and the page is told changes are off.
	status, body := c.get("/api/state")
	var snapshot Snapshot
	if err := json.Unmarshal(body, &snapshot); status != http.StatusOK || err != nil || !snapshot.ReadOnly {
		t.Errorf("GET /api/state in read-only mode: status %d, read_only %v, error %v", status, snapshot.ReadOnly, err)
	}
}

// If no token has been configured at all, every POST is refused. An empty
// token must never mean "no token needed".
func TestNoTokenConfiguredRefusesEveryPost(t *testing.T) {
	c := newTestConsole(t, Config{Token: ""})

	for path := range c.server.postRoutes() {
		for _, token := range []string{"", "anything"} {
			if got := c.post(t, path, token, `{}`); got.Status != http.StatusForbidden {
				t.Errorf("POST %s with no token configured: status %d, want 403", path, got.Status)
			}
		}
	}
	if count := clustertest.Writes(c.client); count != 0 {
		t.Errorf("%d changes reached the cluster with no token configured", count)
	}
}

// The reading addresses need no token.
func TestReadingNeedsNoToken(t *testing.T) {
	c := newTestConsole(t, Config{Token: testToken})

	status, body := c.get("/api/state")
	var snapshot Snapshot
	if err := json.Unmarshal(body, &snapshot); status != http.StatusOK || err != nil {
		t.Fatalf("GET /api/state: status %d, error %v", status, err)
	}
	if snapshot.ReadOnly || snapshot.Cluster.Nodes == nil || snapshot.Warnings == nil {
		t.Errorf("snapshot = %+v; want read_only false and empty lists (not null) for nodes and warnings", snapshot)
	}

	status, body = c.get("/api/catalog")
	var hello struct {
		Catalog catalog.Catalog    `json:"catalog"`
		Limits  map[string]float64 `json:"limits"`
	}
	if err := json.Unmarshal(body, &hello); status != http.StatusOK || err != nil {
		t.Fatalf("GET /api/catalog: status %d, error %v", status, err)
	}
	if hello.Catalog.Default != "camera-ingest" || len(hello.Catalog.Items) != 5 || hello.Limits["max_rate"] != 100 {
		t.Errorf("catalog reply: default %q, %d items, max_rate %v", hello.Catalog.Default, len(hello.Catalog.Items), hello.Limits["max_rate"])
	}

	if status, _ := c.get("/healthz"); status != http.StatusOK {
		t.Errorf("GET /healthz: status %d", status)
	}
}

// The normal sequence of buttons works end to end, and each press leaves a
// line in the event log.
func TestDeployScaleDeleteRemove(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "camera-ingest-abc", Namespace: "workload"}}
	c := newTestConsole(t, Config{Token: testToken}, pod)

	got := c.post(t, "/api/deploy", testToken, `{"image_key":"camera-ingest","cpu_request":"250m","max_replicas":10,"fast_node_death":true}`)
	if got.Status != http.StatusOK {
		t.Fatalf("deploy: status %d, error %q", got.Status, got.Error)
	}
	if !strings.Contains(got.Message, "CPU 250m") || !strings.Contains(got.Message, "up to 10 pods") || !strings.Contains(got.Message, "after 20 s") {
		t.Errorf("deploy message %q does not describe what was deployed", got.Message)
	}
	clustertest.Eventually(t, "state to show the workload", func() bool {
		state := c.server.snapshot().Cluster
		return state.Workload != nil && state.HPA != nil
	})
	if workload := c.server.snapshot().Cluster.Workload; workload.CPURequest != "250m" || !workload.FastNodeDeath {
		t.Errorf("state after deploy: %+v", *workload)
	}

	if got := c.post(t, "/api/scale", testToken, `{"replicas":4}`); got.Status != http.StatusOK {
		t.Fatalf("scale: status %d, error %q", got.Status, got.Error)
	}
	if got := c.post(t, "/api/scale", testToken, `{"replicas":11}`); got.Status != http.StatusBadRequest {
		t.Errorf("scale above the maximum: status %d, want 400", got.Status)
	}
	if got := c.post(t, "/api/pod/delete", testToken, `{"name":"camera-ingest-abc"}`); got.Status != http.StatusOK {
		t.Errorf("pod delete: status %d, error %q", got.Status, got.Error)
	}
	if got := c.post(t, "/api/pod/delete", testToken, `{"name":"no-such-pod"}`); got.Status != http.StatusNotFound {
		t.Errorf("deleting a pod that does not exist: status %d, want 404", got.Status)
	}
	if got := c.post(t, "/api/undeploy", testToken, ""); got.Status != http.StatusOK {
		t.Errorf("undeploy: status %d, error %q", got.Status, got.Error)
	}

	for _, want := range []string{"deployed camera-ingest", "scaled to at least 4 pods", "deleted pod camera-ingest-abc", "removed the workload"} {
		if !c.hasEvent("action", want) {
			t.Errorf("the event log has no action line containing %q", want)
		}
	}
	if outside := clustertest.WritesOutside(c.client, "workload"); len(outside) > 0 {
		t.Errorf("changes outside workload: %v", outside)
	}
}

// Marks go into the event log with their own kind; odd labels are refused.
func TestMark(t *testing.T) {
	c := newTestConsole(t, Config{Token: testToken})

	if got := c.post(t, "/api/mark", testToken, `{"label":"  power cut  "}`); got.Status != http.StatusOK {
		t.Fatalf("mark: status %d, error %q", got.Status, got.Error)
	}
	if !c.hasEvent("mark", "power cut") {
		t.Error("the mark is not in the event log")
	}
	bad := []string{`{"label":""}`, `{"label":"   "}`, `{"label":"line one\nline two"}`, `{"label":"` + strings.Repeat("x", 81) + `"}`, `{"text":"wrong field"}`, `not json`}
	for _, body := range bad {
		if got := c.post(t, "/api/mark", testToken, body); got.Status != http.StatusBadRequest {
			t.Errorf("mark with body %.30q: status %d, want 400", body, got.Status)
		}
	}
}

// Load can be started, changed while running, and stopped; too much is
// refused.
func TestLoadEndpoints(t *testing.T) {
	c := newTestConsole(t, Config{Token: testToken})

	if got := c.post(t, "/api/load/cameras", testToken, `{"cameras":5}`); got.Status != http.StatusBadRequest {
		t.Errorf("changing cameras while stopped: status %d, want 400", got.Status)
	}
	// The limit in these tests is 100 frames a second.
	if got := c.post(t, "/api/load/start", testToken, `{"cameras":30,"fps":5}`); got.Status != http.StatusBadRequest {
		t.Errorf("150 frames a second: status %d, want 400", got.Status)
	}
	if got := c.post(t, "/api/load/start", testToken, `{"cameras":4,"fps":5}`); got.Status != http.StatusOK {
		t.Fatalf("start: status %d, error %q", got.Status, got.Error)
	}
	if got := c.post(t, "/api/load/cameras", testToken, `{"cameras":10}`); got.Status != http.StatusOK {
		t.Errorf("change cameras: status %d, error %q", got.Status, got.Error)
	}
	if status := c.server.snapshot().Load; !status.Running || status.Cameras != 10 || status.FPS != 5 || status.Demand != 50 {
		t.Errorf("load status = %+v; want running, 10 cameras, 5 fps, demand 50", status)
	}
	if got := c.post(t, "/api/load/cameras", testToken, `{"cameras":21}`); got.Status != http.StatusBadRequest {
		t.Errorf("105 frames a second: status %d, want 400", got.Status)
	}

	// Let it send for a moment, then take the second's numbers as the
	// once-a-second loop would.
	time.Sleep(600 * time.Millisecond)
	c.server.tick(time.Now())
	rows := c.server.stats.Recent()
	if len(rows) != 1 || rows[0].Sent == 0 || rows[0].Cameras != 10 || rows[0].Demand != 50 || rows[0].PerNode["node-a"] == 0 {
		t.Errorf("stats row = %+v; want frames sent, 10 cameras, demand 50, replies counted for node-a", rows)
	}

	if got := c.post(t, "/api/load/stop", testToken, ""); got.Status != http.StatusOK {
		t.Errorf("stop: status %d", got.Status)
	}
	if c.server.snapshot().Load.Running {
		t.Error("load still running after stop")
	}
	for _, want := range []string{"load started: 4 cameras x 5 fps = 20 frames per second", "load changed: 10 cameras", "load stopped"} {
		if !c.hasEvent("load", want) {
			t.Errorf("the event log has no load line containing %q", want)
		}
	}
}

// The once-a-second job raises a warning for a pod stuck Pending, writes it
// to the event log, and clears it when the pod is gone.
func TestTickRaisesAndClearsWarnings(t *testing.T) {
	stuck := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "camera-ingest-stuck", Namespace: "workload"},
		Status: corev1.PodStatus{Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
				Message:            "0/6 nodes are available: 2 Insufficient cpu.",
				LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Minute))}}},
	}
	c := newTestConsole(t, Config{Token: testToken}, stuck)
	clustertest.Eventually(t, "watcher to see the stuck pod", func() bool { return len(c.server.snapshot().Cluster.Pending) == 1 })

	c.server.tick(time.Now())
	current := c.server.currentWarnings()
	if len(current) != 1 || current[0].Kind != "pending" || !strings.Contains(current[0].Reason, "Insufficient cpu") {
		t.Fatalf("warnings = %+v; want one pending warning giving the scheduler's reason", current)
	}
	if !c.hasEvent("warning", "WARNING: 1 pod Pending") {
		t.Errorf("the event log has no line for the new warning: %+v", c.events.Recent())
	}
	if row := c.server.stats.Recent()[0]; row.PendingPods != 1 {
		t.Errorf("stats row pending_pods = %d, want 1", row.PendingPods)
	}

	_ = c.client.CoreV1().Pods("workload").Delete(t.Context(), "camera-ingest-stuck", metav1.DeleteOptions{})
	clustertest.Eventually(t, "watcher to see the pod go", func() bool { return len(c.server.snapshot().Cluster.Pending) == 0 })
	c.server.tick(time.Now())
	if len(c.server.currentWarnings()) != 0 || !c.hasEvent("warning", "cleared: 1 pod Pending") {
		t.Errorf("warning was not cleared: %+v / %+v", c.server.currentWarnings(), c.events.Recent())
	}
}

// CSV files in the data folder can be listed and downloaded, and nothing
// else can.
func TestFileDownload(t *testing.T) {
	folder := t.TempDir()
	_ = os.WriteFile(filepath.Join(folder, "stats-20261008T050000Z.csv"), []byte("time_utc,sent\n"), 0o644)
	_ = os.WriteFile(filepath.Join(folder, "notes.txt"), []byte("not a csv"), 0o644)
	_ = os.WriteFile(filepath.Join(filepath.Dir(folder), "outside.csv"), []byte("outside the data folder"), 0o644)
	c := newTestConsole(t, Config{Token: testToken, DataDir: folder})

	status, body := c.get("/api/files")
	if status != http.StatusOK || !bytes.Contains(body, []byte("stats-20261008T050000Z.csv")) || bytes.Contains(body, []byte("notes.txt")) {
		t.Errorf("file list: status %d, body %s; want the CSV listed and the .txt not", status, body)
	}
	if status, body := c.get("/api/files/stats-20261008T050000Z.csv"); status != http.StatusOK || string(body) != "time_utc,sent\n" {
		t.Errorf("download: status %d, body %q", status, body)
	}
	for _, name := range []string{"notes.txt", "missing.csv", "..%2Foutside.csv", "%2e%2e%2foutside.csv", "..csv"} {
		if status, body := c.get("/api/files/" + name); status == http.StatusOK {
			t.Errorf("GET /api/files/%s returned 200 with body %q; want it refused", name, body)
		}
	}
}

// The live stream sends the catch-up messages on connecting, then new events
// as they happen.
func TestStream(t *testing.T) {
	c := newTestConsole(t, Config{Token: testToken})
	c.events.Add("mark", "before connecting")

	web := httptest.NewServer(c.handler)
	defer web.Close()
	response, err := http.Get(web.URL + "/api/stream")
	if err != nil {
		t.Fatalf("cannot open the stream: %v", err)
	}
	defer response.Body.Close()
	if contentType := response.Header.Get("Content-Type"); contentType != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", contentType)
	}

	// Read the stream in the background, handing each message's name and
	// data to the test through a channel.
	type streamMessage struct{ name, data string }
	messages := make(chan streamMessage, 100)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		name := ""
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				name = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				messages <- streamMessage{name, strings.TrimPrefix(line, "data: ")}
			}
		}
	}()
	next := func() streamMessage {
		t.Helper()
		select {
		case message := <-messages:
			return message
		case <-time.After(3 * time.Second):
			t.Fatal("no message arrived on the stream within 3 s")
			return streamMessage{}
		}
	}

	for _, want := range []string{"hello", "history", "state", "warnings"} {
		message := next()
		if message.name != want || !json.Valid([]byte(message.data)) {
			t.Fatalf("got message %q (valid JSON: %v), want %q", message.name, json.Valid([]byte(message.data)), want)
		}
		if want == "history" && !strings.Contains(message.data, "before connecting") {
			t.Errorf("history does not include the event from before connecting: %s", message.data)
		}
	}

	// Something happens: it arrives as an "event" message.
	c.post(t, "/api/mark", testToken, `{"label":"power restored"}`)
	if message := next(); message.name != "event" || !strings.Contains(message.data, "power restored") {
		t.Errorf("after a mark, got %q %s; want an event message with the label", message.name, message.data)
	}

	// The once-a-second job sends a "stats" message.
	c.server.tick(time.Now())
	if message := next(); message.name != "stats" || !strings.Contains(message.data, `"sent"`) {
		t.Errorf("after a tick, got %q %s; want a stats message", message.name, message.data)
	}
}
