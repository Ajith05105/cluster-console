package api

// This file holds one function per address that changes something, plus the
// two for downloading the CSV files. Each POST handler follows the same
// steps: read the JSON, do the thing, write a line in the event log, reply.
//
// None of the request bodies has a namespace field. The namespace is fixed
// inside the cluster package.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"cluster-console/backend/catalog"
)

// actionTimeout is the longest a single action may take.
const actionTimeout = 30 * time.Second

// POST /api/deploy
//
//	{"image_key": "camera-ingest", "cpu_request": "500m", "memory_request": "128Mi",
//	 "max_replicas": 20, "fast_node_death": false}
//
// Only image_key is required; the rest default to the catalog's values.
// image_key must be a catalog key. There is no way to name an image directly.
func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ImageKey      string `json:"image_key"`
		CPURequest    string `json:"cpu_request"`
		MemoryRequest string `json:"memory_request"`
		MaxReplicas   int    `json:"max_replicas"`
		FastNodeDeath bool   `json:"fast_node_death"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	if request.ImageKey == "" {
		request.ImageKey = s.catalog.Default
	}

	ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
	defer cancel()
	err := s.cluster.Deploy(ctx, request.ImageKey, catalog.Options{
		CPURequest:    request.CPURequest,
		MemoryRequest: request.MemoryRequest,
		MaxReplicas:   request.MaxReplicas,
		FastNodeDeath: request.FastNodeDeath,
	})
	if err != nil {
		failWith(w, err)
		return
	}

	// Describe what was deployed using the values that were actually used.
	item, _ := s.catalog.Find(request.ImageKey)
	cpu, memory, maximum := item.CPURequest, item.MemoryRequest, item.HPA.MaxReplicas
	if request.CPURequest != "" {
		cpu = request.CPURequest
	}
	if request.MemoryRequest != "" {
		memory = request.MemoryRequest
	}
	if request.MaxReplicas != 0 {
		maximum = request.MaxReplicas
	}
	nodeDeath := s.catalog.NodeDeathSeconds.Baseline
	if request.FastNodeDeath {
		nodeDeath = s.catalog.NodeDeathSeconds.Fast
	}
	message := fmt.Sprintf("deployed %s (CPU %s, memory %s, up to %d pods, pods on a dead node replaced after %d s)",
		item.Key, cpu, memory, maximum, nodeDeath)
	s.events.Add("action", message)
	ok(w, message)
}

// POST /api/undeploy   (no body)
func (s *Server) handleUndeploy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
	defer cancel()
	deleted, err := s.cluster.Undeploy(ctx)
	if err != nil {
		failWith(w, err)
		return
	}
	message := "removed the workload"
	if deleted == 0 {
		message = "nothing was deployed, so nothing was removed"
	}
	s.events.Add("action", message)
	ok(w, message)
}

// POST /api/scale   {"replicas": 5}
func (s *Server) handleScale(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Replicas int `json:"replicas"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
	defer cancel()
	if err := s.cluster.Scale(ctx, request.Replicas); err != nil {
		failWith(w, err)
		return
	}
	message := fmt.Sprintf("scaled to at least %d pods", request.Replicas)
	if request.Replicas == 1 {
		message = "scaled to at least 1 pod"
	}
	s.events.Add("action", message)
	ok(w, message)
}

// POST /api/pod/delete   {"name": "camera-ingest-6cdbf566b4-rfpcl"}
func (s *Server) handlePodDelete(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
	defer cancel()
	if err := s.cluster.DeletePod(ctx, request.Name); err != nil {
		failWith(w, err)
		return
	}
	message := "deleted pod " + request.Name
	s.events.Add("action", message)
	ok(w, message)
}

// POST /api/load/start   {"cameras": 20, "fps": 5}
//
// Starts the load, or changes it if it is already running.
func (s *Server) handleLoadStart(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Cameras int     `json:"cameras"`
		FPS     float64 `json:"fps"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	wasRunning := s.load.Status().Running
	if err := s.load.Start(request.Cameras, request.FPS); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	verb := "started"
	if wasRunning {
		verb = "changed"
	}
	message := fmt.Sprintf("load %s: %d cameras x %g fps = %g frames per second",
		verb, request.Cameras, request.FPS, float64(request.Cameras)*request.FPS)
	s.events.Add("load", message)
	s.ClusterChanged() // so browsers get a snapshot with the new load status
	ok(w, message)
}

// POST /api/load/cameras   {"cameras": 40}
//
// Changes the number of cameras while load is running. The cameras already
// sending are not disturbed.
func (s *Server) handleLoadCameras(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Cameras int `json:"cameras"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	if err := s.load.SetCameras(request.Cameras); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	status := s.load.Status()
	message := fmt.Sprintf("load changed: %d cameras x %g fps = %g frames per second", status.Cameras, status.FPS, status.Demand)
	s.events.Add("load", message)
	s.ClusterChanged()
	ok(w, message)
}

// POST /api/load/stop   (no body)
func (s *Server) handleLoadStop(w http.ResponseWriter, r *http.Request) {
	if !s.load.Status().Running {
		ok(w, "load was not running")
		return
	}
	s.load.Stop()
	s.events.Add("load", "load stopped")
	s.ClusterChanged()
	ok(w, "load stopped")
}

// POST /api/mark   {"label": "power cut"}
//
// Adds a named moment to the event log. Used for things the console cannot
// see for itself, such as someone pulling a power cable.
func (s *Server) handleMark(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Label string `json:"label"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	label := strings.TrimSpace(request.Label)
	if label == "" || len(label) > 80 {
		fail(w, http.StatusBadRequest, "label must be between 1 and 80 characters")
		return
	}
	for _, character := range label {
		if unicode.IsControl(character) {
			fail(w, http.StatusBadRequest, "label must be plain text on one line")
			return
		}
	}
	event := s.events.Add("mark", label)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "marked: " + label, "time": event.Time})
}

// csvFileName is what a downloadable file's name must look like. Because it
// allows no slashes, a request cannot reach outside the data folder.
var csvFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.csv$`)

// GET /api/files
//
// Lists the CSV files that can be downloaded, newest first.
func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	type file struct {
		Name     string    `json:"name"`
		Bytes    int64     `json:"bytes"`
		Modified time.Time `json:"modified"`
	}
	files := []file{}
	if s.config.DataDir != "" {
		entries, _ := os.ReadDir(s.config.DataDir)
		for _, entry := range entries {
			if entry.IsDir() || !csvFileName.MatchString(entry.Name()) {
				continue
			}
			if info, err := entry.Info(); err == nil {
				files = append(files, file{entry.Name(), info.Size(), info.ModTime().UTC()})
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name > files[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// GET /api/files/{name}
//
// Downloads one CSV file.
func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.config.DataDir == "" || !csvFileName.MatchString(name) {
		fail(w, http.StatusNotFound, "no such file")
		return
	}
	path := filepath.Join(s.config.DataDir, name)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		fail(w, http.StatusNotFound, "no such file")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, path)
}
