package main

// Automated tests for camera-ingest. They call the request handlers directly,
// in memory, so no network or cluster is needed. Run them with `make test`.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cluster-console/internal/scene"
)

// post sends a POST /frame with the given body to the server and returns the
// raw response plus the decoded JSON answer.
func post(t *testing.T, s *server, body []byte) (*httptest.ResponseRecorder, frameReply) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/frame", bytes.NewReader(body))
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)

	var reply frameReply
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
		t.Fatalf("answer was not JSON: %v (body: %q)", err, response.Body.String())
	}
	return response, reply
}

// A frame with the bright box in it must be accepted and reported as motion,
// and the answer must name the node and pod.
func TestFrameWithMotion(t *testing.T) {
	s := newServer("test-node", "test-pod", 0, 4)

	for frameNumber := 1; frameNumber < scene.FrameCount; frameNumber++ {
		response, reply := post(t, s, scene.JPEG(scene.Frame(frameNumber)))

		if response.Code != http.StatusOK {
			t.Fatalf("frame %d: got HTTP %d, want 200", frameNumber, response.Code)
		}
		if !reply.OK || !reply.Motion {
			t.Errorf("frame %d: ok=%v motion=%v score=%.3f, want ok and motion", frameNumber, reply.OK, reply.Motion, reply.Score)
		}
		if got := response.Header().Get("X-Node"); got != "test-node" {
			t.Errorf("frame %d: X-Node header = %q, want test-node", frameNumber, got)
		}
		if reply.Node != "test-node" || reply.Pod != "test-pod" {
			t.Errorf("frame %d: node=%q pod=%q in JSON, want test-node and test-pod", frameNumber, reply.Node, reply.Pod)
		}
		if reply.Width != scene.Width || reply.Height != scene.Height {
			t.Errorf("frame %d: size %dx%d, want %dx%d", frameNumber, reply.Width, reply.Height, scene.Width, scene.Height)
		}
	}
}

// The empty-room frame is the same as the reference, so: no motion.
func TestFrameWithoutMotion(t *testing.T) {
	s := newServer("test-node", "test-pod", 0, 4)

	response, reply := post(t, s, scene.JPEG(scene.Frame(0)))

	if response.Code != http.StatusOK {
		t.Fatalf("got HTTP %d, want 200", response.Code)
	}
	if reply.Motion {
		t.Errorf("empty room reported motion (score %.3f)", reply.Score)
	}
}

// Every frame must cost at least WORK_MS of CPU.
func TestFrameBurnsRequestedCPU(t *testing.T) {
	const workMs = 30
	s := newServer("test-node", "test-pod", workMs, 4)

	started := time.Now()
	response, reply := post(t, s, scene.JPEG(scene.Frame(1)))
	took := time.Since(started)

	if response.Code != http.StatusOK {
		t.Fatalf("got HTTP %d, want 200", response.Code)
	}
	if reply.CPUMs < workMs {
		t.Errorf("reported cpu_ms = %.2f, want at least %d", reply.CPUMs, workMs)
	}
	// Using 30 ms of CPU cannot take less than 30 ms on the clock.
	if took < workMs*time.Millisecond {
		t.Errorf("request took %v on the clock, want at least %d ms", took, workMs)
	}
}

// Rubbish that is not a JPEG must be refused with 400.
func TestRejectsNonJPEG(t *testing.T) {
	s := newServer("test-node", "test-pod", 0, 4)

	response, reply := post(t, s, []byte("this is not a picture"))

	if response.Code != http.StatusBadRequest {
		t.Errorf("got HTTP %d, want 400", response.Code)
	}
	if reply.OK || reply.Error == "" {
		t.Errorf("want ok=false with an error message, got ok=%v error=%q", reply.OK, reply.Error)
	}
}

// A body over the size limit must be refused with 413.
func TestRejectsOversizedBody(t *testing.T) {
	s := newServer("test-node", "test-pod", 0, 4)

	response, _ := post(t, s, make([]byte, maxFrameBytes+1))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("got HTTP %d, want 413", response.Code)
	}
}

// When every ticket is taken, a new frame must be turned away with 503 and
// still say which node answered.
func TestRefusesWhenBusy(t *testing.T) {
	s := newServer("test-node", "test-pod", 0, 1)
	s.slots <- struct{}{} // take the only ticket, as if a frame were in progress

	response, reply := post(t, s, scene.JPEG(scene.Frame(1)))

	if response.Code != http.StatusServiceUnavailable {
		t.Errorf("got HTTP %d, want 503", response.Code)
	}
	if reply.OK {
		t.Error("want ok=false when busy")
	}
	if got := response.Header().Get("X-Node"); got != "test-node" {
		t.Errorf("X-Node header = %q, want test-node", got)
	}
}

// Only POST is allowed on /frame.
func TestFrameRejectsGET(t *testing.T) {
	s := newServer("test-node", "test-pod", 0, 4)

	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/frame", nil))

	if response.Code != http.StatusMethodNotAllowed {
		t.Errorf("got HTTP %d, want 405", response.Code)
	}
}

// /ready says 200 normally and 503 once shutdown has begun. /healthz always
// says 200.
func TestReadyAndHealthz(t *testing.T) {
	s := newServer("test-node", "test-pod", 0, 4)

	get := func(path string) int {
		response := httptest.NewRecorder()
		s.routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		return response.Code
	}

	if code := get("/ready"); code != http.StatusOK {
		t.Errorf("/ready = %d, want 200", code)
	}
	if code := get("/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = %d, want 200", code)
	}

	s.ready.Store(false) // what main() does when asked to stop

	if code := get("/ready"); code != http.StatusServiceUnavailable {
		t.Errorf("/ready during shutdown = %d, want 503", code)
	}
	if code := get("/healthz"); code != http.StatusOK {
		t.Errorf("/healthz during shutdown = %d, want 200", code)
	}
}

// The ready-made frames must be small, as the brief asks for small JPEGs.
func TestFramesAreSmall(t *testing.T) {
	for i, frame := range scene.Frames() {
		if len(frame) > 20*1024 {
			t.Errorf("frame %d is %d bytes, want under 20 KiB", i, len(frame))
		}
		t.Logf("frame %d: %d bytes", i, len(frame))
	}
}
