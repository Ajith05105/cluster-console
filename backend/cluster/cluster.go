// Package cluster is the console's connection to Kubernetes. It does two
// jobs:
//
//  1. WATCHING. It keeps an always-up-to-date local copy of the things the
//     console shows: nodes, the workload's pods, its Deployment and
//     autoscaler, and Argo CD's applications. Kubernetes pushes every change
//     to us as it happens (a "watch"), so nothing here polls on a timer.
//     Each change also becomes a line in the event log where it is useful
//     for the demo ("node agent-7 is NotReady", "pod ... ready on agent-10").
//
//  2. ACTING. Deploy, Remove, scale and delete-a-pod (see actions.go).
//
// SAFETY: every action is aimed at one namespace, `workload`, which is fixed
// in the code (catalog.Namespace). No function in this package accepts a
// namespace from its caller, so no request to the console can point an
// action anywhere else.
package cluster

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	appslisters "k8s.io/client-go/listers/apps/v1"
	autoscalinglisters "k8s.io/client-go/listers/autoscaling/v2"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"cluster-console/backend/catalog"
)

// argoApplications identifies Argo CD's Application objects to Kubernetes.
var argoApplications = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// Cluster is the connection. Make one with New, set OnChange and Emit, then
// call Start.
type Cluster struct {
	client    kubernetes.Interface // for the built-in kinds (pods, nodes...)
	dynamic   dynamic.Interface    // for Argo CD's kind; nil means "do not watch Argo CD"
	catalog   *catalog.Catalog
	namespace string // always catalog.Namespace; see SAFETY above

	// OnChange is called whenever anything we watch changes. The console
	// uses it to know when to send the browser a fresh snapshot.
	OnChange func()
	// Emit is called with a line for the event log.
	Emit func(kind, message string)

	// An "informer" is client-go's name for a watcher that keeps a local
	// copy of one kind of object. A "lister" reads from that copy.
	workloadInformers informers.SharedInformerFactory
	nodeInformers     informers.SharedInformerFactory
	argoInformers     dynamicinformer.DynamicSharedInformerFactory

	podInformer        cache.SharedIndexInformer
	deploymentInformer cache.SharedIndexInformer
	hpaInformer        cache.SharedIndexInformer
	eventInformer      cache.SharedIndexInformer
	nodeInformer       cache.SharedIndexInformer
	argoInformer       cache.SharedIndexInformer // nil if dynamic is nil

	podLister        corelisters.PodLister
	deploymentLister appslisters.DeploymentLister
	hpaLister        autoscalinglisters.HorizontalPodAutoscalerLister
	nodeLister       corelisters.NodeLister

	// shape is what kind of request the load generator should send to
	// whatever is deployed right now. It is read for every frame, so it is
	// kept here ready-made instead of being worked out each time.
	shape atomic.Pointer[loadShape]

	// mu guards the two maps below.
	mu sync.Mutex
	// reportedEvents remembers which Kubernetes warning events have already
	// been put in the event log, so each appears once.
	reportedEvents map[string]bool
	// watchProblems remembers the last problem logged for each watch, so
	// the same message is not logged over and over.
	watchProblems map[string]time.Time
}

// loadShape is the method and path the load generator should use.
type loadShape struct {
	method string
	path   string
}

// New prepares the connection. Nothing is contacted until Start.
//
// argoNamespace is where Argo CD's applications live (read-only). dynamicClient
// may be nil, in which case Argo CD is not watched.
func New(client kubernetes.Interface, dynamicClient dynamic.Interface, cat *catalog.Catalog, argoNamespace string) *Cluster {
	c := &Cluster{
		client:         client,
		dynamic:        dynamicClient,
		catalog:        cat,
		namespace:      catalog.Namespace,
		OnChange:       func() {},
		Emit:           func(string, string) {},
		reportedEvents: map[string]bool{},
		watchProblems:  map[string]time.Time{},
	}
	c.setShape("")

	// Watchers for the workload namespace only. WithNamespace is what
	// limits them: they never ask about pods anywhere else.
	c.workloadInformers = informers.NewSharedInformerFactoryWithOptions(client, 0, informers.WithNamespace(c.namespace))
	pods := c.workloadInformers.Core().V1().Pods()
	deployments := c.workloadInformers.Apps().V1().Deployments()
	autoscalers := c.workloadInformers.Autoscaling().V2().HorizontalPodAutoscalers()
	events := c.workloadInformers.Core().V1().Events()
	c.podInformer, c.podLister = pods.Informer(), pods.Lister()
	c.deploymentInformer, c.deploymentLister = deployments.Informer(), deployments.Lister()
	c.hpaInformer, c.hpaLister = autoscalers.Informer(), autoscalers.Lister()
	c.eventInformer = events.Informer()

	// Nodes do not belong to any namespace, so they get their own watcher.
	c.nodeInformers = informers.NewSharedInformerFactory(client, 0)
	nodes := c.nodeInformers.Core().V1().Nodes()
	c.nodeInformer, c.nodeLister = nodes.Informer(), nodes.Lister()

	// Argo CD applications, read-only, in Argo CD's namespace.
	if dynamicClient != nil {
		c.argoInformers = dynamicinformer.NewFilteredDynamicSharedInformerFactory(dynamicClient, 0, argoNamespace, nil)
		c.argoInformer = c.argoInformers.ForResource(argoApplications).Informer()
	}

	c.registerHandlers()
	return c
}

// Start connects the watchers and waits a short while for the first full
// copy of the workload namespace to arrive. It returns once that is in, or
// after 20 seconds if it is not (the watchers keep trying in the background
// either way, so the console can start even if the cluster is slow).
func (c *Cluster) Start(ctx context.Context) {
	c.workloadInformers.Start(ctx.Done())
	c.nodeInformers.Start(ctx.Done())
	if c.argoInformers != nil {
		c.argoInformers.Start(ctx.Done())
	}

	wait, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if !cache.WaitForCacheSync(wait.Done(), c.podInformer.HasSynced, c.deploymentInformer.HasSynced, c.hpaInformer.HasSynced) {
		log.Print("cluster: still waiting for the first copy of the workload namespace; carrying on")
	}
	c.refreshShape()
}

// LoadShape says what kind of request the load generator should send to the
// workload that is deployed right now.
func (c *Cluster) LoadShape() (method, path string) {
	shape := c.shape.Load()
	return shape.method, shape.path
}

// setShape records the request shape for the given catalog key. An unknown
// or empty key (nothing deployed) falls back to the catalog's default entry.
func (c *Cluster) setShape(key string) {
	item, found := c.catalog.Find(key)
	if !found {
		item, found = c.catalog.Find(c.catalog.Default)
	}
	if !found {
		c.shape.Store(&loadShape{method: http.MethodPost, path: "/frame"})
		return
	}
	c.shape.Store(&loadShape{method: item.Load.Method, path: item.Load.Path})
}

// refreshShape looks at what is deployed and updates the request shape.
func (c *Cluster) refreshShape() {
	deployments, _ := c.deploymentLister.List(managedSelector)
	if len(deployments) == 0 {
		c.setShape("")
		return
	}
	c.setShape(deployments[0].Labels["app"])
}

// registerHandlers tells each watcher what to do when something changes.
// Every handler ends by calling OnChange. Most also compare the object
// before and after, and write a line to the event log when something worth a
// timestamp happened.
//
// The `initial` flag on the add handlers is true for objects that already
// existed when the console started. Those are not news, so they are not
// logged.
func (c *Cluster) registerHandlers() {
	// --- Pods -------------------------------------------------------------
	c.watch("pods", c.podInformer, cache.ResourceEventHandlerDetailedFuncs{
		AddFunc: func(object any, initial bool) {
			if pod, ok := object.(*corev1.Pod); ok && !initial {
				c.Emit("cluster", fmt.Sprintf("pod %s created", pod.Name))
			}
			c.OnChange()
		},
		UpdateFunc: func(before, after any) {
			was, okWas := before.(*corev1.Pod)
			now, okNow := after.(*corev1.Pod)
			if okWas && okNow {
				c.podChanged(was, now)
			}
			c.OnChange()
		},
		DeleteFunc: func(object any) {
			if pod, ok := removed[*corev1.Pod](object); ok {
				c.Emit("cluster", fmt.Sprintf("pod %s gone%s", pod.Name, onNode(pod.Spec.NodeName)))
			}
			c.OnChange()
		},
	})

	// --- Nodes ------------------------------------------------------------
	c.watch("nodes", c.nodeInformer, cache.ResourceEventHandlerDetailedFuncs{
		AddFunc: func(object any, initial bool) {
			if node, ok := object.(*corev1.Node); ok && !initial {
				c.Emit("cluster", fmt.Sprintf("node %s joined the cluster", node.Name))
			}
			c.OnChange()
		},
		UpdateFunc: func(before, after any) {
			was, okWas := before.(*corev1.Node)
			now, okNow := after.(*corev1.Node)
			if okWas && okNow && nodeIsReady(was) != nodeIsReady(now) {
				if nodeIsReady(now) {
					c.Emit("cluster", fmt.Sprintf("node %s is Ready", now.Name))
				} else {
					c.Emit("cluster", fmt.Sprintf("node %s is NotReady", now.Name))
				}
			}
			c.OnChange()
		},
		DeleteFunc: func(object any) {
			if node, ok := removed[*corev1.Node](object); ok {
				c.Emit("cluster", fmt.Sprintf("node %s removed from the cluster", node.Name))
			}
			c.OnChange()
		},
	})

	// --- Deployments ------------------------------------------------------
	c.watch("deployments", c.deploymentInformer, cache.ResourceEventHandlerDetailedFuncs{
		AddFunc: func(any, bool) { c.refreshShape(); c.OnChange() },
		UpdateFunc: func(before, after any) {
			was, okWas := before.(*appsv1.Deployment)
			now, okNow := after.(*appsv1.Deployment)
			if okWas && okNow {
				if wanted(was) != wanted(now) {
					c.Emit("cluster", fmt.Sprintf("%s: pods wanted changed from %d to %d", now.Name, wanted(was), wanted(now)))
				}
				if was.Status.ReadyReplicas != now.Status.ReadyReplicas {
					c.Emit("cluster", fmt.Sprintf("%s: %d of %d pods ready", now.Name, now.Status.ReadyReplicas, wanted(now)))
				}
			}
			c.refreshShape()
			c.OnChange()
		},
		DeleteFunc: func(any) { c.refreshShape(); c.OnChange() },
	})

	// --- Autoscalers ------------------------------------------------------
	c.watch("autoscalers", c.hpaInformer, cache.ResourceEventHandlerDetailedFuncs{
		AddFunc: func(any, bool) { c.OnChange() },
		UpdateFunc: func(before, after any) {
			was, okWas := before.(*autoscalingv2.HorizontalPodAutoscaler)
			now, okNow := after.(*autoscalingv2.HorizontalPodAutoscaler)
			if okWas && okNow && was.Status.DesiredReplicas != now.Status.DesiredReplicas {
				cpu := "CPU not measured yet"
				if described := describeAutoscaler(now); described.CPUPercent != nil {
					cpu = fmt.Sprintf("CPU at %d%% of request, target %d%%", *described.CPUPercent, described.TargetPercent)
				}
				c.Emit("cluster", fmt.Sprintf("autoscaler wants %d pods, was %d (%s)",
					now.Status.DesiredReplicas, was.Status.DesiredReplicas, cpu))
			}
			c.OnChange()
		},
		DeleteFunc: func(any) { c.OnChange() },
	})

	// --- Kubernetes' own warning events in the workload namespace ---------
	// These are things like "FailedScheduling" or "exceeded quota". Each is
	// passed on to the event log once.
	c.watch("events", c.eventInformer, cache.ResourceEventHandlerDetailedFuncs{
		AddFunc: func(object any, initial bool) {
			if event, ok := object.(*corev1.Event); ok && !initial {
				c.kubernetesEvent(event)
			}
		},
		UpdateFunc: func(_, after any) {
			if event, ok := after.(*corev1.Event); ok {
				c.kubernetesEvent(event)
			}
		},
	})

	// --- Argo CD applications ---------------------------------------------
	if c.argoInformer != nil {
		c.watch("Argo CD applications", c.argoInformer, cache.ResourceEventHandlerDetailedFuncs{
			AddFunc: func(any, bool) { c.OnChange() },
			UpdateFunc: func(before, after any) {
				was, okWas := before.(*unstructured.Unstructured)
				now, okNow := after.(*unstructured.Unstructured)
				if okWas && okNow {
					if a, b := describeArgoApp(was), describeArgoApp(now); a.Sync != b.Sync {
						c.Emit("cluster", fmt.Sprintf("Argo CD app %s is %s (was %s)", b.Name, b.Sync, a.Sync))
					}
				}
				c.OnChange()
			},
			DeleteFunc: func(any) { c.OnChange() },
		})
	}
}

// watch attaches the handlers to one watcher, and arranges for connection
// problems (most often: the console has not been given permission) to be
// logged in plain words instead of failing silently.
func (c *Cluster) watch(what string, informer cache.SharedIndexInformer, handlers cache.ResourceEventHandlerDetailedFuncs) {
	_ = informer.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		c.mu.Lock()
		last := c.watchProblems[what]
		worthLogging := time.Since(last) > time.Minute
		if worthLogging {
			c.watchProblems[what] = time.Now()
		}
		c.mu.Unlock()
		if worthLogging {
			log.Printf("cluster: cannot watch %s: %v", what, err)
		}
	})
	if _, err := informer.AddEventHandler(handlers); err != nil {
		log.Printf("cluster: could not attach handlers for %s: %v", what, err)
	}
}

// podChanged writes event-log lines for the pod changes that matter in a
// demo: got a node, became ready, stopped being ready, was told to stop.
func (c *Cluster) podChanged(was, now *corev1.Pod) {
	if was.Spec.NodeName == "" && now.Spec.NodeName != "" {
		c.Emit("cluster", fmt.Sprintf("pod %s scheduled on %s", now.Name, now.Spec.NodeName))
	}
	if !podIsReady(was) && podIsReady(now) {
		c.Emit("cluster", fmt.Sprintf("pod %s ready%s", now.Name, onNode(now.Spec.NodeName)))
	}
	if podIsReady(was) && !podIsReady(now) {
		c.Emit("cluster", fmt.Sprintf("pod %s no longer ready%s", now.Name, onNode(now.Spec.NodeName)))
	}
	if was.DeletionTimestamp == nil && now.DeletionTimestamp != nil {
		c.Emit("cluster", fmt.Sprintf("pod %s is shutting down%s", now.Name, onNode(now.Spec.NodeName)))
	}
}

// routineWarnings are Kubernetes Warning events that are normal and would
// only add noise to the event log. Both come from the autoscaler in the first
// seconds after a deploy, before the new pods have any CPU measurements. The
// autoscaler simply tries again fifteen seconds later.
var routineWarnings = map[string]bool{
	"FailedGetResourceMetric":      true,
	"FailedComputeMetricsReplicas": true,
}

// kubernetesEvent passes one of Kubernetes' own Warning events on to the
// event log, the first time it is seen.
func (c *Cluster) kubernetesEvent(event *corev1.Event) {
	if event.Type != corev1.EventTypeWarning || routineWarnings[event.Reason] {
		return
	}
	// A pod that has been told to stop deliberately fails its readiness
	// check while it shuts down (camera-ingest answers "not ready" so that
	// traffic is steered away first). Kubernetes reports that as an
	// "Unhealthy" warning, which looks alarming but is the plan working.
	// Drop it for pods that are shutting down or already gone; an Unhealthy
	// warning for a pod that should be running is still passed on.
	if event.Reason == "Unhealthy" && event.InvolvedObject.Kind == "Pod" && c.podIsGoingAway(event.InvolvedObject.Name) {
		return
	}
	c.mu.Lock()
	seen := c.reportedEvents[string(event.UID)]
	if !seen {
		// Do not let the memory of old events grow without limit.
		if len(c.reportedEvents) > 5000 {
			c.reportedEvents = map[string]bool{}
		}
		c.reportedEvents[string(event.UID)] = true
	}
	c.mu.Unlock()
	if !seen {
		c.Emit("cluster", fmt.Sprintf("Kubernetes warning: %s on %s: %s", event.Reason, event.InvolvedObject.Name, event.Message))
	}
}

// podIsGoingAway reports whether a pod in the workload namespace has been
// told to stop, or has already gone.
func (c *Cluster) podIsGoingAway(name string) bool {
	pod, err := c.podLister.Pods(c.namespace).Get(name)
	return err != nil || pod.DeletionTimestamp != nil
}

// wanted is the number of pods a Deployment asks for.
func wanted(deployment *appsv1.Deployment) int32 {
	if deployment.Spec.Replicas == nil {
		return 1
	}
	return *deployment.Spec.Replicas
}

// onNode writes " on <node>", or nothing if the pod has no node.
func onNode(node string) string {
	if node == "" {
		return ""
	}
	return " on " + node
}

// removed gets the object out of a "this was deleted" notice. Usually the
// notice is the object itself. If the watch was briefly disconnected when
// the deletion happened, it is a wrapper holding the last copy we had.
func removed[T any](object any) (T, bool) {
	if wrapper, ok := object.(cache.DeletedFinalStateUnknown); ok {
		object = wrapper.Obj
	}
	typed, ok := object.(T)
	return typed, ok
}
