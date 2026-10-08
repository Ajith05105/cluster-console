// console is the program that runs in the cluster as the demo console. This
// file only reads the settings and wires the parts together; the parts
// themselves live in the sibling packages:
//
//	catalog   what may be deployed, and the templates it is deployed from
//	cluster   watching Kubernetes and carrying out actions in `workload`
//	loadgen   the virtual cameras
//	warnings  the rules behind the warning banner
//	record    the event log and the per-second stats, and their CSV files
//	api       the web API and live stream the React page talks to
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"cluster-console/backend/api"
	"cluster-console/backend/catalog"
	"cluster-console/backend/cluster"
	"cluster-console/backend/loadgen"
	"cluster-console/backend/record"
	"cluster-console/internal/scene"
)

func main() {
	// --- Settings, from environment variables -----------------------------
	// The Deployment in deploy/manifests/console.yaml sets these.
	listen := env("LISTEN_ADDR", ":8080")
	dataDir := env("DATA_DIR", "/data")
	token := os.Getenv("CONSOLE_TOKEN") // from a Kubernetes Secret; never logged
	readOnly := env("READ_ONLY", "false") == "true"
	argoNamespace := env("ARGOCD_NAMESPACE", "argocd")

	// Where the load generator sends frames. The route's host name does not
	// resolve inside the cluster, so requests go to Traefik's in-cluster
	// address with the host name set as a header.
	loadTarget := env("LOAD_TARGET_URL", "http://traefik.kube-system.svc.cluster.local")
	loadHost := env("LOAD_HOST", catalog.RouteHost)
	loadMaxRate := envNumber("LOAD_MAX_RATE", 600)      // frames per second
	loadTimeoutMs := envNumber("LOAD_TIMEOUT_MS", 2000) // per frame

	// --- The catalog -------------------------------------------------------
	cat, err := catalog.Load()
	if err != nil {
		log.Fatalf("cannot start: %v", err)
	}

	// --- The CSV files -----------------------------------------------------
	// A new pair of files is started each time the console starts, named
	// with the start time in UTC.
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("cannot start: data folder %s: %v", dataDir, err)
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	events, err := record.NewEventLog(filepath.Join(dataDir, "events-"+stamp+".csv"))
	if err != nil {
		log.Fatalf("cannot start: %v", err)
	}
	defer events.Close()
	stats, err := record.NewStatsWriter(filepath.Join(dataDir, "stats-"+stamp+".csv"))
	if err != nil {
		log.Fatalf("cannot start: %v", err)
	}
	defer stats.Close()

	// --- The connection to Kubernetes ---------------------------------------
	kubeConfig, err := kubernetesConfig()
	if err != nil {
		log.Fatalf("cannot start: no way to reach Kubernetes: %v", err)
	}
	client, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		log.Fatalf("cannot start: %v", err)
	}
	dynamicClient, err := dynamic.NewForConfig(kubeConfig)
	if err != nil {
		log.Fatalf("cannot start: %v", err)
	}
	cl := cluster.New(client, dynamicClient, cat, argoNamespace)

	// --- The load generator -------------------------------------------------
	load := loadgen.New(loadgen.Config{
		TargetURL: loadTarget,
		Host:      loadHost,
		MaxRate:   loadMaxRate,
		Timeout:   time.Duration(loadTimeoutMs) * time.Millisecond,
		Frames:    scene.Frames(), // the ready-made JPEGs, made once here
		Shape:     cl.LoadShape,
	})
	defer load.Stop()

	// --- The API, and joining everything up ---------------------------------
	server := api.New(api.Config{Token: token, ReadOnly: readOnly, DataDir: dataDir}, cat, cl, load, events, stats)
	cl.OnChange = server.ClusterChanged
	cl.Emit = func(kind, message string) { events.Add(kind, message) }

	// ctx is cancelled when Kubernetes asks the pod to stop (SIGTERM) or
	// someone presses Ctrl-C. Everything below watches it and winds down.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	switch {
	case readOnly:
		log.Print("READ_ONLY is on: every POST will be refused")
	case token == "":
		log.Print("CONSOLE_TOKEN is empty: every POST will be refused until a token is configured")
	}

	cl.Start(ctx)
	go server.Run(ctx)
	events.Add("action", "console started")

	httpServer := &http.Server{
		Addr:              listen,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// No write timeout: the live stream stays open for as long as the
		// browser is connected.
	}
	go func() {
		log.Printf("console listening on %s (data in %s, load target %s with Host %s, max %g frames/s)",
			listen, dataDir, loadTarget, loadHost, loadMaxRate)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("web server stopped unexpectedly: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("stop requested")
	load.Stop()
	events.Add("action", "console stopping")
	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(deadline)
}

// kubernetesConfig works out how to reach the Kubernetes API.
//
// Inside the cluster, Kubernetes gives every pod the address and a
// short-lived credential for its ServiceAccount automatically; that is the
// normal case. Outside (for trying the console on a laptop) it falls back to
// the kubeconfig file named by the KUBECONFIG variable.
func kubernetesConfig() (*rest.Config, error) {
	if config, err := rest.InClusterConfig(); err == nil {
		return config, nil
	}
	path := os.Getenv("KUBECONFIG")
	if path == "" {
		return nil, fmt.Errorf("not running inside a cluster and KUBECONFIG is not set")
	}
	return clientcmd.BuildConfigFromFlags("", path)
}

// env reads an environment variable, or returns the fallback if it is unset.
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// envNumber reads a number from an environment variable, or returns the
// fallback if it is unset or not a positive number.
func envNumber(name string, fallback float64) float64 {
	value, err := strconv.ParseFloat(os.Getenv(name), 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
