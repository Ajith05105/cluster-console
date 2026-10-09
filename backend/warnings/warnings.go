// Package warnings decides what goes in the warning banner.
//
// Once a second the console hands this package a plain description of how
// things stand (which pods are waiting, which nodes are down, how many frames
// failed...) and gets back the list of warnings that apply. Every warning
// carries a reason written for a person to read.
//
// The package knows nothing about Kubernetes or HTTP. It only applies the
// five rules below to the facts it is given, which keeps it easy to test.
package warnings

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The thresholds for the rules.
const (
	// A pod must have been unschedulable this long before we warn. Shorter
	// waits are normal while the scheduler catches up.
	PendingAfter = 15 * time.Second

	// Ready pods must have been below the wanted number this long before we
	// warn. Shorter gaps are normal while new pods start.
	ReplicasShortAfter = 10 * time.Second

	// We warn when more than this share of frames failed (0.01 = 1%)...
	FailedFramesAbove = 0.01
	// ...measured over this many of the most recent seconds...
	FailedFramesWindow = 10
	// ...and only if at least this many frames were sent in that time, so
	// one unlucky frame out of three does not raise an alarm.
	FailedFramesMinSent = 20
)

// Warning is one line in the warning banner.
type Warning struct {
	// ID identifies the warning from one second to the next, so the console
	// can tell "still the same warning" from "a new one".
	ID string `json:"id"`
	// Kind is which rule raised it: pending, node, replicas, frames or argo.
	Kind string `json:"kind"`
	// Reason is the sentence shown to the person.
	Reason string `json:"reason"`
	// Since is when the problem began.
	Since time.Time `json:"since"`
}

// Input is the description of how things stand right now.
type Input struct {
	Now      time.Time
	Pending  []PendingPod
	Nodes    []Node
	Workload *Workload    // nil when nothing is deployed
	Recent   []LoadSecond // the most recent seconds, oldest first
	ArgoApps []ArgoApp
}

// PendingPod is a pod that has not been given a node.
type PendingPod struct {
	Name string
	// Unschedulable is true when the scheduler has looked at the pod and
	// found no node it fits on.
	Unschedulable bool
	// Reason is the scheduler's explanation, for example "Insufficient cpu".
	Reason string
	// Since is when the scheduler first reported that.
	Since time.Time
}

// Node is one machine in the cluster.
type Node struct {
	Name  string
	Ready bool
	// NotReadySince is when it stopped being ready.
	NotReadySince time.Time
}

// Workload is the deployed workload's pod counts.
type Workload struct {
	Name    string
	Desired int // pods wanted
	Ready   int // pods ready to take traffic
}

// LoadSecond is one second of load generator counts.
type LoadSecond struct {
	Sent          int
	Failed        int
	FailedBusy    int
	FailedTimeout int
	FailedOther   int
}

// ArgoApp is one Argo CD application and whether it matches git.
type ArgoApp struct {
	Name string
	Sync string // "Synced", "OutOfSync" or "Unknown"
}

// Engine applies the rules. It remembers when each ongoing problem was first
// seen, which is how it knows how long something has been wrong.
type Engine struct {
	firstSeen map[string]time.Time
}

// NewEngine makes an Engine.
func NewEngine() *Engine {
	return &Engine{firstSeen: map[string]time.Time{}}
}

// Evaluate applies all the rules and returns the warnings that apply right
// now, in a fixed order.
func (e *Engine) Evaluate(in Input) []Warning {
	found := []Warning{}
	// active notes which remembered problems are still going on; the rest
	// are forgotten at the end.
	active := map[string]bool{}

	// since returns when the problem with this id was first seen, noting the
	// time now if it is new.
	since := func(id string) time.Time {
		active[id] = true
		if _, known := e.firstSeen[id]; !known {
			e.firstSeen[id] = in.Now
		}
		return e.firstSeen[id]
	}

	// Rule 1: pods that have been Pending and unschedulable for too long.
	// The autoscaler does not know whether nodes have room, so this is the
	// only place a "cluster is full" problem shows up. Pods with the same
	// reason are reported together.
	type group struct {
		count int
		since time.Time
	}
	groups := map[string]*group{}
	for _, pod := range in.Pending {
		if !pod.Unschedulable || in.Now.Sub(pod.Since) <= PendingAfter {
			continue
		}
		g := groups[pod.Reason]
		if g == nil {
			g = &group{since: pod.Since}
			groups[pod.Reason] = g
		}
		g.count++
		if pod.Since.Before(g.since) {
			g.since = pod.Since
		}
	}
	for _, reason := range sortedKeys(groups) {
		g := groups[reason]
		found = append(found, Warning{
			ID:   "pending:" + reason,
			Kind: "pending",
			Reason: fmt.Sprintf("%s Pending for %s with no node to run on: %s",
				plural(g.count, "pod"), age(in.Now.Sub(g.since)), reason),
			Since: g.since,
		})
	}

	// Rule 2: a node is NotReady. This applies to every node alike, workers
	// and control-plane; none is left out.
	for _, node := range in.Nodes {
		if node.Ready {
			continue
		}
		started := node.NotReadySince
		if started.IsZero() {
			started = since("node:" + node.Name)
		}
		found = append(found, Warning{
			ID:     "node:" + node.Name,
			Kind:   "node",
			Reason: fmt.Sprintf("Node %s is NotReady (for %s)", node.Name, age(in.Now.Sub(started))),
			Since:  started,
		})
	}

	// Rule 3: fewer pods ready than wanted, for too long.
	if w := in.Workload; w != nil && w.Ready < w.Desired {
		started := since("replicas")
		if in.Now.Sub(started) > ReplicasShortAfter {
			found = append(found, Warning{
				ID:   "replicas",
				Kind: "replicas",
				Reason: fmt.Sprintf("%s has %d of %d pods ready (short for %s)",
					w.Name, w.Ready, w.Desired, age(in.Now.Sub(started))),
				Since: started,
			})
		}
	}

	// Rule 4: too many frames failing.
	recent := in.Recent
	if len(recent) > FailedFramesWindow {
		recent = recent[len(recent)-FailedFramesWindow:]
	}
	var total LoadSecond
	for _, second := range recent {
		total.Sent += second.Sent
		total.Failed += second.Failed
		total.FailedBusy += second.FailedBusy
		total.FailedTimeout += second.FailedTimeout
		total.FailedOther += second.FailedOther
	}
	if total.Sent >= FailedFramesMinSent && float64(total.Failed) > FailedFramesAbove*float64(total.Sent) {
		found = append(found, Warning{
			ID:   "frames",
			Kind: "frames",
			Reason: fmt.Sprintf("%.1f%% of frames failed in the last %d s (%d of %d: %s)",
				100*float64(total.Failed)/float64(total.Sent), len(recent), total.Failed, total.Sent,
				failureBreakdown(total)),
			Since: since("frames"),
		})
	}

	// Rule 5: an Argo CD app no longer matches git.
	for _, app := range in.ArgoApps {
		if app.Sync != "OutOfSync" {
			continue
		}
		id := "argo:" + app.Name
		found = append(found, Warning{
			ID:     id,
			Kind:   "argo",
			Reason: fmt.Sprintf("Argo CD app %s is OutOfSync: the cluster no longer matches git", app.Name),
			Since:  since(id),
		})
	}

	// Forget problems that have gone away, so that if they come back later
	// their clock starts again from zero.
	for id := range e.firstSeen {
		if !active[id] {
			delete(e.firstSeen, id)
		}
	}
	return found
}

// failureBreakdown describes what kinds of failure made up the total, for
// example "30 busy, 8 timed out".
func failureBreakdown(total LoadSecond) string {
	parts := []string{}
	if total.FailedBusy > 0 {
		parts = append(parts, fmt.Sprintf("%d busy", total.FailedBusy))
	}
	if total.FailedTimeout > 0 {
		parts = append(parts, fmt.Sprintf("%d timed out", total.FailedTimeout))
	}
	if total.FailedOther > 0 {
		parts = append(parts, fmt.Sprintf("%d other errors", total.FailedOther))
	}
	return strings.Join(parts, ", ")
}

// age writes a length of time in whole seconds, or minutes and seconds once
// it is over a minute: "23 s", "2 min 05 s".
func age(length time.Duration) string {
	seconds := int(length.Seconds())
	if seconds < 0 {
		seconds = 0
	}
	if seconds < 60 {
		return fmt.Sprintf("%d s", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%d min %02d s", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%d h %02d min", seconds/3600, seconds%3600/60)
}

// plural writes "1 pod" or "3 pods".
func plural(count int, word string) string {
	if count == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", count, word)
}

// sortedKeys returns a map's keys in alphabetical order, so warnings always
// come out in the same order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
