#!/usr/bin/env bash
# Asks the REAL cluster what the console is allowed to do, and compares the
# answers with what was agreed.
#
# It changes nothing. Each line is a question to Kubernetes of the form
# "may the console's ServiceAccount do X to Y in namespace Z?", answered yes
# or no (kubectl auth can-i). Run it after Argo CD has applied
# deploy/manifests/console-rbac.yaml.
#
# It needs admin access to ask on the console's behalf, so run it on a
# control-plane node. From the laptop:
#
#     ssh -p <port> pi@<gateway> 'bash -s' < scripts/check-rbac.sh
#
# Exit status is 0 if every answer is as agreed, 1 otherwise.
set -u

KUBECTL="${KUBECTL:-sudo -n k3s kubectl}"
WHO="system:serviceaccount:console:console"
wrong=0

# check <yes|no> <verb> <resource> [namespace]
check() {
  local want="$1" verb="$2" resource="$3" namespace="${4:-}"
  local where="cluster-wide" flag=()
  if [ -n "$namespace" ]; then where="in $namespace"; flag=(-n "$namespace"); fi
  # "deployments.apps/scale" means the scale sub-resource of Deployments.
  # kubectl must be told that with --subresource; written with a slash it
  # would take "scale" to be the NAME of a Deployment and answer a different
  # question.
  local kind="$resource"
  if [[ "$resource" == */* ]]; then
    kind="${resource%%/*}"
    flag+=(--subresource="${resource#*/}")
  fi
  local got
  got=$($KUBECTL auth can-i "$verb" "$kind" "${flag[@]}" --as="$WHO" 2>/dev/null | head -1)
  local mark="ok   "
  if [ "$got" != "$want" ]; then mark="WRONG"; wrong=$((wrong + 1)); fi
  printf '%s  %-6s %-42s %-14s agreed: %-3s  cluster says: %s\n' "$mark" "$verb" "$resource" "$where" "$want" "$got"
}

echo "--- What the console MUST be able to do -------------------------------"
check yes list   pods                                    workload
check yes delete pods                                    workload
check yes list   events                                  workload
check yes list   deployments.apps                        workload
check yes create deployments.apps                        workload
check yes delete deployments.apps                        workload
check yes patch  deployments.apps/scale                  workload
check yes list   horizontalpodautoscalers.autoscaling    workload
check yes create horizontalpodautoscalers.autoscaling    workload
check yes delete horizontalpodautoscalers.autoscaling    workload
check yes create services                                workload
check yes delete services                                workload
check yes create ingresses.networking.k8s.io             workload
check yes delete ingresses.networking.k8s.io             workload
check yes list   nodes
check yes list   applications.argoproj.io                argocd

echo "--- What it must NOT be able to do: in workload ------------------------"
check no  create resourcequotas                          workload
check no  patch  resourcequotas                          workload
check no  update resourcequotas                          workload
check no  delete resourcequotas                          workload
check no  patch  deployments.apps                        workload
check no  update deployments.apps                        workload
check no  patch  horizontalpodautoscalers.autoscaling    workload
check no  update services                                workload
check no  create pods                                    workload
check no  create pods/exec                               workload
check no  get    secrets                                 workload
check no  create roles.rbac.authorization.k8s.io         workload

echo "--- What it must NOT be able to do: anywhere else ----------------------"
for namespace in kube-system default console argocd gitea monitoring whoami; do
  check no delete pods                    "$namespace"
  check no patch  deployments.apps/scale  "$namespace"
  check no delete deployments.apps        "$namespace"
  check no get    secrets                 "$namespace"
done
check no  list   pods                                    kube-system
check no  patch  applications.argoproj.io                argocd
check no  delete applications.argoproj.io                argocd
check no  create applications.argoproj.io                argocd
check no  delete nodes
check no  patch  nodes
check no  create namespaces
check no  delete namespaces
check no  list   secrets
check no  create clusterrolebindings.rbac.authorization.k8s.io

echo
if [ "$wrong" -eq 0 ]; then
  echo "All answers are as agreed."
else
  echo "$wrong answer(s) differ from what was agreed."
  exit 1
fi
