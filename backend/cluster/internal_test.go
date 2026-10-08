package cluster

// Tests for the small helper functions in this package.

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Deploy's last line of defence: only objects aimed at the workload
// namespace AND labelled as the console's may be created.
func TestIsOurs(t *testing.T) {
	label := map[string]string{"managed-by": "console"}
	service := func(namespace string, labels map[string]string) *corev1.Service {
		return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: namespace, Labels: labels}}
	}

	if !isOurs(service("workload", label), "workload") {
		t.Error("a labelled object in workload was refused")
	}
	refused := map[string]any{
		"an object in kube-system":        service("kube-system", label),
		"an object with no namespace":     service("", label),
		"an object without the label":     service("workload", nil),
		"an object with the wrong label":  service("workload", map[string]string{"managed-by": "someone-else"}),
		"something that is not an object": "just a string",
		"nothing at all":                  nil,
	}
	for what, object := range refused {
		if isOurs(object, "workload") {
			t.Errorf("%s was accepted", what)
		}
	}
}

// The scheduler's long sentence is boiled down to the useful part.
func TestShortSchedulingReason(t *testing.T) {
	// Copied from the real cluster on 2026-10-08.
	real := "0/6 nodes are available: 1 node(s) had untolerated taint(s), 2 Insufficient cpu, " +
		"3 node(s) didn't match Pod's node affinity/selector. no new claims to deallocate, preemption: " +
		"0/6 nodes are available: 2 No preemption victims found for incoming pod, 4 Preemption is not helpful for scheduling."

	cases := []struct{ reason, message, want string }{
		{"Unschedulable", real, "Insufficient cpu"},
		{"Unschedulable", "0/3 nodes are available: 1 Insufficient cpu, 2 Insufficient memory.", "Insufficient cpu, Insufficient memory"},
		{"Unschedulable", "0/6 nodes are available: 6 node(s) didn't match Pod's node affinity/selector.", "no node matches the placement rules"},
		{"Unschedulable", "something we have not seen before", "Unschedulable"},
		{"", "", "waiting for the scheduler"},
	}
	for _, c := range cases {
		if got := shortSchedulingReason(c.reason, c.message); got != c.want {
			t.Errorf("shortSchedulingReason(%q, %.40q...) = %q, want %q", c.reason, c.message, got, c.want)
		}
	}
}

// Node names sort the way a person expects.
func TestNameOrder(t *testing.T) {
	if !nameLess("agent-7", "agent-10") || nameLess("agent-10", "agent-7") {
		t.Error("agent-7 should come before agent-10")
	}
	if !nameLess("agent-10", "server-1") || !nameLess("mac-mini-agent", "server-1") {
		t.Error("names with different stems should sort alphabetically")
	}
}
