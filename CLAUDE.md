# Rules for working in this repo

These rules apply to every task in this repository. If a request conflicts
with one of them, stop and ask the owner before doing anything.

## Safety rules

- Never write secrets into any file or commit: no passwords, tokens, keys,
  vault contents, or private IPs beyond the cluster subnet layout.
- Do not touch the cluster unless the task explicitly asks for it. That means
  no `kubectl` and no changes to any machine by default.
- If a credential is needed, ask the owner to log in themselves (for example
  with `gh auth login`, or their Gitea login in the browser). Do not ask them
  to paste a token into the chat.
- Stop and ask if anything looks unsafe.

## Limits when a task does involve the cluster

- Only act in the namespaces `workload` and `console`.
- Never power off machines.
- Do not change k3s, Argo CD or Traefik global settings.

## Reporting

- Report only what was measured. Do not estimate, extrapolate or fill gaps
  with expected values; if something was not measured, say so.

## Code style

- Write plain-English comments in Go code. The owner reads React but not Go,
  so comments should explain what each piece does and why, without assuming
  Go knowledge.
