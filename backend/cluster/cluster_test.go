package cluster_test

// Automated tests for watching and acting, run against a fake Kubernetes
// (see clustertest). No real cluster is involved.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"cluster-console/backend/catalog"
	"cluster-console/backend/cluster"
	"cluster-console/backend/cluster/clustertest"
)

var ctx = context.Background()

// managed is the label the console puts on everything it creates.
var managed = map[string]string{"managed-by": "console"}

// wantStatus fails the test unless err is a UserError with the given status.
func wantStatus(t *testing.T, err error, status int, doing string) {
	t.Helper()
	var mistake *cluster.UserError
	if !errors.As(err, &mistake) || mistake.Status != status {
		t.Errorf("%s: got error %v, want a refusal with status %d", doing, err, status)
	}
}

// exists reports whether each of the four workload objects exists for a key.
func exists(client *fake.Clientset, key string) (deployment, service, autoscaler bool) {
	_, err := client.AppsV1().Deployments("workload").Get(ctx, key, metav1.GetOptions{})
	deployment = err == nil
	_, err = client.CoreV1().Services("workload").Get(ctx, key, metav1.GetOptions{})
	service = err == nil
	_, err = client.AutoscalingV2().HorizontalPodAutoscalers("workload").Get(ctx, key, metav1.GetOptions{})
	autoscaler = err == nil
	return
}

// Deploy creates the four objects, in the workload namespace, labelled as
// the console's, and the snapshot then describes them.
func TestDeployCreatesTheWorkload(t *testing.T) {
	cl, client := clustertest.New(t)

	if err := cl.Deploy(ctx, "camera-ingest", catalog.Options{}); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	deployment, err := client.AppsV1().Deployments("workload").Get(ctx, "camera-ingest", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("no Deployment was created: %v", err)
	}
	if deployment.Labels["managed-by"] != "console" {
		t.Error("the Deployment is not labelled managed-by=console")
	}
	if deployment.Spec.Replicas != nil {
		t.Errorf("the Deployment sets replicas to %d; it must be left unset", *deployment.Spec.Replicas)
	}
	container := deployment.Spec.Template.Spec.Containers[0]
	if container.Image != "gitea.cluster.local:3000/apps/camera-ingest:0.1.0" {
		t.Errorf("image = %q", container.Image)
	}
	if got := container.Resources.Requests.Cpu().String(); got != "500m" {
		t.Errorf("CPU request = %s, want 500m", got)
	}
	if _, service, autoscaler := exists(client, "camera-ingest"); !service || !autoscaler {
		t.Errorf("Service created: %v, autoscaler created: %v; want both", service, autoscaler)
	}
	route, err := client.NetworkingV1().Ingresses("workload").Get(ctx, "workload", metav1.GetOptions{})
	if err != nil || route.Spec.Rules[0].Host != "workload.cluster.local" {
		t.Errorf("route missing or wrong host: %v", err)
	}
	if outside := clustertest.WritesOutside(client, "workload"); len(outside) > 0 {
		t.Errorf("Deploy changed things outside workload: %v", outside)
	}

	// The watcher catches up and the snapshot describes what was deployed.
	clustertest.Eventually(t, "snapshot to show the workload", func() bool {
		state := cl.Snapshot()
		return state.Workload != nil && state.HPA != nil
	})
	state := cl.Snapshot()
	if state.Workload.Key != "camera-ingest" || state.Workload.Desired != 1 || state.Workload.CPURequest != "500m" {
		t.Errorf("snapshot workload = %+v", *state.Workload)
	}
	if state.Workload.NodeDeathSeconds != 300 || state.Workload.FastNodeDeath {
		t.Errorf("node-death setting = %d s (fast=%v), want the 300 s baseline", state.Workload.NodeDeathSeconds, state.Workload.FastNodeDeath)
	}
	if state.HPA.Min != 1 || state.HPA.Max != 20 || state.HPA.TargetPercent != 50 {
		t.Errorf("snapshot autoscaler = %+v", *state.HPA)
	}
	if method, path := cl.LoadShape(); method != http.MethodPost || path != "/frame" {
		t.Errorf("load shape = %s %s, want POST /frame", method, path)
	}
}

// The choices from the Deploy form reach the cluster.
func TestDeployAppliesTheFormChoices(t *testing.T) {
	cl, client := clustertest.New(t)

	options := catalog.Options{CPURequest: "250m", MemoryRequest: "200Mi", MaxReplicas: 8, FastNodeDeath: true}
	if err := cl.Deploy(ctx, "camera-ingest", options); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}
	deployment, _ := client.AppsV1().Deployments("workload").Get(ctx, "camera-ingest", metav1.GetOptions{})
	requests := deployment.Spec.Template.Spec.Containers[0].Resources.Requests
	if requests.Cpu().String() != "250m" || requests.Memory().String() != "200Mi" {
		t.Errorf("requests = %s CPU, %s memory; want 250m and 200Mi", requests.Cpu(), requests.Memory())
	}
	for _, toleration := range deployment.Spec.Template.Spec.Tolerations {
		if toleration.TolerationSeconds == nil || *toleration.TolerationSeconds != 20 {
			t.Errorf("toleration %s = %v seconds, want 20 with the fast switch on", toleration.Key, toleration.TolerationSeconds)
		}
	}
	autoscaler, _ := client.AutoscalingV2().HorizontalPodAutoscalers("workload").Get(ctx, "camera-ingest", metav1.GetOptions{})
	if autoscaler.Spec.MaxReplicas != 8 {
		t.Errorf("autoscaler maximum = %d, want 8", autoscaler.Spec.MaxReplicas)
	}
}

// Bad input is refused before anything is created.
func TestDeployRefusesBadInput(t *testing.T) {
	cl, client := clustertest.New(t)

	wantStatus(t, cl.Deploy(ctx, "docker.io/library/anything", catalog.Options{}), http.StatusBadRequest, "an image address instead of a key")
	wantStatus(t, cl.Deploy(ctx, "camera-ingest", catalog.Options{CPURequest: "lots"}), http.StatusBadRequest, "a non-number CPU request")
	wantStatus(t, cl.Deploy(ctx, "camera-ingest", catalog.Options{MaxReplicas: 500}), http.StatusBadRequest, "500 replicas")

	if count := clustertest.Writes(client); count != 0 {
		t.Errorf("%d changes were made to the cluster by refused requests", count)
	}
}

// Deploying a second entry replaces the first, and the load generator is
// told to send the new kind of request.
func TestDeployReplacesWhatWasThere(t *testing.T) {
	cl, client := clustertest.New(t)

	if err := cl.Deploy(ctx, "camera-ingest", catalog.Options{}); err != nil {
		t.Fatalf("first Deploy failed: %v", err)
	}
	clustertest.Eventually(t, "watcher to see camera-ingest", func() bool { return cl.Snapshot().Workload != nil })

	if err := cl.Deploy(ctx, "nginx-alpine", catalog.Options{}); err != nil {
		t.Fatalf("second Deploy failed: %v", err)
	}
	if deployment, service, autoscaler := exists(client, "camera-ingest"); deployment || service || autoscaler {
		t.Errorf("camera-ingest was not fully removed: deployment=%v service=%v autoscaler=%v", deployment, service, autoscaler)
	}
	if deployment, service, autoscaler := exists(client, "nginx-alpine"); !deployment || !service || !autoscaler {
		t.Errorf("nginx-alpine was not fully created: deployment=%v service=%v autoscaler=%v", deployment, service, autoscaler)
	}
	clustertest.Eventually(t, "load shape to become GET /", func() bool {
		method, path := cl.LoadShape()
		return method == http.MethodGet && path == "/"
	})
}

// Remove deletes what the console created and leaves alone anything it did
// not, even an object with the same name as a catalog entry.
func TestUndeployOnlyRemovesWhatTheConsoleCreated(t *testing.T) {
	notOurs := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "whoami", Namespace: "workload"}}
	cl, client := clustertest.New(t, notOurs)

	if err := cl.Deploy(ctx, "camera-ingest", catalog.Options{}); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}
	clustertest.Eventually(t, "watcher to see the workload", func() bool { return cl.Snapshot().Workload != nil })

	deleted, err := cl.Undeploy(ctx)
	if err != nil {
		t.Fatalf("Undeploy failed: %v", err)
	}
	if deleted != 4 {
		t.Errorf("Undeploy deleted %d objects, want 4", deleted)
	}
	if deployment, service, autoscaler := exists(client, "camera-ingest"); deployment || service || autoscaler {
		t.Errorf("left behind: deployment=%v service=%v autoscaler=%v", deployment, service, autoscaler)
	}
	if _, err := client.NetworkingV1().Ingresses("workload").Get(ctx, "workload", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Error("the route was left behind")
	}
	if _, err := client.CoreV1().Services("workload").Get(ctx, "whoami", metav1.GetOptions{}); err != nil {
		t.Error("Undeploy deleted a Service the console did not create")
	}

	// A second Remove finds nothing to do, and says so.
	if deleted, err := cl.Undeploy(ctx); err != nil || deleted != 0 {
		t.Errorf("second Undeploy: deleted %d, error %v; want 0 and no error", deleted, err)
	}
}

// Scale raises the autoscaler's minimum and sets the pod count through the
// Deployment's scale sub-resource.
func TestScale(t *testing.T) {
	cl, client := clustertest.New(t)

	wantStatus(t, cl.Scale(ctx, 3), http.StatusConflict, "scaling with nothing deployed")

	if err := cl.Deploy(ctx, "camera-ingest", catalog.Options{}); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}
	clustertest.Eventually(t, "watcher to see the workload", func() bool {
		state := cl.Snapshot()
		return state.Workload != nil && state.HPA != nil
	})

	wantStatus(t, cl.Scale(ctx, 0), http.StatusBadRequest, "scaling to 0")
	wantStatus(t, cl.Scale(ctx, -3), http.StatusBadRequest, "scaling to -3")
	wantStatus(t, cl.Scale(ctx, 21), http.StatusBadRequest, "scaling above the autoscaler maximum of 20")

	client.ClearActions()
	if err := cl.Scale(ctx, 5); err != nil {
		t.Fatalf("Scale(5) failed: %v", err)
	}

	autoscaler, err := client.AutoscalingV2().HorizontalPodAutoscalers("workload").Get(ctx, "camera-ingest", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the autoscaler is gone after Scale: %v", err)
	}
	if autoscaler.Spec.MinReplicas == nil || *autoscaler.Spec.MinReplicas != 5 {
		t.Errorf("autoscaler minimum = %v, want 5", autoscaler.Spec.MinReplicas)
	}
	if autoscaler.Spec.MaxReplicas != 20 || autoscaler.Labels["managed-by"] != "console" {
		t.Errorf("the replacement autoscaler lost its maximum or label: max=%d labels=%v", autoscaler.Spec.MaxReplicas, autoscaler.Labels)
	}

	// The pod count must have been set with a patch to deployments/scale in
	// the workload namespace, which is the one narrow permission we have.
	scaled := false
	for _, action := range client.Actions() {
		if action.GetVerb() == "patch" && action.GetResource().Resource == "deployments" &&
			action.GetSubresource() == "scale" && action.GetNamespace() == "workload" {
			scaled = true
		}
		if action.GetVerb() == "update" {
			t.Errorf("Scale used an update on %s, which the console has no permission for", action.GetResource().Resource)
		}
	}
	if !scaled {
		t.Error("Scale did not patch deployments/scale in the workload namespace")
	}
	if outside := clustertest.WritesOutside(client, "workload"); len(outside) > 0 {
		t.Errorf("Scale changed things outside workload: %v", outside)
	}
}

// Delete-a-pod works for a pod in workload and for nothing else.
func TestDeletePod(t *testing.T) {
	ours := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "camera-ingest-abc", Namespace: "workload"}}
	theirs := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "coredns-xyz", Namespace: "kube-system"}}
	cl, client := clustertest.New(t, ours, theirs)

	wantStatus(t, cl.DeletePod(ctx, "coredns-xyz"), http.StatusNotFound, "a pod that exists only in kube-system")
	wantStatus(t, cl.DeletePod(ctx, "kube-system/coredns-xyz"), http.StatusBadRequest, "a name with a namespace in front")
	wantStatus(t, cl.DeletePod(ctx, "../kube-system/pods/coredns-xyz"), http.StatusBadRequest, "a path instead of a name")
	wantStatus(t, cl.DeletePod(ctx, ""), http.StatusBadRequest, "an empty name")
	if _, err := client.CoreV1().Pods("kube-system").Get(ctx, "coredns-xyz", metav1.GetOptions{}); err != nil {
		t.Fatal("the kube-system pod was deleted")
	}
	if count := clustertest.Writes(client); count != 0 {
		t.Errorf("%d changes were made by refused requests", count)
	}

	if err := cl.DeletePod(ctx, "camera-ingest-abc"); err != nil {
		t.Fatalf("deleting a workload pod failed: %v", err)
	}
	if _, err := client.CoreV1().Pods("workload").Get(ctx, "camera-ingest-abc", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Error("the workload pod was not deleted")
	}
	if outside := clustertest.WritesOutside(client, "workload"); len(outside) > 0 {
		t.Errorf("DeletePod changed things outside workload: %v", outside)
	}
}

// node builds a fake node for the snapshot test.
func node(name string, controlPlane, ready bool) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}}}
	if controlPlane {
		n.Labels["node-role.kubernetes.io/control-plane"] = "true"
	}
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status, LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Minute))}}
	n.Status.Allocatable = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}
	return n
}

// The snapshot sorts nodes into pools, puts pods under their nodes, and
// explains why a Pending pod is waiting.
func TestSnapshot(t *testing.T) {
	running := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "camera-ingest-1", Namespace: "workload", Labels: managed},
		Spec:       corev1.PodSpec{NodeName: "agent-7"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	// The scheduler's message is copied from the real cluster.
	waiting := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "camera-ingest-2", Namespace: "workload", Labels: managed},
		Status: corev1.PodStatus{Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
				LastTransitionTime: metav1.NewTime(time.Now().Add(-30 * time.Second)),
				Message: "0/6 nodes are available: 1 node(s) had untolerated taint(s), 2 Insufficient cpu, " +
					"3 node(s) didn't match Pod's node affinity/selector. no new claims to deallocate, preemption: " +
					"0/6 nodes are available: 2 No preemption victims found for incoming pod, 4 Preemption is not helpful for scheduling."}}},
	}
	// A pod in another namespace must never appear.
	elsewhere := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "coredns-xyz", Namespace: "kube-system"}, Spec: corev1.PodSpec{NodeName: "server-1"}}

	cl, _ := clustertest.New(t, running, waiting, elsewhere,
		node("server-1", true, true), node("agent-10", false, true), node("agent-7", false, false), node("mac-mini-agent", false, false))
	clustertest.Eventually(t, "watchers to load", func() bool {
		state := cl.Snapshot()
		return len(state.Nodes) == 4 && len(state.Pending) == 1
	})
	state := cl.Snapshot()

	// Order: test pool first (agent-7 before agent-10), then excluded, then
	// control-plane.
	var names, pools []string
	for _, n := range state.Nodes {
		names = append(names, n.Name)
		pools = append(pools, n.Pool)
	}
	wantNames := []string{"agent-7", "agent-10", "mac-mini-agent", "server-1"}
	wantPools := []string{"test", "test", "excluded", "control-plane"}
	for i := range wantNames {
		if names[i] != wantNames[i] || pools[i] != wantPools[i] {
			t.Fatalf("nodes = %v with pools %v; want %v with pools %v", names, pools, wantNames, wantPools)
		}
	}

	agent7 := state.Nodes[0]
	if agent7.Ready || agent7.NotReadySince == nil || agent7.CPUMillis != 4000 || agent7.Role != "worker" {
		t.Errorf("agent-7 = %+v; want not ready, with a since-time, 4000 millicores, role worker", agent7)
	}
	if len(agent7.Pods) != 1 || agent7.Pods[0].Name != "camera-ingest-1" || !agent7.Pods[0].Ready {
		t.Errorf("agent-7 pods = %+v; want the one ready pod", agent7.Pods)
	}
	if server := state.Nodes[3]; len(server.Pods) != 0 {
		t.Errorf("server-1 shows pods %+v; pods from other namespaces must not appear", server.Pods)
	}

	pending := state.Pending[0]
	if pending.Name != "camera-ingest-2" || !pending.Unschedulable || pending.PendingReason != "Insufficient cpu" || pending.PendingSince == nil {
		t.Errorf("pending pod = %+v; want unschedulable with reason \"Insufficient cpu\"", pending)
	}
	if !state.Sources.Workload || !state.Sources.Nodes || state.Sources.Argo {
		t.Errorf("sources = %+v; want workload and nodes readable, Argo not watched in this test", state.Sources)
	}
}

// What the watchers see is written to the event log.
func TestChangesAreReported(t *testing.T) {
	cl, client := clustertest.New(t, node("agent-7", false, true))
	lines := make(chan string, 100)
	cl.Emit = func(kind, message string) { lines <- message }

	expect := func(want string) {
		t.Helper()
		timeout := time.After(3 * time.Second)
		for {
			select {
			case got := <-lines:
				if got == want {
					return
				}
			case <-timeout:
				t.Fatalf("no event-log line %q", want)
			}
		}
	}

	// A node joins.
	_, _ = client.CoreV1().Nodes().Create(ctx, node("agent-11", false, true), metav1.CreateOptions{})
	expect("node agent-11 joined the cluster")

	// A node goes NotReady.
	_, _ = client.CoreV1().Nodes().Update(ctx, node("agent-7", false, false), metav1.UpdateOptions{})
	expect("node agent-7 is NotReady")

	// A pod is created, scheduled and becomes ready.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "camera-ingest-1", Namespace: "workload"}}
	_, _ = client.CoreV1().Pods("workload").Create(ctx, pod, metav1.CreateOptions{})
	expect("pod camera-ingest-1 created")
	pod.Spec.NodeName = "agent-11"
	_, _ = client.CoreV1().Pods("workload").Update(ctx, pod, metav1.UpdateOptions{})
	expect("pod camera-ingest-1 scheduled on agent-11")
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	_, _ = client.CoreV1().Pods("workload").Update(ctx, pod, metav1.UpdateOptions{})
	expect("pod camera-ingest-1 ready on agent-11")

	// The autoscaler changes its mind.
	hpa := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: "camera-ingest", Namespace: "workload", Labels: managed}}
	_, _ = client.AutoscalingV2().HorizontalPodAutoscalers("workload").Create(ctx, hpa, metav1.CreateOptions{})
	hpa.Status.DesiredReplicas = 6
	_, _ = client.AutoscalingV2().HorizontalPodAutoscalers("workload").Update(ctx, hpa, metav1.UpdateOptions{})
	expect("autoscaler wants 6 pods, was 0 (CPU not measured yet)")

	// A Deployment's wanted pod count changes.
	one, four := int32(1), int32(4)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "camera-ingest", Namespace: "workload", Labels: managed},
		Spec: appsv1.DeploymentSpec{Replicas: &one}}
	_, _ = client.AppsV1().Deployments("workload").Create(ctx, deployment, metav1.CreateOptions{})
	deployment.Spec.Replicas = &four
	_, _ = client.AppsV1().Deployments("workload").Update(ctx, deployment, metav1.UpdateOptions{})
	expect("camera-ingest: pods wanted changed from 1 to 4")

	// Kubernetes' own warnings are passed on, once each, except the routine
	// autoscaler ones.
	warning := func(name, reason, message string) *corev1.Event {
		return &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "workload", UID: "uid-" + types.UID(name)},
			Type: corev1.EventTypeWarning, Reason: reason, Message: message,
			InvolvedObject: corev1.ObjectReference{Name: "camera-ingest-2"}}
	}
	events := client.CoreV1().Events("workload")
	_, _ = events.Create(ctx, warning("routine", "FailedGetResourceMetric", "no metrics yet"), metav1.CreateOptions{})
	failed := warning("real", "FailedScheduling", "0/6 nodes are available: 2 Insufficient cpu.")
	_, _ = events.Create(ctx, failed, metav1.CreateOptions{})
	failed.Count = 2 // Kubernetes repeats the same event with a higher count
	_, _ = events.Update(ctx, failed, metav1.UpdateOptions{})
	expect("Kubernetes warning: FailedScheduling on camera-ingest-2: 0/6 nodes are available: 2 Insufficient cpu.")

	// A pod goes away.
	_ = client.CoreV1().Pods("workload").Delete(ctx, "camera-ingest-1", metav1.DeleteOptions{})
	expect("pod camera-ingest-1 gone on agent-11")

	// Nothing is left over: the routine warning was dropped, and the
	// repeated FailedScheduling was reported only once.
	time.Sleep(100 * time.Millisecond)
	for {
		select {
		case extra := <-lines:
			if strings.Contains(extra, "FailedGetResourceMetric") || strings.Contains(extra, "FailedScheduling") {
				t.Errorf("unexpected extra event-log line: %q", extra)
			}
			continue
		default:
		}
		break
	}
}
