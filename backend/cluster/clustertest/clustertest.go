// Package clustertest is support code for the automated tests. It is not
// part of the console program.
//
// It builds a cluster.Cluster connected to a FAKE Kubernetes: an in-memory
// stand-in supplied by the Kubernetes client library that accepts the same
// calls as a real cluster and keeps a list of every call made to it. Tests
// use that list to prove what the console did and, more importantly, what it
// did not do.
package clustertest

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"cluster-console/backend/catalog"
	"cluster-console/backend/cluster"
)

// New returns a Cluster connected to a fake Kubernetes that starts out
// holding the given objects. The watchers are running by the time it
// returns, and are stopped automatically when the test ends.
func New(t *testing.T, objects ...runtime.Object) (*cluster.Cluster, *fake.Clientset) {
	t.Helper()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog did not load: %v", err)
	}

	client := fake.NewSimpleClientset(objects...)
	// nil = no Argo CD watcher; the tests that need one do not use this.
	cl := cluster.New(client, nil, cat, "argocd")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cl.Start(ctx)
	return cl, client
}

// Eventually waits up to three seconds for a condition to become true, and
// fails the test if it does not. It is needed because the watchers learn
// about a change a moment after the change is made.
func Eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// WritesOutside lists every changing call (create, update, patch, delete)
// the fake Kubernetes received that was aimed anywhere other than the given
// namespace. An empty result is the proof that nothing else was touched.
func WritesOutside(client *fake.Clientset, namespace string) []string {
	changing := map[string]bool{"create": true, "update": true, "patch": true, "delete": true, "delete-collection": true}
	var found []string
	for _, action := range client.Actions() {
		if changing[action.GetVerb()] && action.GetNamespace() != namespace {
			found = append(found, action.GetVerb()+" "+action.GetResource().Resource+" in namespace \""+action.GetNamespace()+"\"")
		}
	}
	return found
}

// Writes counts the changing calls the fake Kubernetes received.
func Writes(client *fake.Clientset) int {
	changing := map[string]bool{"create": true, "update": true, "patch": true, "delete": true, "delete-collection": true}
	count := 0
	for _, action := range client.Actions() {
		if changing[action.GetVerb()] {
			count++
		}
	}
	return count
}
