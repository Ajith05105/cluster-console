package warnings

// Automated tests for the five warning rules. Each test describes a
// situation, with a made-up clock, and checks which warnings come out.

import (
	"strings"
	"testing"
	"time"
)

// t0 is an arbitrary fixed starting time for the made-up clock.
var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// at returns the time `seconds` after t0.
func at(seconds int) time.Time { return t0.Add(time.Duration(seconds) * time.Second) }

// find returns the warning of the given kind, or nil if there is none.
func find(list []Warning, kind string) *Warning {
	for i := range list {
		if list[i].Kind == kind {
			return &list[i]
		}
	}
	return nil
}

// Nothing wrong, nothing reported.
func TestQuietWhenAllIsWell(t *testing.T) {
	got := NewEngine().Evaluate(Input{
		Now:      at(100),
		Nodes:    []Node{{Name: "agent-7", Ready: true}},
		Workload: &Workload{Name: "camera-ingest", Desired: 3, Ready: 3},
		Recent:   []LoadSecond{{Sent: 100}, {Sent: 100}},
		ArgoApps: []ArgoApp{{Name: "cluster-console", Sync: "Synced"}},
	})
	if len(got) != 0 {
		t.Errorf("want no warnings, got %+v", got)
	}
}

// Rule 1: an unschedulable pod is reported only after 15 seconds, with the
// scheduler's reason in the text.
func TestPendingPods(t *testing.T) {
	engine := NewEngine()
	pods := []PendingPod{
		{Name: "a", Unschedulable: true, Reason: "Insufficient cpu", Since: at(0)},
		{Name: "b", Unschedulable: true, Reason: "Insufficient cpu", Since: at(2)},
		// Not yet looked at by the scheduler: never reported by this rule.
		{Name: "c", Unschedulable: false, Since: at(0)},
	}

	if got := engine.Evaluate(Input{Now: at(15), Pending: pods}); find(got, "pending") != nil {
		t.Errorf("warned at exactly 15 s; want only after more than 15 s: %+v", got)
	}

	got := engine.Evaluate(Input{Now: at(16), Pending: pods})
	w := find(got, "pending")
	if w == nil {
		t.Fatalf("no pending warning at 16 s: %+v", got)
	}
	if !strings.Contains(w.Reason, "Insufficient cpu") || !strings.Contains(w.Reason, "1 pod ") {
		t.Errorf("reason %q should name the scheduler reason and count 1 pod (the other has waited only 14 s)", w.Reason)
	}

	got = engine.Evaluate(Input{Now: at(30), Pending: pods})
	if w = find(got, "pending"); w == nil || !strings.Contains(w.Reason, "2 pods") || !w.Since.Equal(at(0)) {
		t.Errorf("at 30 s want 2 pods since t0, got %+v", w)
	}
}

// Rule 1: different scheduler reasons are reported separately.
func TestPendingPodsGroupedByReason(t *testing.T) {
	got := NewEngine().Evaluate(Input{Now: at(60), Pending: []PendingPod{
		{Name: "a", Unschedulable: true, Reason: "Insufficient cpu", Since: at(0)},
		{Name: "b", Unschedulable: true, Reason: "Insufficient memory", Since: at(0)},
	}})
	if len(got) != 2 {
		t.Errorf("want 2 warnings (one per reason), got %+v", got)
	}
}

// Rule 2: a NotReady node is reported at once. Every node is treated the
// same: there is no list of nodes to stay quiet about.
func TestNodeNotReady(t *testing.T) {
	got := NewEngine().Evaluate(Input{Now: at(50), Nodes: []Node{
		{Name: "agent-7", Ready: false, NotReadySince: at(45)},
		{Name: "agent-10", Ready: true},
		{Name: "mac-mini-agent", Ready: false, NotReadySince: at(0)},
		{Name: "server-2", Ready: false, NotReadySince: at(20)},
	}})
	if len(got) != 3 {
		t.Fatalf("want one warning for each of the three NotReady nodes, got %+v", got)
	}
	reasons := map[string]string{}
	for _, warning := range got {
		if warning.Kind != "node" {
			t.Errorf("unexpected warning kind %q", warning.Kind)
		}
		reasons[warning.ID] = warning.Reason
	}
	if !strings.Contains(reasons["node:agent-7"], "agent-7") || !strings.Contains(reasons["node:agent-7"], "5 s") {
		t.Errorf("agent-7 reason %q should name the node and say 5 s", reasons["node:agent-7"])
	}
	if !strings.Contains(reasons["node:mac-mini-agent"], "mac-mini-agent") || !strings.Contains(reasons["node:mac-mini-agent"], "50 s") {
		t.Errorf("mac-mini-agent reason %q should name the node and say 50 s", reasons["node:mac-mini-agent"])
	}
	if _, warned := reasons["node:server-2"]; !warned {
		t.Error("no warning for the NotReady control-plane node")
	}
}

// Rule 3: ready pods below the wanted number is reported only after it has
// lasted more than 10 seconds, and the clock restarts if it recovers.
func TestReplicasShort(t *testing.T) {
	engine := NewEngine()
	short := &Workload{Name: "camera-ingest", Desired: 7, Ready: 5}
	full := &Workload{Name: "camera-ingest", Desired: 7, Ready: 7}

	for _, second := range []int{0, 5, 10} {
		if got := engine.Evaluate(Input{Now: at(second), Workload: short}); find(got, "replicas") != nil {
			t.Errorf("warned after only %d s", second)
		}
	}
	got := engine.Evaluate(Input{Now: at(11), Workload: short})
	w := find(got, "replicas")
	if w == nil || !strings.Contains(w.Reason, "5 of 7") {
		t.Fatalf("at 11 s want a warning saying 5 of 7, got %+v", got)
	}

	// Recovers, then drops again: the 10 seconds start over.
	engine.Evaluate(Input{Now: at(12), Workload: full})
	if got := engine.Evaluate(Input{Now: at(13), Workload: short}); find(got, "replicas") != nil {
		t.Error("warned immediately after a recovery; the 10 s should start again")
	}
	if got := engine.Evaluate(Input{Now: at(24), Workload: short}); find(got, "replicas") == nil {
		t.Error("no warning 11 s after the second drop")
	}
}

// Rule 4: more than 1% failed frames over the last 10 seconds.
func TestFailedFrames(t *testing.T) {
	// Exactly 1% is not "above 1%".
	seconds := make([]LoadSecond, 10)
	for i := range seconds {
		seconds[i] = LoadSecond{Sent: 100, Failed: 1, FailedBusy: 1}
	}
	if got := NewEngine().Evaluate(Input{Now: at(10), Recent: seconds}); find(got, "frames") != nil {
		t.Errorf("warned at exactly 1%%: %+v", got)
	}

	// One more failure tips it over.
	seconds[9] = LoadSecond{Sent: 100, Failed: 2, FailedBusy: 1, FailedTimeout: 1}
	got := NewEngine().Evaluate(Input{Now: at(10), Recent: seconds})
	w := find(got, "frames")
	if w == nil {
		t.Fatalf("no warning at 1.1%%: %+v", got)
	}
	if !strings.Contains(w.Reason, "11 of 1000") || !strings.Contains(w.Reason, "busy") || !strings.Contains(w.Reason, "timed out") {
		t.Errorf("reason %q should give the counts and the kinds of failure", w.Reason)
	}

	// Only the last 10 seconds count: old failures are ignored.
	old := append([]LoadSecond{{Sent: 100, Failed: 100, FailedOther: 100}}, make([]LoadSecond, 10)...)
	for i := 1; i < len(old); i++ {
		old[i] = LoadSecond{Sent: 100}
	}
	if got := NewEngine().Evaluate(Input{Now: at(11), Recent: old}); find(got, "frames") != nil {
		t.Errorf("warned about failures older than the window: %+v", got)
	}

	// Too few frames to judge.
	few := []LoadSecond{{Sent: 3, Failed: 1, FailedOther: 1}}
	if got := NewEngine().Evaluate(Input{Now: at(1), Recent: few}); find(got, "frames") != nil {
		t.Errorf("warned on 1 failure out of 3 frames: %+v", got)
	}
}

// Rule 5: an OutOfSync Argo CD app is reported by name.
func TestArgoOutOfSync(t *testing.T) {
	engine := NewEngine()
	apps := []ArgoApp{{Name: "cluster-console", Sync: "OutOfSync"}, {Name: "whoami", Sync: "Synced"}}

	got := engine.Evaluate(Input{Now: at(0), ArgoApps: apps})
	if len(got) != 1 || got[0].Kind != "argo" || !strings.Contains(got[0].Reason, "cluster-console") {
		t.Fatalf("want one warning naming cluster-console, got %+v", got)
	}

	// The start time is remembered while the problem lasts.
	got = engine.Evaluate(Input{Now: at(30), ArgoApps: apps})
	if !got[0].Since.Equal(at(0)) {
		t.Errorf("Since = %v, want the time it was first seen", got[0].Since)
	}
}
