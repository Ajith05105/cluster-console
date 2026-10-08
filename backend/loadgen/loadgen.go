// Package loadgen is the load generator: it pretends to be a number of
// security cameras, each sending frames to the workload at a steady rate.
//
// The important property is that it is OPEN LOOP. A real camera does not wait
// for the server before taking its next picture, and neither does this: each
// virtual camera sends a frame every 1/fps seconds whether or not the earlier
// frames have been answered. So when the cluster cannot keep up, the demand
// stays the same and the shortfall shows up as failed frames, instead of the
// generator quietly slowing down and hiding the problem.
//
// Every frame is its own separate HTTP request, sent through Traefik like any
// outside client's would be.
package loadgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Limits on what can be asked for, besides the total rate limit in Config.
const (
	MaxCameras = 1000
	MinFPS     = 0.1
	MaxFPS     = 30
)

// Config is the load generator's fixed settings.
type Config struct {
	// TargetURL is where requests are sent: Traefik's address inside the
	// cluster, for example http://traefik.kube-system.svc.cluster.local
	TargetURL string
	// Host is the host name put on every request so Traefik knows which
	// route it is for, for example workload.cluster.local. (That name does
	// not resolve inside the cluster, which is why the address and the host
	// name are given separately.)
	Host string
	// MaxRate is the highest total demand allowed, in frames per second.
	// A request for more cameras x fps than this is refused.
	MaxRate float64
	// Timeout is how long to wait for an answer before counting the frame
	// as failed.
	Timeout time.Duration
	// Frames are the ready-made JPEG pictures, sent in rotation.
	Frames [][]byte
	// Shape says what kind of request to send right now: POST /frame for
	// camera-ingest, GET / for the plain web servers. It is asked for every
	// frame, so it follows whatever is currently deployed.
	Shape func() (method, path string)
}

// Status is what the generator is doing right now.
type Status struct {
	Running bool    `json:"running"`
	Cameras int     `json:"cameras"`
	FPS     float64 `json:"fps"`
	Demand  float64 `json:"demand"`   // cameras x fps
	MaxRate float64 `json:"max_rate"` // the limit on demand
}

// Second is what happened during one second. See record.StatsRow for the
// meaning of each number; the fields here are the load generator's share.
type Second struct {
	Cameras       int
	FPS           float64
	Demand        float64
	Sent          int
	Processed     int
	Failed        int
	FailedBusy    int
	FailedTimeout int
	FailedOther   int
	LatencyMeanMs float64
	LatencyP95Ms  float64
	PerNode       map[string]int
}

// Generator is the load generator. Make one with New.
type Generator struct {
	config Config
	client *http.Client

	// mu guards the three fields below: whether we are running, how fast,
	// and the list of running cameras.
	mu      sync.Mutex
	running bool
	fps     float64
	// cameras holds one "stop" function per running camera. Calling it
	// stops that camera. The length of this list is the camera count.
	cameras []context.CancelFunc

	// countsMu guards the counters for the second in progress.
	countsMu  sync.Mutex
	counts    Second
	latencies []float64 // in milliseconds, one per processed frame

	// nextFrame counts frames sent, used to pick the next picture in turn.
	nextFrame atomic.Uint64
}

// New makes a load generator. It does nothing until Start is called.
func New(config Config) *Generator {
	return &Generator{
		config: config,
		client: &http.Client{
			// The Transport is the part that manages network connections.
			// Connections to Traefik are kept open and reused between
			// frames (each frame is still its own request). The pool is
			// large so that many frames can be in flight at once.
			Transport: &http.Transport{
				MaxIdleConns:        512,
				MaxIdleConnsPerHost: 512,
				IdleConnTimeout:     30 * time.Second,
				DialContext:         (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			},
		},
		counts: Second{PerNode: map[string]int{}},
	}
}

// Start begins sending, or changes what is being sent if already running.
func (g *Generator) Start(cameras int, fps float64) error {
	if cameras < 1 {
		return fmt.Errorf("cameras must be at least 1")
	}
	if err := g.check(cameras, fps); err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	// A different fps means every camera needs a new rhythm, so stop them
	// all and start again. The same fps just adjusts the number of cameras.
	if g.running && g.fps != fps {
		g.setCameraCount(0)
	}
	g.running = true
	g.fps = fps
	g.setCameraCount(cameras)
	return nil
}

// SetCameras changes the number of cameras while load is running, without
// disturbing the cameras that are already sending.
func (g *Generator) SetCameras(cameras int) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.running {
		return fmt.Errorf("load is not running; start it first")
	}
	if cameras < 0 {
		return fmt.Errorf("cameras cannot be negative")
	}
	if err := g.check(cameras, g.fps); err != nil {
		return err
	}
	g.setCameraCount(cameras)
	return nil
}

// Stop stops all cameras. Frames already sent are still waited for and
// counted.
func (g *Generator) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.setCameraCount(0)
	g.running = false
}

// Status reports what the generator is doing right now.
func (g *Generator) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.statusLocked()
}

// TakeSecond returns the counts gathered since it was last called and starts
// a fresh count. The console calls it once a second.
func (g *Generator) TakeSecond() Second {
	status := g.Status()

	g.countsMu.Lock()
	second := g.counts
	latencies := g.latencies
	g.counts = Second{PerNode: map[string]int{}}
	g.latencies = nil
	g.countsMu.Unlock()

	second.Cameras = status.Cameras
	second.FPS = status.FPS
	second.Demand = status.Demand
	second.LatencyMeanMs, second.LatencyP95Ms = summarise(latencies)
	return second
}

// check refuses numbers outside the allowed limits, including the total rate
// limit.
func (g *Generator) check(cameras int, fps float64) error {
	if cameras > MaxCameras {
		return fmt.Errorf("at most %d cameras are allowed", MaxCameras)
	}
	if fps < MinFPS || fps > MaxFPS {
		return fmt.Errorf("fps must be between %g and %g", float64(MinFPS), float64(MaxFPS))
	}
	if demand := float64(cameras) * fps; demand > g.config.MaxRate {
		return fmt.Errorf("%d cameras x %g fps = %g frames per second, above the limit of %g",
			cameras, fps, demand, g.config.MaxRate)
	}
	return nil
}

// statusLocked builds the Status. The caller must hold g.mu.
func (g *Generator) statusLocked() Status {
	status := Status{Running: g.running, MaxRate: g.config.MaxRate}
	if g.running {
		status.Cameras = len(g.cameras)
		status.FPS = g.fps
		status.Demand = float64(len(g.cameras)) * g.fps
	}
	return status
}

// setCameraCount starts or stops cameras until exactly `want` are running.
// The caller must hold g.mu.
func (g *Generator) setCameraCount(want int) {
	// Too many: stop the most recently started ones.
	for len(g.cameras) > want {
		last := len(g.cameras) - 1
		g.cameras[last]() // call that camera's stop function
		g.cameras = g.cameras[:last]
	}
	// Too few: start more.
	for len(g.cameras) < want {
		// The gap between one camera's frames, for example 100 ms at 10 fps.
		period := time.Duration(float64(time.Second) / g.fps)
		ctx, stop := context.WithCancel(context.Background())
		g.cameras = append(g.cameras, stop)
		// "go" starts the camera running on its own, alongside everything
		// else. Each camera is one of these lightweight background tasks.
		go g.runCamera(ctx, period)
	}
}

// runCamera is one virtual camera: it sends a frame every `period` until it
// is told to stop.
func (g *Generator) runCamera(ctx context.Context, period time.Duration) {
	// Real cameras are not synchronised with each other. Start each one at
	// a random point within the period, so the frames are spread out evenly
	// instead of all arriving in the same instant.
	startDelay := time.Duration(rand.Int64N(int64(period)))
	select {
	case <-ctx.Done():
		return
	case <-time.After(startDelay):
	}

	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		// OPEN LOOP: start the send as its own background task and go
		// straight back to waiting for the next tick. Nothing here waits
		// for the reply.
		go g.sendFrame()

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// sendFrame sends one frame as one HTTP request and records how it went.
func (g *Generator) sendFrame() {
	method, path := g.config.Shape()

	// Give up on this frame after the timeout.
	ctx, cancel := context.WithTimeout(context.Background(), g.config.Timeout)
	defer cancel()

	var body io.Reader
	if method == http.MethodPost {
		// Pick the next picture in rotation.
		picture := g.config.Frames[g.nextFrame.Add(1)%uint64(len(g.config.Frames))]
		body = bytes.NewReader(picture)
	}
	request, err := http.NewRequestWithContext(ctx, method, g.config.TargetURL+path, body)
	if err != nil {
		g.record(func(c *Second) { c.Sent++; c.Failed++; c.FailedOther++ })
		return
	}
	request.Host = g.config.Host
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "image/jpeg")
	}

	g.record(func(c *Second) { c.Sent++ })
	started := time.Now()

	response, err := g.client.Do(request)
	if err != nil {
		if isTimeout(err) {
			g.record(func(c *Second) { c.Failed++; c.FailedTimeout++ })
		} else {
			g.record(func(c *Second) { c.Failed++; c.FailedOther++ })
		}
		return
	}
	// Read (and throw away) the reply so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
	took := time.Since(started)

	switch {
	case response.StatusCode == http.StatusOK:
		node := response.Header.Get("X-Node")
		if node == "" {
			node = "(no X-Node header)"
		}
		g.countsMu.Lock()
		g.counts.Processed++
		g.counts.PerNode[node]++
		g.latencies = append(g.latencies, float64(took.Microseconds())/1000)
		g.countsMu.Unlock()
	case response.StatusCode == http.StatusServiceUnavailable:
		// camera-ingest answers 503 when a pod is already working on as
		// many frames as it can. Traefik also answers 503 when no pod is
		// ready at all.
		g.record(func(c *Second) { c.Failed++; c.FailedBusy++ })
	default:
		g.record(func(c *Second) { c.Failed++; c.FailedOther++ })
	}
}

// record applies a small change to the current second's counters, safely
// even when many frames finish at the same moment.
func (g *Generator) record(change func(*Second)) {
	g.countsMu.Lock()
	change(&g.counts)
	g.countsMu.Unlock()
}

// isTimeout says whether a request failed because it ran out of time, as
// opposed to, say, the connection being refused.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// summarise returns the mean of a list of latencies and the value that 95%
// of them are below. Both are zero for an empty list.
func summarise(latencies []float64) (mean, p95 float64) {
	if len(latencies) == 0 {
		return 0, 0
	}
	sort.Float64s(latencies)
	total := 0.0
	for _, value := range latencies {
		total += value
	}
	// Position of the 95% mark in the sorted list.
	index := int(float64(len(latencies))*0.95+0.5) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(latencies) {
		index = len(latencies) - 1
	}
	return total / float64(len(latencies)), latencies[index]
}
