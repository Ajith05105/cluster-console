package api

// This test reads deploy/manifests/console-rbac.yaml, the file that says what
// the console may do in the cluster, and fails if that file ever allows more
// than the agreed rules. It is the "permissions" half of the safety proof;
// security_test.go is the "code" half.

import (
	"os"
	"sort"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

const rbacManifest = "../../deploy/manifests/console-rbac.yaml"

// grant is one "may do these verbs to these resources" entry, together with
// where it applies.
type grant struct {
	kind      string // Role or ClusterRole
	name      string
	namespace string // empty for a ClusterRole, which applies everywhere
	rule      rbacv1.PolicyRule
}

// Verbs that only look, and verbs that change things.
var (
	readVerbs  = map[string]bool{"get": true, "list": true, "watch": true}
	writeVerbs = map[string]bool{"create": true, "update": true, "patch": true, "delete": true, "deletecollection": true}
)

// loadGrants reads the manifest and returns every grant in it, plus every
// binding's subjects and the role each binding points at.
func loadGrants(t *testing.T) (grants []grant, subjects []rbacv1.Subject, boundRoles map[string]bool) {
	t.Helper()
	contents, err := os.ReadFile(rbacManifest)
	if err != nil {
		t.Fatalf("cannot read %s: %v", rbacManifest, err)
	}
	boundRoles = map[string]bool{}

	for _, document := range strings.Split(string(contents), "\n---\n") {
		var header metav1.TypeMeta
		if err := yaml.Unmarshal([]byte(document), &header); err != nil {
			t.Fatalf("a document in %s is not valid YAML: %v", rbacManifest, err)
		}
		switch header.Kind {
		case "Role":
			var role rbacv1.Role
			mustParse(t, document, &role)
			for _, rule := range role.Rules {
				grants = append(grants, grant{"Role", role.Name, role.Namespace, rule})
			}
		case "ClusterRole":
			var role rbacv1.ClusterRole
			mustParse(t, document, &role)
			for _, rule := range role.Rules {
				grants = append(grants, grant{"ClusterRole", role.Name, "", rule})
			}
		case "RoleBinding":
			var binding rbacv1.RoleBinding
			mustParse(t, document, &binding)
			subjects = append(subjects, binding.Subjects...)
			boundRoles[binding.RoleRef.Kind+"/"+binding.Namespace+"/"+binding.RoleRef.Name] = true
		case "ClusterRoleBinding":
			var binding rbacv1.ClusterRoleBinding
			mustParse(t, document, &binding)
			subjects = append(subjects, binding.Subjects...)
			boundRoles[binding.RoleRef.Kind+"//"+binding.RoleRef.Name] = true
		case "ServiceAccount":
			// Nothing to check.
		default:
			t.Errorf("%s contains an unexpected kind %q", rbacManifest, header.Kind)
		}
	}
	if len(grants) == 0 {
		t.Fatalf("found no rules in %s; has the file moved or changed shape?", rbacManifest)
	}
	return grants, subjects, boundRoles
}

func mustParse(t *testing.T, document string, into any) {
	t.Helper()
	// UnmarshalStrict also refuses misspelt field names, which would
	// otherwise be silently ignored.
	if err := yaml.UnmarshalStrict([]byte(document), into); err != nil {
		t.Fatalf("cannot parse a document in %s: %v", rbacManifest, err)
	}
}

// Anything that changes the cluster is allowed in the workload namespace only.
func TestRBACWritesOnlyInWorkload(t *testing.T) {
	grants, _, _ := loadGrants(t)
	for _, g := range grants {
		for _, verb := range g.rule.Verbs {
			if writeVerbs[verb] && !(g.kind == "Role" && g.namespace == "workload") {
				t.Errorf("%s %q (namespace %q) allows %q on %v: changes are only allowed in the workload namespace",
					g.kind, g.name, g.namespace, verb, g.rule.Resources)
			}
			if !readVerbs[verb] && !writeVerbs[verb] {
				t.Errorf("%s %q allows the verb %q, which is not on the list of known verbs", g.kind, g.name, verb)
			}
		}
	}
}

// No wildcards, and none of the things the console must never touch.
func TestRBACHasNoWildcardsOrForbiddenResources(t *testing.T) {
	forbidden := map[string]bool{
		"resourcequotas": true, "secrets": true, "configmaps": true, "serviceaccounts": true,
		"roles": true, "rolebindings": true, "clusterroles": true, "clusterrolebindings": true,
		"namespaces": true, "nodes/proxy": true, "pods/exec": true,
	}
	grants, _, _ := loadGrants(t)
	for _, g := range grants {
		for _, value := range append(append(append([]string{}, g.rule.APIGroups...), g.rule.Resources...), g.rule.Verbs...) {
			if strings.Contains(value, "*") {
				t.Errorf("%s %q uses a wildcard (%q)", g.kind, g.name, value)
			}
		}
		for _, resource := range g.rule.Resources {
			if forbidden[resource] {
				t.Errorf("%s %q grants access to %q, which the console must never have", g.kind, g.name, resource)
			}
		}
		if len(g.rule.NonResourceURLs) > 0 {
			t.Errorf("%s %q grants access to raw URLs %v", g.kind, g.name, g.rule.NonResourceURLs)
		}
	}
}

// In the workload namespace, exactly the agreed permissions and no more.
func TestRBACWorkloadRoleIsExactlyAsAgreed(t *testing.T) {
	// resource -> the verbs allowed on it
	agreed := map[string]string{
		"/pods":                                "delete get list watch",
		"/events":                              "get list watch",
		"apps/deployments":                     "create delete get list watch",
		"apps/deployments/scale":               "patch",
		"autoscaling/horizontalpodautoscalers": "create delete get list watch",
		"/services":                            "create delete get",
		"networking.k8s.io/ingresses":          "create delete get",
	}
	got := map[string]string{}
	grants, _, _ := loadGrants(t)
	for _, g := range grants {
		if g.namespace != "workload" {
			continue
		}
		for _, group := range g.rule.APIGroups {
			for _, resource := range g.rule.Resources {
				verbs := append([]string{}, g.rule.Verbs...)
				sort.Strings(verbs)
				got[group+"/"+resource] = strings.TrimSpace(got[group+"/"+resource] + " " + strings.Join(verbs, " "))
			}
		}
	}
	for resource, verbs := range agreed {
		if got[resource] != verbs {
			t.Errorf("workload: %s allows [%s], agreed is [%s]", resource, got[resource], verbs)
		}
	}
	for resource, verbs := range got {
		if _, isAgreed := agreed[resource]; !isAgreed {
			t.Errorf("workload: %s allows [%s], but is not on the agreed list at all", resource, verbs)
		}
	}
}

// Outside the workload namespace: nodes (cluster-wide) and Argo CD
// applications (in argocd), read only, and nothing else.
func TestRBACReadOnlyGrantsAreExactlyAsAgreed(t *testing.T) {
	grants, _, _ := loadGrants(t)
	for _, g := range grants {
		if g.namespace == "workload" {
			continue
		}
		where := g.kind + " in namespace " + g.namespace
		var wantGroup, wantResource string
		switch {
		case g.kind == "ClusterRole":
			where, wantGroup, wantResource = "ClusterRole", "", "nodes"
		case g.kind == "Role" && g.namespace == "argocd":
			wantGroup, wantResource = "argoproj.io", "applications"
		default:
			t.Errorf("unexpected %s %q in namespace %q", g.kind, g.name, g.namespace)
			continue
		}
		if len(g.rule.APIGroups) != 1 || g.rule.APIGroups[0] != wantGroup ||
			len(g.rule.Resources) != 1 || g.rule.Resources[0] != wantResource {
			t.Errorf("%s: %q grants %v %v, agreed is only %q", where, g.name, g.rule.APIGroups, g.rule.Resources, wantResource)
		}
		for _, verb := range g.rule.Verbs {
			if !readVerbs[verb] {
				t.Errorf("%s: %q allows %q on %s; only get, list and watch are agreed", where, g.name, verb, wantResource)
			}
		}
	}
}

// Every permission is handed to the console's own ServiceAccount and to
// nobody else, and every role in the file is actually bound.
func TestRBACIsBoundOnlyToTheConsole(t *testing.T) {
	grants, subjects, boundRoles := loadGrants(t)
	if len(subjects) == 0 {
		t.Fatal("no bindings found")
	}
	for _, subject := range subjects {
		if subject.Kind != "ServiceAccount" || subject.Name != "console" || subject.Namespace != "console" {
			t.Errorf("a binding gives access to %s %s/%s; only ServiceAccount console/console is agreed",
				subject.Kind, subject.Namespace, subject.Name)
		}
	}
	for _, g := range grants {
		if !boundRoles[g.kind+"/"+g.namespace+"/"+g.name] {
			t.Errorf("%s %q (namespace %q) is defined but never bound, or bound from a different namespace", g.kind, g.name, g.namespace)
		}
	}
}
