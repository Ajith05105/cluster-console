package api

// THE SAFETY PROOF (code half).
//
// This test fills a fake Kubernetes with things in other namespaces that
// look as tempting as possible: same names as the catalog entries, the same
// managed-by=console label. It then uses the console's API, with a valid
// token, both normally and in every underhand way we could think of, and
// checks two things at the end:
//
//  1. every object outside `workload` is still there, unchanged, and
//  2. the fake Kubernetes received not one changing call aimed outside
//     `workload`.
//
// The other half of the proof is rbac_manifest_test.go, which checks that
// the console's permissions would stop it even if this code were wrong.

import (
	"net/http"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"cluster-console/backend/cluster/clustertest"
)

func TestNothingOutsideWorkloadCanBeScaledOrDeleted(t *testing.T) {
	ours := map[string]string{"managed-by": "console", "app": "camera-ingest"}
	three := int32(3)

	// The bait: objects in kube-system and default, named and labelled
	// exactly like the console's own.
	var bait []runtime.Object
	for _, namespace := range []string{"kube-system", "default", "console", "argocd"} {
		bait = append(bait,
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "camera-ingest", Namespace: namespace, Labels: ours},
				Spec: appsv1.DeploymentSpec{Replicas: &three}},
			&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "camera-ingest", Namespace: namespace, Labels: ours}},
			&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: "camera-ingest", Namespace: namespace, Labels: ours},
				Spec: autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 20}},
			&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: namespace, Labels: ours}},
			&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "camera-ingest-" + namespace, Namespace: namespace, Labels: ours}},
		)
	}
	bait = append(bait,
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "coredns-abc", Namespace: "kube-system"}},
		&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "workload-quota", Namespace: "workload"}},
		// One real pod in workload, so there is something legitimate to delete.
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "camera-ingest-ours", Namespace: "workload", Labels: ours}},
	)
	c := newTestConsole(t, Config{Token: testToken}, bait...)

	// --- Part 1: underhand requests. Each must be refused. ----------------
	refused := []struct {
		why  string
		path string
		body string
		want int
	}{
		// Naming a pod that exists only in another namespace.
		{"pod that lives in kube-system", "/api/pod/delete", `{"name":"coredns-abc"}`, http.StatusNotFound},
		{"pod that lives in default", "/api/pod/delete", `{"name":"camera-ingest-default"}`, http.StatusNotFound},
		// Trying to put a namespace into the name.
		{"namespace/name", "/api/pod/delete", `{"name":"kube-system/coredns-abc"}`, http.StatusBadRequest},
		{"path instead of a name", "/api/pod/delete", `{"name":"../../kube-system/pods/coredns-abc"}`, http.StatusBadRequest},
		{"name with a query string", "/api/pod/delete", `{"name":"coredns-abc?namespace=kube-system"}`, http.StatusBadRequest},
		// Trying to add a namespace field. Unknown fields are refused.
		{"extra namespace field on pod delete", "/api/pod/delete", `{"name":"coredns-abc","namespace":"kube-system"}`, http.StatusBadRequest},
		{"extra namespace field on scale", "/api/scale", `{"replicas":0,"namespace":"kube-system"}`, http.StatusBadRequest},
		{"extra name field on scale", "/api/scale", `{"replicas":9,"name":"camera-ingest","namespace":"default"}`, http.StatusBadRequest},
		{"extra namespace field on deploy", "/api/deploy", `{"image_key":"camera-ingest","namespace":"kube-system"}`, http.StatusBadRequest},
		// Nothing is deployed in workload yet. The Deployments called
		// camera-ingest in the other namespaces must not be found instead.
		{"scale when the only Deployments are in other namespaces", "/api/scale", `{"replicas":5}`, http.StatusConflict},
		// Trying to deploy something that is not in the catalog.
		{"image address instead of a key", "/api/deploy", `{"image_key":"docker.io/library/nginx:latest"}`, http.StatusBadRequest},
		{"path as a key", "/api/deploy", `{"image_key":"../../kube-system"}`, http.StatusBadRequest},
		{"YAML smuggled into the CPU field", "/api/deploy", `{"image_key":"camera-ingest","cpu_request":"500m\"\n  namespace: kube-system"}`, http.StatusBadRequest},
		{"YAML smuggled into the memory field", "/api/deploy", `{"image_key":"camera-ingest","memory_request":"128Mi\nnamespace: kube-system"}`, http.StatusBadRequest},
	}
	for _, attempt := range refused {
		if got := c.post(t, attempt.path, testToken, attempt.body); got.Status != attempt.want {
			t.Errorf("%s: POST %s %s gave status %d, want %d", attempt.why, attempt.path, attempt.body, got.Status, attempt.want)
		}
	}
	if count := clustertest.Writes(c.client); count != 0 {
		t.Fatalf("the refused requests still caused %d changes in the cluster", count)
	}

	// --- Part 2: every button, used normally, with query strings that try
	// to redirect it. The buttons work, in workload only. -----------------
	steps := []struct {
		path string
		body string
		// deployed is the catalog key this step deploys, if it is a deploy.
		deployed string
	}{
		{"/api/deploy?namespace=kube-system", `{"image_key":"camera-ingest"}`, "camera-ingest"},
		{"/api/scale?namespace=kube-system&name=camera-ingest", `{"replicas":6}`, ""},
		{"/api/pod/delete?namespace=default", `{"name":"camera-ingest-ours"}`, ""},
		{"/api/deploy", `{"image_key":"whoami","fast_node_death":true}`, "whoami"},
		{"/api/scale", `{"replicas":2}`, ""},
		{"/api/undeploy?namespace=kube-system", `{"namespace":"kube-system"}`, ""},
	}
	for _, step := range steps {
		if got := c.post(t, step.path, testToken, step.body); got.Status != http.StatusOK {
			t.Fatalf("POST %s %s: status %d, error %q; want it to work", step.path, step.body, got.Status, got.Error)
		}
		if step.deployed != "" {
			// Let the watcher catch up so the next step sees the new workload.
			clustertest.Eventually(t, "watcher to see "+step.deployed+" deployed", func() bool {
				state := c.server.snapshot().Cluster
				return state.Workload != nil && state.Workload.Key == step.deployed && state.HPA != nil
			})
		}
	}
	if clustertest.Writes(c.client) == 0 {
		t.Fatal("the normal steps made no changes at all, so this test proves nothing")
	}

	// --- The proof ---------------------------------------------------------
	// 1. Not one changing call was aimed outside workload, and no call of
	//    any kind was made on ResourceQuotas. (Checked first, before this
	//    test makes look-ups of its own below.)
	if outside := clustertest.WritesOutside(c.client, "workload"); len(outside) > 0 {
		t.Errorf("changing calls aimed outside workload: %v", outside)
	}
	for _, action := range c.client.Actions() {
		if action.GetResource().Resource == "resourcequotas" {
			t.Errorf("the console made a %q call on ResourceQuotas; it must never touch them", action.GetVerb())
		}
	}

	// 2. Everything outside workload is still there and unchanged.
	ctx := t.Context()
	get := metav1.GetOptions{}
	for _, namespace := range []string{"kube-system", "default", "console", "argocd"} {
		deployment, err := c.client.AppsV1().Deployments(namespace).Get(ctx, "camera-ingest", get)
		if err != nil {
			t.Errorf("the Deployment in %s was deleted", namespace)
		} else if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 3 {
			t.Errorf("the Deployment in %s was scaled: replicas = %v, want 3", namespace, deployment.Spec.Replicas)
		}
		if _, err := c.client.CoreV1().Services(namespace).Get(ctx, "camera-ingest", get); err != nil {
			t.Errorf("the Service in %s was deleted", namespace)
		}
		if hpa, err := c.client.AutoscalingV2().HorizontalPodAutoscalers(namespace).Get(ctx, "camera-ingest", get); err != nil {
			t.Errorf("the autoscaler in %s was deleted", namespace)
		} else if hpa.Spec.MinReplicas != nil {
			t.Errorf("the autoscaler in %s was changed: minimum = %d", namespace, *hpa.Spec.MinReplicas)
		}
		if _, err := c.client.NetworkingV1().Ingresses(namespace).Get(ctx, "workload", get); err != nil {
			t.Errorf("the route in %s was deleted", namespace)
		}
		if _, err := c.client.CoreV1().Pods(namespace).Get(ctx, "camera-ingest-"+namespace, get); err != nil {
			t.Errorf("the pod in %s was deleted", namespace)
		}
	}
	if _, err := c.client.CoreV1().Pods("kube-system").Get(ctx, "coredns-abc", get); err != nil {
		t.Error("the coredns pod in kube-system was deleted")
	}

	// 3. The ResourceQuota in workload is still there.
	if _, err := c.client.CoreV1().ResourceQuotas("workload").Get(ctx, "workload-quota", get); err != nil {
		t.Error("the ResourceQuota in workload was deleted")
	}
}
