package catalog

// This file turns one catalog entry into the four Kubernetes objects that
// make up a running workload: Deployment, Service, autoscaler and Traefik
// route. The shapes of those objects are the template files in templates/;
// this code fills in the blanks.

import (
	"bytes"
	"embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"
)

// The template files, copied into the program at build time (see the note on
// go:embed in catalog.go).
//
//go:embed templates/*.yaml.tmpl
var templateFiles embed.FS

const (
	// Namespace is the only namespace workloads are ever rendered into.
	// It is fixed here, not an input, so nothing a caller passes in can
	// point a workload somewhere else.
	Namespace = "workload"

	// RouteHost is the host name of the Traefik route. It is the same for
	// every catalog entry.
	RouteHost = "workload.cluster.local"

	// ManagedByLabel and ManagedByValue are the label every rendered object
	// carries (managed-by=console). The Remove button deletes objects with
	// this label and nothing else.
	ManagedByLabel = "managed-by"
	ManagedByValue = "console"

	// MaxReplicasCeiling is the largest autoscaler maximum we accept. The
	// ResourceQuota in deploy/manifests/workload-quota.yaml allows 60 pods:
	// twice this, so that every pod can overlap with its replacement when a
	// node dies.
	MaxReplicasCeiling = 30
)

// The order the objects are written out in.
var templateOrder = []string{
	"deployment.yaml.tmpl",
	"service.yaml.tmpl",
	"hpa.yaml.tmpl",
	"ingress.yaml.tmpl",
}

// Options are the choices made on the console's Deploy form. Leaving a field
// empty (or zero) means "use the catalog's default".
type Options struct {
	CPURequest    string // for example "500m" or "1"
	MemoryRequest string // for example "128Mi" or "1Gi"
	MaxReplicas   int    // the autoscaler's upper limit

	// FastNodeDeath picks how quickly pods on a dead node are replaced:
	// false = baseline (300 s, plain Kubernetes), true = fast (20 s).
	FastNodeDeath bool
}

// templateData is everything the template files can refer to. A placeholder
// such as {{.Image}} in a template is replaced by the Image field here.
type templateData struct {
	Namespace string
	Host      string

	Key          string
	Image        string
	Port         int
	HealthPath   string
	LivenessPath string
	Env          map[string]string
	LockedDown   bool

	CPURequest    string
	CPULimit      string // empty means "no CPU limit"
	MemoryRequest string
	MemoryLimit   string

	NodeDeathSeconds int

	TargetCPUPercent int
	MinReplicas      int
	MaxReplicas      int
}

// Render produces the YAML for one catalog entry: four objects separated by
// "---" lines, all in the workload namespace and all labelled
// managed-by=console.
//
// Everything typed by a person (the CPU and memory fields) is checked against
// a strict pattern before it goes anywhere near the YAML, so the form cannot
// be used to smuggle in extra settings.
func (c *Catalog) Render(key string, options Options) (string, error) {
	item, found := c.Find(key)
	if !found {
		return "", fmt.Errorf("%q is not in the catalog", key)
	}

	data := templateData{
		Namespace:        Namespace,
		Host:             RouteHost,
		Key:              item.Key,
		Image:            item.Image,
		Port:             item.Port,
		HealthPath:       item.HealthPath,
		LivenessPath:     item.LivenessPath,
		Env:              item.Env,
		LockedDown:       item.LockedDown,
		CPURequest:       item.CPURequest,
		MemoryRequest:    item.MemoryRequest,
		MemoryLimit:      item.MemoryLimit,
		NodeDeathSeconds: c.NodeDeathSeconds.Baseline,
		TargetCPUPercent: item.HPA.TargetCPUPercent,
		MinReplicas:      item.HPA.MinReplicas,
		MaxReplicas:      item.HPA.MaxReplicas,
	}
	if item.CPULimit != nil {
		data.CPULimit = *item.CPULimit
	}

	// Apply the choices from the form on top of the catalog defaults.
	if options.CPURequest != "" {
		data.CPURequest = options.CPURequest
	}
	if options.MemoryRequest != "" {
		data.MemoryRequest = options.MemoryRequest
	}
	if options.MaxReplicas != 0 {
		data.MaxReplicas = options.MaxReplicas
	}
	if options.FastNodeDeath {
		data.NodeDeathSeconds = c.NodeDeathSeconds.Fast
	}

	// Check the numbers.
	cpuRequest, err := parseCPU(data.CPURequest)
	if err != nil {
		return "", fmt.Errorf("CPU request: %w", err)
	}
	memoryRequest, err := parseMemory(data.MemoryRequest)
	if err != nil {
		return "", fmt.Errorf("memory request: %w", err)
	}
	if data.MaxReplicas < data.MinReplicas || data.MaxReplicas > MaxReplicasCeiling {
		return "", fmt.Errorf("max replicas must be between %d and %d", data.MinReplicas, MaxReplicasCeiling)
	}

	// Kubernetes refuses a pod whose request is bigger than its limit. If
	// the form asks for more than the catalog's limit, lift the limit to
	// match the request.
	if data.CPULimit != "" {
		cpuLimit, _ := parseCPU(data.CPULimit) // already checked by Load
		if cpuRequest > cpuLimit {
			data.CPULimit = data.CPURequest
		}
	}
	memoryLimit, _ := parseMemory(data.MemoryLimit) // already checked by Load
	if memoryRequest > memoryLimit {
		data.MemoryLimit = data.MemoryRequest
	}

	// Fill in each template in turn and join the results.
	templates, err := template.ParseFS(templateFiles, "templates/*.yaml.tmpl")
	if err != nil {
		return "", fmt.Errorf("reading templates: %w", err)
	}
	var out bytes.Buffer
	for i, name := range templateOrder {
		if i > 0 {
			out.WriteString("---\n")
		}
		if err := templates.ExecuteTemplate(&out, name, data); err != nil {
			return "", fmt.Errorf("filling in %s: %w", name, err)
		}
	}
	return out.String(), nil
}

// The only spellings of CPU and memory amounts we accept. Kubernetes itself
// allows many more, but a short strict list is easy to reason about.
var (
	cpuMillis = regexp.MustCompile(`^([0-9]{1,5})m$`)                 // "500m"
	cpuCores  = regexp.MustCompile(`^([0-9]{1,2})(\.([0-9]{1,3}))?$`) // "1", "0.5", "1.25"
	memory    = regexp.MustCompile(`^([0-9]{1,5})(Mi|Gi)$`)           // "128Mi", "1Gi"
)

// parseCPU reads a CPU amount and returns it in thousandths of a core
// ("500m" and "0.5" both give 500).
func parseCPU(text string) (int, error) {
	if match := cpuMillis.FindStringSubmatch(text); match != nil {
		millis, _ := strconv.Atoi(match[1])
		if millis == 0 {
			return 0, fmt.Errorf("%q must be more than zero", text)
		}
		return millis, nil
	}
	if match := cpuCores.FindStringSubmatch(text); match != nil {
		whole, _ := strconv.Atoi(match[1])
		// Pad the part after the dot to three digits: "5" -> "500".
		fraction, _ := strconv.Atoi((match[3] + "000")[:3])
		millis := whole*1000 + fraction
		if millis == 0 {
			return 0, fmt.Errorf("%q must be more than zero", text)
		}
		return millis, nil
	}
	return 0, fmt.Errorf("%q is not a CPU amount; write it like 500m or 1", strings.TrimSpace(text))
}

// parseMemory reads a memory amount and returns it in mebibytes
// ("1Gi" gives 1024).
func parseMemory(text string) (int, error) {
	match := memory.FindStringSubmatch(text)
	if match == nil {
		return 0, fmt.Errorf("%q is not a memory amount; write it like 128Mi or 1Gi", strings.TrimSpace(text))
	}
	amount, _ := strconv.Atoi(match[1])
	if amount == 0 {
		return 0, fmt.Errorf("%q must be more than zero", text)
	}
	if match[2] == "Gi" {
		amount *= 1024
	}
	return amount, nil
}
