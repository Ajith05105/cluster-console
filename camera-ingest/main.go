// camera-ingest is the demo workload: a small web service that pretends to
// process frames from security cameras.
//
// It has three addresses:
//
//	POST /frame    send one JPEG picture, get a small JSON answer back
//	GET  /ready    "can I take traffic right now?"  (Kubernetes asks this)
//	GET  /healthz  "is the program alive?"          (Kubernetes asks this)
//
// It keeps nothing between requests, so any copy (pod) can handle any frame.
// That is what lets Kubernetes add, remove and replace pods freely.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"cluster-console/internal/scene"
)

// Fixed settings. Things that are useful to change per deployment are read
// from environment variables instead; see main() at the bottom.
const (
	// The biggest request body we accept. Frames are a few kilobytes, so
	// 1 MiB is generous and stops anyone from posting something huge.
	maxFrameBytes = 1 << 20

	// The biggest picture we are willing to decode (1920 x 1080 pixels).
	maxFramePixels = 1920 * 1080

	// Every frame is shrunk to this tiny size before comparing. Comparing
	// 64 x 48 = 3072 numbers is quick and ignores fine detail and JPEG noise.
	smallWidth  = 64
	smallHeight = 48

	// A shrunk pixel counts as "changed" if its brightness (0 to 255) differs
	// from the reference picture by more than this.
	pixelThreshold = 24

	// We report motion if more than this share of the pixels changed
	// (0.02 = 2%).
	motionThreshold = 0.02

	// Safety net: never spend longer than this burning CPU on one frame, even
	// if the pod is being starved of CPU.
	maxBurnWallTime = 5 * time.Second

	// When Kubernetes asks us to stop, we first say "not ready" and wait this
	// long, so traffic is steered away before we stop answering.
	drainDelay = 3 * time.Second
)

// server holds everything the request handlers need. One is created at
// start-up and shared by all requests.
type server struct {
	nodeName string        // which machine this pod runs on (from Kubernetes)
	podName  string        // this pod's own name (from Kubernetes)
	workTime time.Duration // CPU time every frame must cost (from WORK_MS)

	// reference is the empty-room picture, already shrunk. Every incoming
	// frame is compared against it.
	reference []uint8

	// slots limits how many frames are worked on at once. Think of it as a
	// fixed number of tickets: a request must take a ticket to start and
	// hands it back when done. If none are free, the request is turned away
	// immediately instead of piling up.
	slots chan struct{}

	// ready is what GET /ready reports. It flips to false during shutdown.
	ready atomic.Bool
}

// frameReply is the JSON sent back for POST /frame. The `json:"..."` tags are
// the field names as they appear in the JSON.
type frameReply struct {
	OK     bool    `json:"ok"`
	Node   string  `json:"node"`
	Pod    string  `json:"pod"`
	Motion bool    `json:"motion"`          // did something move?
	Score  float64 `json:"score"`           // share of pixels that changed, 0 to 1
	Width  int     `json:"width"`           // width of the frame we were sent
	Height int     `json:"height"`          // height of the frame we were sent
	CPUMs  float64 `json:"cpu_ms"`          // CPU time this frame really used
	Error  string  `json:"error,omitempty"` // only present when ok is false
}

// newServer builds a server from its settings.
func newServer(nodeName, podName string, workMs, maxInFlight int) *server {
	s := &server{
		nodeName:  nodeName,
		podName:   podName,
		workTime:  time.Duration(workMs) * time.Millisecond,
		reference: shrinkToGray(scene.Reference()),
		slots:     make(chan struct{}, maxInFlight),
	}
	s.ready.Store(true)
	return s
}

// routes connects each address to the function that handles it. A request
// with the wrong method (for example GET /frame) automatically gets
// "405 Method Not Allowed".
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /frame", s.handleFrame)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	return mux
}

// handleFrame processes one camera frame.
func (s *server) handleFrame(w http.ResponseWriter, r *http.Request) {
	// Step 1: take a ticket, or refuse straight away if all are in use.
	// Refusing quickly ("503 busy") is deliberate. The cameras do not wait
	// for answers, so without this an overloaded pod would build an endless
	// queue, run out of memory and crash. This way it stays up and simply
	// drops the frames it cannot handle.
	select {
	case s.slots <- struct{}{}:
		// Got a ticket. Hand it back when this function finishes.
		defer func() { <-s.slots }()
	default:
		s.reply(w, http.StatusServiceUnavailable, frameReply{Error: "busy: too many frames in progress"})
		return
	}

	// Step 2: read the request body, refusing anything over the size limit.
	r.Body = http.MaxBytesReader(w, r.Body, maxFrameBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.reply(w, http.StatusRequestEntityTooLarge, frameReply{Error: "frame is larger than 1 MiB"})
			return
		}
		s.reply(w, http.StatusBadRequest, frameReply{Error: "could not read the request body"})
		return
	}

	// Step 3: start the CPU stopwatch.
	// Go normally moves work between operating-system threads as it pleases.
	// LockOSThread pins this request to one thread until we unlock, so the
	// per-thread CPU reading below measures this request and nothing else.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cpuAtStart := cpuTimeOfThisThread()

	// Step 4: look at the picture's size first (cheap), then decode it.
	header, err := jpeg.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		s.reply(w, http.StatusBadRequest, frameReply{Error: "body is not a JPEG picture"})
		return
	}
	if header.Width*header.Height > maxFramePixels {
		s.reply(w, http.StatusRequestEntityTooLarge, frameReply{Error: "picture is larger than 1920x1080"})
		return
	}
	frame, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil {
		s.reply(w, http.StatusBadRequest, frameReply{Error: "body is not a JPEG picture"})
		return
	}

	// Step 5: shrink it and compare with the empty-room reference.
	score := motionScore(shrinkToGray(frame), s.reference)

	// Step 6: top the CPU cost up to WORK_MS. Decoding a small frame takes
	// very little CPU, so this stands in for heavier processing a real
	// system would do. It is what makes load on the cluster visible.
	burnCPU(r.Context(), cpuAtStart, s.workTime)

	// Step 7: answer.
	cpuUsed := cpuTimeOfThisThread() - cpuAtStart
	s.reply(w, http.StatusOK, frameReply{
		OK:     true,
		Motion: score > motionThreshold,
		Score:  score,
		Width:  header.Width,
		Height: header.Height,
		CPUMs:  float64(cpuUsed.Microseconds()) / 1000,
	})
}

// handleReady answers Kubernetes' "can you take traffic?" check. Only pods
// that say yes (HTTP 200) are sent frames.
func (s *server) handleReady(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	_, _ = io.WriteString(w, "ready\n")
}

// handleHealthz answers Kubernetes' "are you alive?" check. If this ever
// stopped answering, Kubernetes would restart the pod.
func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, "ok\n")
}

// reply sends a JSON answer. It always adds the node and pod names, both in
// the JSON and as X-Node / X-Pod headers. The load generator counts frames
// per node by reading the X-Node header.
func (s *server) reply(w http.ResponseWriter, status int, body frameReply) {
	body.Node = s.nodeName
	body.Pod = s.podName
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Node", s.nodeName)
	w.Header().Set("X-Pod", s.podName)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// shrinkToGray turns a picture of any size into a small black-and-white one,
// returned as a plain list of brightness numbers, row by row.
//
// Each small pixel is the average brightness of the block of original pixels
// it covers.
func shrinkToGray(img image.Image) []uint8 {
	bounds := img.Bounds()
	brightnessAt := brightnessReader(img)
	small := make([]uint8, smallWidth*smallHeight)

	for sy := 0; sy < smallHeight; sy++ {
		// Rows of the original picture covered by small row sy.
		top := bounds.Min.Y + sy*bounds.Dy()/smallHeight
		bottom := bounds.Min.Y + (sy+1)*bounds.Dy()/smallHeight
		if bottom <= top {
			bottom = top + 1 // always cover at least one row
		}
		for sx := 0; sx < smallWidth; sx++ {
			// Columns of the original picture covered by small column sx.
			left := bounds.Min.X + sx*bounds.Dx()/smallWidth
			right := bounds.Min.X + (sx+1)*bounds.Dx()/smallWidth
			if right <= left {
				right = left + 1
			}

			total, count := 0, 0
			for y := top; y < bottom; y++ {
				for x := left; x < right; x++ {
					total += brightnessAt(x, y)
					count++
				}
			}
			small[sy*smallWidth+sx] = uint8(total / count)
		}
	}
	return small
}

// brightnessReader returns a function that gives the brightness (0 to 255)
// of the pixel at x, y.
//
// JPEGs decode into one of a few in-memory layouts. For the two common ones
// we read the brightness number directly, which is fast. Anything else falls
// back to a slower general conversion.
func brightnessReader(img image.Image) func(x, y int) int {
	switch picture := img.(type) {
	case *image.Gray:
		// Black-and-white JPEG: the pixel value is the brightness.
		return func(x, y int) int { return int(picture.GrayAt(x, y).Y) }
	case *image.YCbCr:
		// Colour JPEG: stored as brightness (Y) plus two colour channels.
		// We only need the brightness channel.
		return func(x, y int) int { return int(picture.Y[picture.YOffset(x, y)]) }
	default:
		return func(x, y int) int {
			return int(color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y)
		}
	}
}

// motionScore compares two shrunk pictures and returns the share of pixels
// that differ noticeably: 0 means identical, 1 means every pixel changed.
func motionScore(frame, reference []uint8) float64 {
	changed := 0
	for i := range frame {
		difference := int(frame[i]) - int(reference[i])
		if difference < 0 {
			difference = -difference
		}
		if difference > pixelThreshold {
			changed++
		}
	}
	return float64(changed) / float64(len(frame))
}

// burnCPU keeps the processor busy until this request has used `target` of
// CPU time in total, counted from cpuAtStart.
//
// The busy work is hashing a small block of bytes over and over. The result
// is thrown away; only the effort matters.
func burnCPU(ctx context.Context, cpuAtStart, target time.Duration) {
	var block [32]byte
	giveUpAt := time.Now().Add(maxBurnWallTime)

	for cpuTimeOfThisThread()-cpuAtStart < target {
		// Do a small batch of work between checks, so we are not asking the
		// operating system for the time constantly.
		for i := 0; i < 200; i++ {
			block = sha256.Sum256(block[:])
		}
		// Stop early if the caller has gone away, or the safety limit hit.
		if ctx.Err() != nil || time.Now().After(giveUpAt) {
			return
		}
	}
}

// envInt reads a whole number from an environment variable. If the variable
// is missing or not a sensible number, it returns the fallback instead.
func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func main() {
	// Settings come from environment variables, which the Kubernetes
	// Deployment sets (see backend/catalog/catalog.json and
	// backend/catalog/templates/deployment.yaml.tmpl).
	port := envInt("PORT", 8080)
	workMs := envInt("WORK_MS", 40)          // CPU milliseconds per frame
	maxInFlight := envInt("MAX_INFLIGHT", 8) // frames worked on at once
	if maxInFlight < 1 {
		maxInFlight = 1
	}

	s := newServer(os.Getenv("NODE_NAME"), os.Getenv("POD_NAME"), workMs, maxInFlight)

	httpServer := &http.Server{
		Addr:    ":" + strconv.Itoa(port),
		Handler: s.routes(),
		// Timeouts so a stuck or very slow client cannot hold a connection
		// open forever.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Start serving in the background, so the code below can wait for a stop
	// signal at the same time.
	go func() {
		log.Printf("camera-ingest listening on :%d (node=%q pod=%q WORK_MS=%d MAX_INFLIGHT=%d)",
			port, s.nodeName, s.podName, workMs, maxInFlight)
		err := httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server stopped unexpectedly: %v", err)
		}
	}()

	// Wait here until Kubernetes asks the pod to stop (SIGTERM), or someone
	// presses Ctrl-C when running it by hand.
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	<-stop.Done()

	// Polite shutdown, so deleting a pod does not drop frames:
	//  1. report "not ready" so no new traffic is sent here,
	//  2. wait a moment for that to take effect,
	//  3. finish the frames already in progress, then exit.
	log.Print("stop requested: draining")
	s.ready.Store(false)
	time.Sleep(drainDelay)

	deadline, cancelDeadline := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelDeadline()
	if err := httpServer.Shutdown(deadline); err != nil {
		log.Printf("shutdown did not finish cleanly: %v", err)
	}
	log.Print("stopped")
}
