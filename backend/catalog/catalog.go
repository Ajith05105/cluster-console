// Package catalog is the fixed list of workloads the console is allowed to
// deploy, plus the code that turns a catalog entry into Kubernetes objects.
//
// The list lives in catalog.json. The console never deploys an image that is
// not in that file: the Deploy button sends a catalog KEY (for example
// "camera-ingest"), never an image address.
package catalog

import (
	"bytes"
	_ "embed" // needed for the go:embed line below
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// catalogJSON holds the contents of catalog.json. The go:embed line tells the
// compiler to copy that file into the program when it is built, so the
// finished program carries the catalog inside it and reads nothing from disk.
//
//go:embed catalog.json
var catalogJSON []byte

// Catalog is the whole of catalog.json. The `json:"..."` tags are the field
// names as they appear in the file.
type Catalog struct {
	// Default is the key of the entry the console selects at start-up.
	Default string `json:"default"`

	// NodeDeathSeconds is how long Kubernetes waits before replacing pods
	// that were on a node which has died. See NodeDeathSeconds below.
	NodeDeathSeconds NodeDeathSeconds `json:"node_death_seconds"`

	// ExcludedNodes are worker nodes that must never run the workload. They
	// are left out of the test pool. Control-plane nodes are always left out
	// and do not need listing here.
	ExcludedNodes []string `json:"excluded_nodes"`

	Items []Item `json:"items"`
}

// NodeDeathSeconds holds the two settings the console can switch between.
type NodeDeathSeconds struct {
	// Baseline is plain Kubernetes behaviour (300 seconds). It is the default.
	Baseline int `json:"baseline"`
	// Fast is the quick setting for the demo (20 seconds).
	Fast int `json:"fast"`
}

// Item is one deployable workload.
type Item struct {
	Key  string `json:"key"`  // short id, also used as the Kubernetes object name
	Name string `json:"name"` // label shown in the dropdown

	// Image is the address the cluster pulls from (always the Gitea registry).
	Image string `json:"image"`
	// Upstream is where the image was copied from, or null if we built it.
	// Only scripts/mirror-images.sh uses it.
	Upstream *string `json:"upstream"`

	Port         int    `json:"port"`          // port the program listens on
	HealthPath   string `json:"health_path"`   // "can you take traffic?" check
	LivenessPath string `json:"liveness_path"` // "are you alive?" check

	// Load says what kind of request the load generator should send.
	Load LoadShape `json:"load"`

	// Env is extra environment variables to set in the pod.
	Env map[string]string `json:"env"`

	CPURequest    string  `json:"cpu_request"`    // default, editable in the console
	CPULimit      *string `json:"cpu_limit"`      // null means no CPU limit
	MemoryRequest string  `json:"memory_request"` // default, editable in the console
	MemoryLimit   string  `json:"memory_limit"`

	// LockedDown turns on the strict security settings (not root, read-only
	// disk). True for our own camera-ingest. The stock web servers need to
	// start as root and write temporary files, so it is false for them.
	LockedDown bool `json:"locked_down"`

	HPA Autoscaling `json:"hpa"`
}

// LoadShape is the request the load generator sends to this workload.
type LoadShape struct {
	Method string `json:"method"` // "POST" for camera frames, "GET" otherwise
	Path   string `json:"path"`
}

// Autoscaling is the autoscaler's settings for this workload.
type Autoscaling struct {
	TargetCPUPercent int `json:"target_cpu_percent"`
	MinReplicas      int `json:"min_replicas"`
	MaxReplicas      int `json:"max_replicas"`
}

// keyPattern is what a catalog key may look like: lower-case letters, digits
// and dashes. Kubernetes requires this of object names.
var keyPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// nodeNamePattern is what a node name may look like: as a key, but dots are
// allowed too.
var nodeNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// Load reads the built-in catalog and checks it for mistakes. A mistake in
// catalog.json is reported here, at start-up, rather than later when someone
// presses Deploy.
func Load() (*Catalog, error) {
	var c Catalog

	// DisallowUnknownFields makes a misspelt field name in catalog.json an
	// error instead of being silently ignored.
	decoder := json.NewDecoder(bytes.NewReader(catalogJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return nil, fmt.Errorf("catalog.json is not valid: %w", err)
	}

	if c.NodeDeathSeconds.Baseline <= 0 || c.NodeDeathSeconds.Fast <= 0 {
		return nil, fmt.Errorf("catalog.json: node_death_seconds baseline and fast must both be above 0")
	}

	for _, node := range c.ExcludedNodes {
		if !nodeNamePattern.MatchString(node) {
			return nil, fmt.Errorf("catalog.json: excluded_nodes: %q is not a valid node name", node)
		}
	}

	seen := map[string]bool{}
	for _, item := range c.Items {
		if err := item.check(); err != nil {
			return nil, fmt.Errorf("catalog.json: entry %q: %w", item.Key, err)
		}
		if seen[item.Key] {
			return nil, fmt.Errorf("catalog.json: key %q is used twice", item.Key)
		}
		seen[item.Key] = true
	}
	if !seen[c.Default] {
		return nil, fmt.Errorf("catalog.json: default %q is not one of the entries", c.Default)
	}
	return &c, nil
}

// Find returns the entry with the given key. The second result is false if
// there is no such entry.
func (c *Catalog) Find(key string) (Item, bool) {
	for _, item := range c.Items {
		if item.Key == key {
			return item, true
		}
	}
	return Item{}, false
}

// InTestPool says whether workload pods are allowed on a node: it must be a
// worker (not control-plane) and not on the excluded list. This mirrors the
// placement rules in templates/deployment.yaml.tmpl.
func (c *Catalog) InTestPool(nodeName string, isControlPlane bool) bool {
	if isControlPlane {
		return false
	}
	for _, excluded := range c.ExcludedNodes {
		if excluded == nodeName {
			return false
		}
	}
	return true
}

// check reports the first thing wrong with one catalog entry, or nil if it
// is fine.
func (item Item) check() error {
	if !keyPattern.MatchString(item.Key) {
		return fmt.Errorf("key must be lower-case letters, digits and dashes")
	}
	if item.Image == "" {
		return fmt.Errorf("image is missing")
	}
	if item.Port < 1 || item.Port > 65535 {
		return fmt.Errorf("port %d is not a valid port", item.Port)
	}
	for _, path := range []string{item.HealthPath, item.LivenessPath, item.Load.Path} {
		if !strings.HasPrefix(path, "/") {
			return fmt.Errorf("path %q must start with /", path)
		}
	}
	if _, err := parseCPU(item.CPURequest); err != nil {
		return fmt.Errorf("cpu_request: %w", err)
	}
	if item.CPULimit != nil {
		if _, err := parseCPU(*item.CPULimit); err != nil {
			return fmt.Errorf("cpu_limit: %w", err)
		}
	}
	if _, err := parseMemory(item.MemoryRequest); err != nil {
		return fmt.Errorf("memory_request: %w", err)
	}
	if _, err := parseMemory(item.MemoryLimit); err != nil {
		return fmt.Errorf("memory_limit: %w", err)
	}
	if item.HPA.MinReplicas < 1 || item.HPA.MaxReplicas < item.HPA.MinReplicas {
		return fmt.Errorf("hpa: need 1 <= min_replicas <= max_replicas")
	}
	if item.HPA.TargetCPUPercent < 1 || item.HPA.TargetCPUPercent > 100 {
		return fmt.Errorf("hpa: target_cpu_percent must be between 1 and 100")
	}
	return nil
}
