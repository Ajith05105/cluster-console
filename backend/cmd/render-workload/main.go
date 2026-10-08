// render-workload prints the Kubernetes YAML for one catalog entry, exactly
// as the console's Deploy button would create it.
//
// It does not talk to the cluster. It exists for two reasons:
//   - to check the templates against the real cluster with a dry run:
//     make render KEY=camera-ingest | kubectl apply --dry-run=server -f -
//   - as a fallback during the demo: if the console is unavailable, the same
//     workload can be created by hand by piping this into `kubectl apply`.
//
// Run it with `make render`, for example:
//
//	make render KEY=camera-ingest
//	make render KEY=camera-ingest ARGS="-fast -cpu 250m -max 10"
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"cluster-console/backend/catalog"
)

func main() {
	// Command-line options. Each one mirrors a field on the Deploy form.
	key := flag.String("key", "", "catalog key to render (empty = the catalog's default)")
	cpu := flag.String("cpu", "", "CPU request, for example 500m (empty = catalog default)")
	memory := flag.String("memory", "", "memory request, for example 128Mi (empty = catalog default)")
	maxReplicas := flag.Int("max", 0, "autoscaler maximum (0 = catalog default)")
	fast := flag.Bool("fast", false, "replace pods on a dead node after 20 s instead of the 300 s baseline")
	list := flag.Bool("list", false, "list the catalog keys and exit")
	flag.Parse()

	c, err := catalog.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if *list {
		for _, item := range c.Items {
			fmt.Println(item.Key)
		}
		return
	}

	chosen := *key
	if chosen == "" {
		chosen = c.Default
	}

	yaml, err := c.Render(chosen, catalog.Options{
		CPURequest:    *cpu,
		MemoryRequest: *memory,
		MaxReplicas:   *maxReplicas,
		FastNodeDeath: *fast,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Print(strings.TrimRight(yaml, "\n") + "\n")
}
