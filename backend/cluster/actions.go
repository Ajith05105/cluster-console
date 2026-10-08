package cluster

// This file is everything the console can DO to the cluster:
//
//	Deploy     create the workload's four objects from a catalog entry
//	Undeploy   delete the objects the console created
//	Scale      set how many pods the workload should have at least
//	DeletePod  delete one pod (to show it being replaced)
//
// All four act only in the `workload` namespace (c.namespace), and only on
// objects labelled managed-by=console, apart from DeletePod which acts on
// any pod in that namespace.

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"

	"cluster-console/backend/catalog"
)

// routeName is the name of the Traefik route (Ingress) the templates create.
// It is the same whichever catalog entry is deployed.
const routeName = "workload"

// UserError is a mistake in what was asked for (as opposed to something
// going wrong in the cluster). Status is the HTTP status the API replies
// with, for example 400 for a bad value or 404 for "no such pod".
type UserError struct {
	Status  int
	Message string
}

func (e *UserError) Error() string { return e.Message }

// userError builds a UserError.
func userError(status int, format string, args ...any) error {
	return &UserError{Status: status, Message: fmt.Sprintf(format, args...)}
}

// Deploy makes the cluster run the catalog entry `key` with the given
// choices. Anything the console deployed before is removed first, so Deploy
// always leaves exactly one workload.
func (c *Cluster) Deploy(ctx context.Context, key string, options catalog.Options) error {
	// Turn the catalog entry into YAML. This is also where the key and the
	// numbers are checked; a failure here is the caller's mistake.
	yaml, err := c.catalog.Render(key, options)
	if err != nil {
		return userError(http.StatusBadRequest, "%v", err)
	}

	// Turn the YAML into Kubernetes objects. The YAML is several documents
	// separated by lines of three dashes.
	var (
		deployment *appsv1.Deployment
		service    *corev1.Service
		autoscaler *autoscalingv2.HorizontalPodAutoscaler
		route      *networkingv1.Ingress
	)
	decoder := scheme.Codecs.UniversalDeserializer()
	for _, document := range strings.Split(yaml, "\n---\n") {
		object, _, err := decoder.Decode([]byte(document), nil, nil)
		if err != nil {
			return fmt.Errorf("the template produced YAML Kubernetes cannot read: %w", err)
		}

		// Belt and braces: refuse anything that is not aimed at the
		// workload namespace or not labelled as ours, whatever the
		// templates say.
		if !isOurs(object, c.namespace) {
			return fmt.Errorf("the template produced an object outside the %s namespace or without the %s label; refusing to create it",
				c.namespace, catalog.ManagedByLabel)
		}

		switch typed := object.(type) {
		case *appsv1.Deployment:
			deployment = typed
		case *corev1.Service:
			service = typed
		case *autoscalingv2.HorizontalPodAutoscaler:
			autoscaler = typed
		case *networkingv1.Ingress:
			route = typed
		default:
			return fmt.Errorf("the template produced an unexpected kind of object (%T); refusing to create it", object)
		}
	}
	if deployment == nil || service == nil || autoscaler == nil || route == nil {
		return fmt.Errorf("the template did not produce all four objects")
	}

	// Clear away whatever the console deployed before.
	if _, err := c.Undeploy(ctx); err != nil {
		return err
	}

	// Create the four objects. The order does not matter to Kubernetes;
	// this one makes the pods appear last, once their Service exists.
	create := metav1.CreateOptions{}
	steps := []struct {
		what string
		do   func() error
	}{
		{"Service", func() error {
			_, err := c.client.CoreV1().Services(c.namespace).Create(ctx, service, create)
			return err
		}},
		{"route", func() error {
			_, err := c.client.NetworkingV1().Ingresses(c.namespace).Create(ctx, route, create)
			return err
		}},
		{"Deployment", func() error {
			_, err := c.client.AppsV1().Deployments(c.namespace).Create(ctx, deployment, create)
			return err
		}},
		{"autoscaler", func() error {
			_, err := c.client.AutoscalingV2().HorizontalPodAutoscalers(c.namespace).Create(ctx, autoscaler, create)
			return err
		}},
	}
	for _, step := range steps {
		if err := createWithRetry(ctx, step.do); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return userError(http.StatusConflict,
					"a %s with that name already exists in %s and was not created by the console; remove it by hand first", step.what, c.namespace)
			}
			return fmt.Errorf("creating the %s: %w", step.what, err)
		}
	}
	return nil
}

// isOurs reports whether something decoded from a template is safe to
// create: it must be a Kubernetes object, aimed at the given namespace, and
// carry the managed-by=console label.
func isOurs(object any, namespace string) bool {
	meta, isObject := object.(metav1.Object)
	return isObject && meta.GetNamespace() == namespace && meta.GetLabels()[catalog.ManagedByLabel] == catalog.ManagedByValue
}

// Undeploy deletes the workload objects the console created, and returns how
// many it deleted. Objects without the managed-by=console label are left
// alone, even if they have the same name.
func (c *Cluster) Undeploy(ctx context.Context) (int, error) {
	// The console may only look up Services and routes by exact name (it
	// has no permission to list them), so work out the names to try: every
	// catalog key, plus the name of anything we can see is deployed.
	names := map[string]bool{}
	for _, item := range c.catalog.Items {
		names[item.Key] = true
	}
	deployments, _ := c.deploymentLister.List(managedSelector)
	for _, deployment := range deployments {
		names[deployment.Name] = true
	}

	// For each kind of object: how to fetch one by name and how to delete it.
	type kind struct {
		what   string
		get    func(name string) (metav1.Object, error)
		delete func(name string) error
	}
	get := metav1.GetOptions{}
	// "Background" means: delete the object now and let Kubernetes clean up
	// what belonged to it (a Deployment's pods) afterwards.
	background := metav1.DeletePropagationBackground
	del := metav1.DeleteOptions{PropagationPolicy: &background}

	kinds := []kind{
		{"autoscaler",
			func(name string) (metav1.Object, error) {
				return c.client.AutoscalingV2().HorizontalPodAutoscalers(c.namespace).Get(ctx, name, get)
			},
			func(name string) error {
				return c.client.AutoscalingV2().HorizontalPodAutoscalers(c.namespace).Delete(ctx, name, del)
			}},
		{"Deployment",
			func(name string) (metav1.Object, error) {
				return c.client.AppsV1().Deployments(c.namespace).Get(ctx, name, get)
			},
			func(name string) error {
				return c.client.AppsV1().Deployments(c.namespace).Delete(ctx, name, del)
			}},
		{"Service",
			func(name string) (metav1.Object, error) {
				return c.client.CoreV1().Services(c.namespace).Get(ctx, name, get)
			},
			func(name string) error {
				return c.client.CoreV1().Services(c.namespace).Delete(ctx, name, del)
			}},
	}
	route := kind{"route",
		func(name string) (metav1.Object, error) {
			return c.client.NetworkingV1().Ingresses(c.namespace).Get(ctx, name, get)
		},
		func(name string) error {
			return c.client.NetworkingV1().Ingresses(c.namespace).Delete(ctx, name, del)
		}}

	// deleteIfOurs deletes one object if it exists and carries our label.
	deleted := 0
	deleteIfOurs := func(k kind, name string) error {
		object, err := k.get(name)
		if apierrors.IsNotFound(err) {
			return nil // nothing there: fine
		}
		if err != nil {
			return fmt.Errorf("looking up the %s %q: %w", k.what, name, err)
		}
		if object.GetLabels()[catalog.ManagedByLabel] != catalog.ManagedByValue {
			return nil // not created by the console: leave it alone
		}
		if err := k.delete(name); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting the %s %q: %w", k.what, name, err)
		}
		deleted++
		return nil
	}

	if err := deleteIfOurs(route, routeName); err != nil {
		return deleted, err
	}
	for name := range names {
		for _, k := range kinds {
			if err := deleteIfOurs(k, name); err != nil {
				return deleted, err
			}
		}
	}
	return deleted, nil
}

// Scale sets the number of pods the workload should have AT LEAST.
//
// Why "at least": the autoscaler also controls the pod count, and it would
// undo a plain change within a minute. So Scale does two things. It raises
// or lowers the autoscaler's minimum to the number asked for, and it sets
// the Deployment to that number straight away. The autoscaler can still add
// pods above the minimum when load calls for it.
//
// The console is not allowed to edit an autoscaler in place, only to create
// and delete one, so changing the minimum means replacing the autoscaler
// with a copy that differs only in that number.
func (c *Cluster) Scale(ctx context.Context, replicas int) error {
	deployments, _ := c.deploymentLister.List(managedSelector)
	if len(deployments) == 0 {
		return userError(http.StatusConflict, "nothing is deployed, so there is nothing to scale")
	}
	deployment := deployments[0]

	if replicas < 1 {
		return userError(http.StatusBadRequest, "replicas must be at least 1; use Remove to take the workload away")
	}
	if replicas > catalog.MaxReplicasCeiling {
		return userError(http.StatusBadRequest, "replicas must be at most %d", catalog.MaxReplicasCeiling)
	}

	// The autoscaler, if there is one for this Deployment.
	autoscalers := c.client.AutoscalingV2().HorizontalPodAutoscalers(c.namespace)
	current, err := c.hpaLister.HorizontalPodAutoscalers(c.namespace).Get(deployment.Name)
	if err == nil && current.Labels[catalog.ManagedByLabel] == catalog.ManagedByValue {
		if replicas > int(current.Spec.MaxReplicas) {
			return userError(http.StatusBadRequest,
				"%d is above the autoscaler's maximum of %d; deploy again with a higher maximum", replicas, current.Spec.MaxReplicas)
		}
		if current.Spec.MinReplicas == nil || int(*current.Spec.MinReplicas) != replicas {
			// Build the replacement: same name, labels and settings, new
			// minimum.
			minimum := int32(replicas)
			replacement := &autoscalingv2.HorizontalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: current.Name, Namespace: c.namespace, Labels: current.Labels},
				Spec:       *current.Spec.DeepCopy(),
			}
			replacement.Spec.MinReplicas = &minimum

			if err := autoscalers.Delete(ctx, current.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("replacing the autoscaler: %w", err)
			}
			err := createWithRetry(ctx, func() error {
				_, err := autoscalers.Create(ctx, replacement, metav1.CreateOptions{})
				return err
			})
			if err != nil {
				return fmt.Errorf("replacing the autoscaler: %w", err)
			}
		}
	}

	// Set the Deployment's pod count now. This goes through the Deployment's
	// "scale" sub-resource, which is the narrow permission the console has:
	// it can change the pod count but nothing else about the Deployment.
	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))
	_, err = c.client.AppsV1().Deployments(c.namespace).Patch(ctx, deployment.Name, types.MergePatchType, patch, metav1.PatchOptions{}, "scale")
	if err != nil {
		return fmt.Errorf("scaling %s: %w", deployment.Name, err)
	}
	return nil
}

// podNamePattern is what a pod name may look like. Checking it first means
// odd input (slashes, dots-and-slashes, spaces) is refused before it is used
// for anything.
var podNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// DeletePod deletes one pod in the workload namespace. Kubernetes then
// starts a replacement, which is the point of the button.
func (c *Cluster) DeletePod(ctx context.Context, name string) error {
	if !podNamePattern.MatchString(name) {
		return userError(http.StatusBadRequest, "that is not a pod name")
	}
	// Look the pod up in our local copy of the workload namespace. A pod
	// anywhere else is simply not in that copy, so it cannot be named here.
	if _, err := c.podLister.Pods(c.namespace).Get(name); err != nil {
		return userError(http.StatusNotFound, "there is no pod called %q in the %s namespace", name, c.namespace)
	}
	err := c.client.CoreV1().Pods(c.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return userError(http.StatusNotFound, "pod %q has already gone", name)
	}
	if err != nil {
		return fmt.Errorf("deleting pod %s: %w", name, err)
	}
	return nil
}

// createWithRetry runs a create step, trying again for a few seconds if
// Kubernetes says the name is still taken. That happens for a moment when an
// object of the same name has just been deleted.
func createWithRetry(ctx context.Context, create func() error) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		if err = create(); !apierrors.IsAlreadyExists(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(250 * time.Millisecond):
		}
	}
	return err
}
