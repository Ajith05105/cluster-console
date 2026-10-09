package catalog

// Automated tests for the catalog and the templates. Run with `make test`.
//
// These check the text that comes out of Render. Whether Kubernetes accepts
// that text is checked separately, against the real cluster, with
// `make render` piped into `kubectl apply --dry-run=server`.

import (
	"strings"
	"testing"
)

// mustLoad loads the built-in catalog or stops the test.
func mustLoad(t *testing.T) *Catalog {
	t.Helper()
	c, err := Load()
	if err != nil {
		t.Fatalf("catalog did not load: %v", err)
	}
	return c
}

// mustRender renders one entry or stops the test.
func mustRender(t *testing.T, c *Catalog, key string, options Options) string {
	t.Helper()
	yaml, err := c.Render(key, options)
	if err != nil {
		t.Fatalf("Render(%q) failed: %v", key, err)
	}
	return yaml
}

// wantLine fails the test unless the YAML has a line that reads exactly
// `line` once leading spaces are ignored.
func wantLine(t *testing.T, yaml, line string) {
	t.Helper()
	if !hasLine(yaml, line) {
		t.Errorf("rendered YAML has no line %q", line)
	}
}

func hasLine(yaml, line string) bool {
	for _, got := range strings.Split(yaml, "\n") {
		if strings.TrimSpace(got) == line {
			return true
		}
	}
	return false
}

// The catalog has the five expected entries and camera-ingest is the default.
func TestCatalogContents(t *testing.T) {
	c := mustLoad(t)

	if c.Default != "camera-ingest" {
		t.Errorf("default = %q, want camera-ingest", c.Default)
	}
	for _, key := range []string{"camera-ingest", "nginx-alpine", "httpd-alpine", "caddy", "whoami"} {
		if _, found := c.Find(key); !found {
			t.Errorf("catalog has no entry %q", key)
		}
	}
	if len(c.Items) != 5 {
		t.Errorf("catalog has %d entries, want 5", len(c.Items))
	}
	if _, found := c.Find("not-a-real-key"); found {
		t.Error("Find returned an entry for a key that does not exist")
	}
}

// Every image must come from the cluster's own registry, because the cluster
// cannot pull from anywhere else.
func TestEveryImageIsInGitea(t *testing.T) {
	for _, item := range mustLoad(t).Items {
		if !strings.HasPrefix(item.Image, "gitea.cluster.local:3000/") {
			t.Errorf("%s: image %q is not in the Gitea registry", item.Key, item.Image)
		}
	}
}

// Every entry renders into exactly four objects, all in the workload
// namespace, all labelled managed-by=console, with the placement rules.
func TestEveryEntryRenders(t *testing.T) {
	c := mustLoad(t)

	for _, item := range c.Items {
		yaml := mustRender(t, c, item.Key, Options{})

		documents := strings.Split(yaml, "---\n")
		if len(documents) != 4 {
			t.Errorf("%s: rendered %d objects, want 4", item.Key, len(documents))
		}
		for i, document := range documents {
			if !hasLine(document, "namespace: workload") {
				t.Errorf("%s: object %d is not in the workload namespace", item.Key, i)
			}
			if !hasLine(document, "managed-by: console") {
				t.Errorf("%s: object %d is not labelled managed-by=console", item.Key, i)
			}
		}
		if strings.Count(yaml, "namespace:") != 4 {
			t.Errorf("%s: want exactly 4 namespace lines, got %d", item.Key, strings.Count(yaml, "namespace:"))
		}

		wantLine(t, yaml, "kind: Deployment")
		wantLine(t, yaml, "kind: Service")
		wantLine(t, yaml, "kind: HorizontalPodAutoscaler")
		wantLine(t, yaml, "kind: Ingress")
		wantLine(t, yaml, "image: "+item.Image)
		wantLine(t, yaml, "- host: workload.cluster.local")

		// Placement: not on control-plane nodes, and that is the only rule.
		// No worker is named or left out.
		wantLine(t, yaml, "- key: node-role.kubernetes.io/control-plane")
		wantLine(t, yaml, "operator: DoesNotExist")
		if strings.Count(yaml, "- key:") != 3 {
			// One placement rule plus the two node-death tolerations.
			t.Errorf("%s: want 3 \"- key:\" lines (1 placement rule, 2 tolerations), got %d", item.Key, strings.Count(yaml, "- key:"))
		}
		for _, unwanted := range []string{"mac-mini-agent", "operator: NotIn", "- key: kubernetes.io/hostname"} {
			if strings.Contains(yaml, unwanted) {
				t.Errorf("%s: rendered YAML contains %q; no node may be singled out", item.Key, unwanted)
			}
		}

		// The pod count must be left to the autoscaler.
		for _, line := range strings.Split(yaml, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "replicas:") {
				t.Errorf("%s: template sets replicas, which must be left unset", item.Key)
			}
		}
	}
}

// With nothing chosen on the form, camera-ingest gets the agreed defaults,
// including the 300-second baseline for node death.
func TestCameraIngestDefaults(t *testing.T) {
	c := mustLoad(t)
	yaml := mustRender(t, c, "camera-ingest", Options{})

	wantLine(t, yaml, `cpu: "500m"`)
	wantLine(t, yaml, `cpu: "1"`)
	wantLine(t, yaml, `memory: "128Mi"`)
	wantLine(t, yaml, `memory: "256Mi"`)
	wantLine(t, yaml, "minReplicas: 1")
	wantLine(t, yaml, "maxReplicas: 20")
	wantLine(t, yaml, "averageUtilization: 50")
	wantLine(t, yaml, `value: "40"`)
	wantLine(t, yaml, "runAsNonRoot: true")

	wantLine(t, yaml, "tolerationSeconds: 300")
	if hasLine(yaml, "tolerationSeconds: 20") {
		t.Error("baseline render contains the fast 20-second setting")
	}
}

// The fast switch changes node-death replacement to 20 seconds, and nothing
// is left at 300.
func TestFastNodeDeathSwitch(t *testing.T) {
	c := mustLoad(t)
	yaml := mustRender(t, c, "camera-ingest", Options{FastNodeDeath: true})

	wantLine(t, yaml, "tolerationSeconds: 20")
	if hasLine(yaml, "tolerationSeconds: 300") {
		t.Error("fast render still contains the 300-second baseline")
	}
}

// Values from the Deploy form replace the catalog defaults, and a limit that
// would end up below the request is lifted to match it.
func TestFormOverrides(t *testing.T) {
	c := mustLoad(t)
	yaml := mustRender(t, c, "camera-ingest", Options{CPURequest: "1.5", MemoryRequest: "1Gi", MaxReplicas: 5})

	wantLine(t, yaml, "maxReplicas: 5")
	// CPU request 1.5 is above the catalog limit of 1, so both become 1.5.
	if strings.Count(yaml, `cpu: "1.5"`) != 2 {
		t.Errorf(`want cpu: "1.5" as both request and limit, got %d of them`, strings.Count(yaml, `cpu: "1.5"`))
	}
	// Memory request 1Gi is above the catalog limit of 256Mi, likewise.
	if strings.Count(yaml, `memory: "1Gi"`) != 2 {
		t.Errorf(`want memory: "1Gi" as both request and limit, got %d of them`, strings.Count(yaml, `memory: "1Gi"`))
	}
}

// The stock web servers are not locked down and have no CPU limit.
func TestExtrasAreNotLockedDown(t *testing.T) {
	c := mustLoad(t)
	yaml := mustRender(t, c, "nginx-alpine", Options{})

	if hasLine(yaml, "runAsNonRoot: true") {
		t.Error("nginx was rendered with the non-root setting, which it cannot run under")
	}
	wantLine(t, yaml, `cpu: "100m"`)
	if strings.Count(yaml, "cpu: ") != 1 {
		t.Errorf("nginx should have a CPU request and no CPU limit, got %d cpu lines", strings.Count(yaml, "cpu: "))
	}
}

// Anything that is not a catalog key, or not a plain number, is refused.
// The second case is the important one: text typed into the form must never
// be able to add its own lines to the YAML.
func TestBadInputIsRefused(t *testing.T) {
	c := mustLoad(t)

	bad := []struct {
		why     string
		key     string
		options Options
	}{
		{"unknown key", "docker.io/library/anything:latest", Options{}},
		{"extra YAML in the CPU field", "camera-ingest", Options{CPURequest: "500m\"\n      hostNetwork: true"}},
		{"words in the CPU field", "camera-ingest", Options{CPURequest: "lots"}},
		{"zero CPU", "camera-ingest", Options{CPURequest: "0m"}},
		{"bytes without a unit in the memory field", "camera-ingest", Options{MemoryRequest: "128"}},
		{"extra YAML in the memory field", "camera-ingest", Options{MemoryRequest: "128Mi\nnamespace: kube-system"}},
		{"too many replicas", "camera-ingest", Options{MaxReplicas: MaxReplicasCeiling + 1}},
		{"negative replicas", "camera-ingest", Options{MaxReplicas: -1}},
	}
	for _, test := range bad {
		if yaml, err := c.Render(test.key, test.options); err == nil {
			t.Errorf("%s: Render accepted it and produced %d bytes", test.why, len(yaml))
		}
	}
}

// The two amount readers agree with Kubernetes' meaning of the units.
func TestAmountReaders(t *testing.T) {
	cpu := map[string]int{"500m": 500, "1": 1000, "0.5": 500, "1.25": 1250, "2.0": 2000}
	for text, want := range cpu {
		if got, err := parseCPU(text); err != nil || got != want {
			t.Errorf("parseCPU(%q) = %d, %v; want %d", text, got, err, want)
		}
	}
	mem := map[string]int{"128Mi": 128, "1Gi": 1024, "2Gi": 2048}
	for text, want := range mem {
		if got, err := parseMemory(text); err != nil || got != want {
			t.Errorf("parseMemory(%q) = %d, %v; want %d", text, got, err, want)
		}
	}
}
