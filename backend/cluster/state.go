package cluster

// This file builds the "state": a plain snapshot of the cluster as the
// console sees it right now, in a shape that is easy to send to the browser
// as JSON. Nothing here talks to the cluster over the network; it reads from
// the local copies that the watchers in cluster.go keep up to date.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"cluster-console/backend/catalog"
)

// State is the snapshot. The `json:"..."` tags are the field names the
// browser sees.
type State struct {
	// Nodes is every machine in the cluster, each with the workload pods
	// running on it.
	Nodes []Node `json:"nodes"`
	// Pending is the workload pods that have not been given a node.
	Pending []Pod `json:"pending"`
	// Workload is what the console has deployed, or null if nothing.
	Workload *Workload `json:"workload"`
	// HPA is the autoscaler's view, or null if there is none.
	HPA *Autoscaler `json:"hpa"`
	// Argo is the Argo CD applications and whether each matches git.
	Argo []ArgoApp `json:"argo"`
	// Sources says which parts of the picture we were able to read.
	Sources Sources `json:"sources"`
}

// Node is one machine.
type Node struct {
	Name string `json:"name"`
	// Known is false if we only know this node exists because a pod says it
	// runs there (which happens if the console may not read the node list).
	Known bool `json:"known"`
	Ready bool `json:"ready"`
	// NotReadySince is when it stopped being ready; null while ready.
	NotReadySince *time.Time `json:"not_ready_since"`
	// Role is "control-plane" or "worker".
	Role string `json:"role"`
	// Pool is "test" for workers the workload may use, "excluded" for
	// workers it may not, and "control-plane" for control-plane nodes.
	Pool string `json:"pool"`
	// CPUMillis is the node's allocatable CPU in thousandths of a core.
	CPUMillis int64 `json:"cpu_millis"`
	Pods      []Pod `json:"pods"`
}

// Pod is one copy of the workload.
type Pod struct {
	Name string `json:"name"`
	Node string `json:"node"` // empty while Pending with no node
	// Phase is Kubernetes' own word: Pending, Running, ...
	Phase string `json:"phase"`
	// Ready means it is passing its health check and receiving traffic.
	Ready bool `json:"ready"`
	// Terminating means it has been told to stop and is shutting down.
	Terminating bool      `json:"terminating"`
	Created     time.Time `json:"created"`
	// Detail is a short extra note such as "ContainerCreating".
	Detail string `json:"detail,omitempty"`

	// The next four are only filled in for pods with no node.
	// Unschedulable is true once the scheduler has tried and found nowhere
	// to put the pod.
	Unschedulable bool `json:"unschedulable,omitempty"`
	// PendingReason is the short reason, for example "Insufficient cpu".
	PendingReason string `json:"pending_reason,omitempty"`
	// PendingMessage is the scheduler's full sentence.
	PendingMessage string `json:"pending_message,omitempty"`
	// PendingSince is when the scheduler first reported it.
	PendingSince *time.Time `json:"pending_since,omitempty"`
}

// Workload describes what is deployed.
type Workload struct {
	Key   string `json:"key"`   // catalog key, for example "camera-ingest"
	Name  string `json:"name"`  // the catalog's display name
	Image string `json:"image"` // image address

	Desired int `json:"desired"` // pods wanted
	Ready   int `json:"ready"`   // pods ready

	CPURequest    string `json:"cpu_request"`
	MemoryRequest string `json:"memory_request"`

	// NodeDeathSeconds is how long pods on a dead node wait before being
	// replaced; FastNodeDeath is true when that is the fast setting.
	NodeDeathSeconds int  `json:"node_death_seconds"`
	FastNodeDeath    bool `json:"fast_node_death"`
}

// Autoscaler is the HorizontalPodAutoscaler's view.
type Autoscaler struct {
	Min     int `json:"min"`
	Max     int `json:"max"`
	Current int `json:"current"` // pods it counts now
	Desired int `json:"desired"` // pods it wants
	// CPUPercent is average CPU use as a share of the CPU request; null
	// until metrics have arrived.
	CPUPercent    *int `json:"cpu_percent"`
	TargetPercent int  `json:"target_percent"`
}

// ArgoApp is one Argo CD application.
type ArgoApp struct {
	Name   string `json:"name"`
	Sync   string `json:"sync"`   // Synced, OutOfSync or Unknown
	Health string `json:"health"` // Healthy, Progressing, Degraded, ...
}

// Sources says which parts of the picture could be read. A false here means
// the console lacks permission or the watch has not connected yet.
type Sources struct {
	Workload bool `json:"workload"` // pods, deployments, autoscalers
	Nodes    bool `json:"nodes"`
	Argo     bool `json:"argo"`
}

// Snapshot builds the current State from the local copies.
func (c *Cluster) Snapshot() State {
	state := State{
		Nodes:   []Node{},
		Pending: []Pod{},
		Argo:    []ArgoApp{},
		Sources: Sources{
			Workload: c.podInformer.HasSynced() && c.deploymentInformer.HasSynced() && c.hpaInformer.HasSynced(),
			Nodes:    c.nodeInformer.HasSynced(),
			Argo:     c.argoInformer != nil && c.argoInformer.HasSynced(),
		},
	}

	// Sort the pods into "waiting for a node" and "on node X".
	pods, _ := c.podLister.List(labels.Everything())
	podsOnNode := map[string][]Pod{}
	for _, pod := range pods {
		// Skip pods that have finished for good.
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		info := describePod(pod)
		if info.Node == "" {
			state.Pending = append(state.Pending, info)
		} else {
			podsOnNode[info.Node] = append(podsOnNode[info.Node], info)
		}
	}
	sort.Slice(state.Pending, func(i, j int) bool { return state.Pending[i].Name < state.Pending[j].Name })

	// One entry per node we can see.
	nodes, _ := c.nodeLister.List(labels.Everything())
	listed := map[string]bool{}
	for _, node := range nodes {
		listed[node.Name] = true
		state.Nodes = append(state.Nodes, c.describeNode(node, podsOnNode[node.Name]))
	}
	// Pods can name a node we could not list. Show those nodes too, marked
	// as not known, so their pods are not lost from view.
	for name, list := range podsOnNode {
		if !listed[name] {
			sortPods(list)
			state.Nodes = append(state.Nodes, Node{Name: name, Role: "unknown", Pool: "unknown", Pods: list})
		}
	}
	sortNodes(state.Nodes)

	// The deployed workload and its autoscaler. There is normally at most
	// one of each; if there were several, the first by name is shown.
	deployments, _ := c.deploymentLister.List(managedSelector)
	sort.Slice(deployments, func(i, j int) bool { return deployments[i].Name < deployments[j].Name })
	if len(deployments) > 0 {
		state.Workload = c.describeWorkload(deployments[0])
	}
	autoscalers, _ := c.hpaLister.List(managedSelector)
	sort.Slice(autoscalers, func(i, j int) bool { return autoscalers[i].Name < autoscalers[j].Name })
	if len(autoscalers) > 0 {
		state.HPA = describeAutoscaler(autoscalers[0])
	}

	// Argo CD applications.
	if c.argoInformer != nil {
		for _, object := range c.argoInformer.GetStore().List() {
			if app, ok := object.(*unstructured.Unstructured); ok {
				state.Argo = append(state.Argo, describeArgoApp(app))
			}
		}
		sort.Slice(state.Argo, func(i, j int) bool { return state.Argo[i].Name < state.Argo[j].Name })
	}
	return state
}

// describeNode turns Kubernetes' record of a node into our Node.
func (c *Cluster) describeNode(node *corev1.Node, pods []Pod) Node {
	_, isControlPlane := node.Labels["node-role.kubernetes.io/control-plane"]

	out := Node{
		Name:      node.Name,
		Known:     true,
		Role:      "worker",
		Pool:      "excluded",
		CPUMillis: node.Status.Allocatable.Cpu().MilliValue(),
		Pods:      pods,
	}
	switch {
	case isControlPlane:
		out.Role, out.Pool = "control-plane", "control-plane"
	case c.catalog.InTestPool(node.Name, false):
		out.Pool = "test"
	}
	if out.Pods == nil {
		out.Pods = []Pod{}
	}
	sortPods(out.Pods)

	// A node reports its health as a list of "conditions". The one called
	// Ready is the one that matters here.
	for _, condition := range node.Status.Conditions {
		if condition.Type != corev1.NodeReady {
			continue
		}
		out.Ready = condition.Status == corev1.ConditionTrue
		if !out.Ready {
			since := condition.LastTransitionTime.Time
			out.NotReadySince = &since
		}
	}
	return out
}

// describePod turns Kubernetes' record of a pod into our Pod.
func describePod(pod *corev1.Pod) Pod {
	out := Pod{
		Name:        pod.Name,
		Node:        pod.Spec.NodeName,
		Phase:       string(pod.Status.Phase),
		Ready:       podIsReady(pod),
		Terminating: pod.DeletionTimestamp != nil,
		Created:     pod.CreationTimestamp.Time,
	}

	// If a container is waiting to start, say why (ContainerCreating,
	// ImagePullBackOff, ...).
	for _, container := range pod.Status.ContainerStatuses {
		if container.State.Waiting != nil && container.State.Waiting.Reason != "" {
			out.Detail = container.State.Waiting.Reason
		}
	}

	if out.Node != "" {
		return out
	}

	// No node yet. The scheduler records why in a condition called
	// PodScheduled.
	out.PendingReason = "waiting for the scheduler"
	for _, condition := range pod.Status.Conditions {
		if condition.Type != corev1.PodScheduled || condition.Status != corev1.ConditionFalse {
			continue
		}
		since := condition.LastTransitionTime.Time
		out.PendingSince = &since
		out.PendingMessage = condition.Message
		out.Unschedulable = condition.Reason == corev1.PodReasonUnschedulable
		out.PendingReason = shortSchedulingReason(condition.Reason, condition.Message)
	}
	return out
}

// insufficient finds phrases like "Insufficient cpu" in a scheduler message.
// The resource name may contain dots and slashes (for example
// "nvidia.com/gpu") but must end in a letter or digit, so the full stop at
// the end of a sentence is not swallowed.
var insufficient = regexp.MustCompile(`Insufficient [a-z]([a-z0-9./-]*[a-z0-9])?`)

// shortSchedulingReason boils the scheduler's long sentence down to the part
// that matters. The full sentence looks like:
//
//	0/6 nodes are available: 1 node(s) had untolerated taint(s),
//	2 Insufficient cpu, 3 node(s) didn't match Pod's node affinity/selector.
//
// Most of that describes nodes the pod was never allowed on. What we want is
// "Insufficient cpu".
func shortSchedulingReason(reason, message string) string {
	found := insufficient.FindAllString(message, -1)
	if len(found) > 0 {
		// Remove repeats, keep the order they appeared in.
		seen := map[string]bool{}
		unique := []string{}
		for _, phrase := range found {
			if !seen[phrase] {
				seen[phrase] = true
				unique = append(unique, phrase)
			}
		}
		return strings.Join(unique, ", ")
	}
	if strings.Contains(message, "didn't match Pod's node affinity") {
		return "no node matches the placement rules"
	}
	if reason != "" {
		return reason
	}
	return "waiting for the scheduler"
}

// describeWorkload turns a Deployment the console created into our Workload.
func (c *Cluster) describeWorkload(deployment *appsv1.Deployment) *Workload {
	out := &Workload{
		Key:     deployment.Labels["app"],
		Name:    deployment.Name,
		Desired: 1, // Kubernetes' default when replicas is not set
		Ready:   int(deployment.Status.ReadyReplicas),
	}
	if deployment.Spec.Replicas != nil {
		out.Desired = int(*deployment.Spec.Replicas)
	}
	if item, found := c.catalog.Find(out.Key); found {
		out.Name = item.Name
	}

	podSpec := deployment.Spec.Template.Spec
	if len(podSpec.Containers) > 0 {
		container := podSpec.Containers[0]
		out.Image = container.Image
		out.CPURequest = container.Resources.Requests.Cpu().String()
		out.MemoryRequest = container.Resources.Requests.Memory().String()
	}
	for _, toleration := range podSpec.Tolerations {
		if toleration.Key == "node.kubernetes.io/not-ready" && toleration.TolerationSeconds != nil {
			out.NodeDeathSeconds = int(*toleration.TolerationSeconds)
		}
	}
	out.FastNodeDeath = out.NodeDeathSeconds == c.catalog.NodeDeathSeconds.Fast
	return out
}

// describeAutoscaler turns a HorizontalPodAutoscaler into our Autoscaler.
func describeAutoscaler(hpa *autoscalingv2.HorizontalPodAutoscaler) *Autoscaler {
	out := &Autoscaler{
		Min:     1,
		Max:     int(hpa.Spec.MaxReplicas),
		Current: int(hpa.Status.CurrentReplicas),
		Desired: int(hpa.Status.DesiredReplicas),
	}
	if hpa.Spec.MinReplicas != nil {
		out.Min = int(*hpa.Spec.MinReplicas)
	}
	// The target: "keep average CPU at N% of the request".
	for _, metric := range hpa.Spec.Metrics {
		if metric.Resource != nil && metric.Resource.Name == corev1.ResourceCPU && metric.Resource.Target.AverageUtilization != nil {
			out.TargetPercent = int(*metric.Resource.Target.AverageUtilization)
		}
	}
	// The latest measurement, once there is one.
	for _, metric := range hpa.Status.CurrentMetrics {
		if metric.Resource != nil && metric.Resource.Name == corev1.ResourceCPU && metric.Resource.Current.AverageUtilization != nil {
			percent := int(*metric.Resource.Current.AverageUtilization)
			out.CPUPercent = &percent
		}
	}
	return out
}

// describeArgoApp picks the name, sync status and health out of an Argo CD
// Application. Argo CD's types are not built into this program, so the
// object is read as nested maps.
func describeArgoApp(app *unstructured.Unstructured) ArgoApp {
	sync, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
	health, _, _ := unstructured.NestedString(app.Object, "status", "health", "status")
	if sync == "" {
		sync = "Unknown"
	}
	if health == "" {
		health = "Unknown"
	}
	return ArgoApp{Name: app.GetName(), Sync: sync, Health: health}
}

// podIsReady reports whether a pod is passing its readiness check.
func podIsReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// nodeIsReady reports whether a node's Ready condition is true.
func nodeIsReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// managedSelector matches objects labelled managed-by=console.
var managedSelector = labels.SelectorFromSet(labels.Set{catalog.ManagedByLabel: catalog.ManagedByValue})

// sortPods orders pods by name.
func sortPods(pods []Pod) {
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
}

// sortNodes orders nodes for display: the test pool first, then excluded
// workers, then control-plane nodes; by name within each group.
func sortNodes(nodes []Node) {
	rank := map[string]int{"test": 0, "unknown": 1, "excluded": 2, "control-plane": 3}
	sort.Slice(nodes, func(i, j int) bool {
		if rank[nodes[i].Pool] != rank[nodes[j].Pool] {
			return rank[nodes[i].Pool] < rank[nodes[j].Pool]
		}
		return nameLess(nodes[i].Name, nodes[j].Name)
	})
}

// trailingNumber splits a name like "agent-10" into "agent-" and 10.
var trailingNumber = regexp.MustCompile(`^(.*?)([0-9]+)$`)

// nameLess orders names so that agent-7 comes before agent-10 (plain
// alphabetical order would put agent-10 first).
func nameLess(a, b string) bool {
	partsA, partsB := trailingNumber.FindStringSubmatch(a), trailingNumber.FindStringSubmatch(b)
	if partsA != nil && partsB != nil && partsA[1] == partsB[1] {
		numberA, _ := strconv.Atoi(partsA[2])
		numberB, _ := strconv.Atoi(partsB[2])
		return numberA < numberB
	}
	return a < b
}
