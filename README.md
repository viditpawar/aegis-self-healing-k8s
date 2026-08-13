# Aegis — Self-Healing Kubernetes Platform

Aegis is a self-healing platform for Kubernetes. A Go controller continuously
watches workload pods and automatically remediates two common failure modes:
pods stuck in `CrashLoopBackOff` and pods stuck `Pending` past a timeout, by
deleting them so the scheduler tries again. It's backed by Prometheus/Alertmanager
for observability and alerting, Argo Rollouts for safe canary deployments, and
namespace-level Pod Security Standards and NetworkPolicy for defense in depth.

This repo runs entirely on a local [kind](https://kind.sigs.k8s.io/) cluster —
no cloud account required.

## Architecture

```
                       ┌─────────────────────┐
                       │   kind cluster       │
                       │   "aegis" (3 nodes)  │
                       └─────────────────────┘
        ┌───────────────────────┼───────────────────────────┐
        │                       │                           │
 ┌──────────────┐      ┌────────────────┐          ┌──────────────────┐
 │ aegis-system  │      │ aegis-workloads │          │ monitoring        │
 │               │      │                 │          │                    │
 │ aegis-controller     │ app pods         │          │ kube-prometheus-  │
 │ (watches pods, │─────▶ (Rollouts,       │◀─────────│ stack (Prometheus, │
 │  deletes broken│      │  crash-test, …) │  scrapes │ Alertmanager,      │
 │  ones)         │      │                 │          │ Grafana)           │
 │ PSS: restricted│      │ PSS: baseline   │          │ PSS: privileged    │
 └──────────────┘      └────────────────┘          └──────────────────┘
                               │
                        ┌──────────────┐
                        │ argo-rollouts │
                        │ controller    │
                        └──────────────┘
```

- **`aegis-controller`** (`controller/`) — a Go binary using `client-go` that
  lists pods in `aegis-workloads` every 15s and deletes any pod that is
  crash-looping (`RestartCount > 5` with reason `CrashLoopBackOff`) or stuck
  `Pending` for more than 5 minutes. Runs as a non-root, no-privilege-escalation
  container so it satisfies the `restricted` Pod Security Standard enforced on
  `aegis-system`.
- **`monitoring`** — `kube-prometheus-stack` (Prometheus, Alertmanager, Grafana,
  node-exporter, kube-state-metrics). Runs in its own namespace, separate from
  `aegis-system`, because `node-exporter` needs host-level access
  (`hostNetwork`, `hostPID`, `hostPath`) that both `restricted` and `baseline`
  Pod Security Standards block.
- **`argo-rollouts`** — the Argo Rollouts controller, enabling canary
  deployments (`manifests/rollouts/canary-app.yaml`) gated by a Prometheus
  success-rate `AnalysisTemplate`.
- **NetworkPolicy** — `aegis-workloads` has a default-deny-ingress policy.
  **Caveat:** kind's default CNI (`kindnet`) does not enforce NetworkPolicy,
  so on this local cluster the policy is present but not actually enforced.
  It takes effect automatically on clusters with a policy-enforcing CNI
  (Calico, Cilium, or any managed cloud CNI like EKS/GKE/AKS's default).
- **CI/CD** — `.github/workflows/build.yaml` builds and pushes the controller
  image to GitHub Container Registry (`ghcr.io`) on every push to `controller/**`,
  using the repo's built-in `GITHUB_TOKEN` — no cloud credentials needed.

## Prerequisites

- Docker Desktop (running)
- `kubectl`, `kind`, `helm`, `go`, `gh`

## Running locally

```powershell
# 1. Create the cluster
kind create cluster --config kind\cluster-config.yaml

# 2. Namespaces + Pod Security Standards
kubectl apply -f manifests\namespaces.yaml

# 3. Monitoring stack
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm install monitoring prometheus-community/kube-prometheus-stack `
  --namespace monitoring `
  --set grafana.enabled=true `
  --set prometheus.prometheusSpec.retention=6h
kubectl apply -f manifests\prometheus-rules.yaml

# 4. Build and load the controller image
docker build -t aegis-controller:latest .\controller
kind load docker-image aegis-controller:latest --name aegis

# 5. Deploy the controller
kubectl apply -f manifests\controller\rbac.yaml
kubectl apply -f manifests\controller\deployment.yaml

# 6. Argo Rollouts
kubectl create namespace argo-rollouts
kubectl apply -n argo-rollouts -f https://github.com/argoproj/argo-rollouts/releases/latest/download/install.yaml
kubectl apply -f manifests\rollouts\canary-app.yaml
kubectl apply -f manifests\rollouts\analysis-template.yaml

# 7. NetworkPolicy
kubectl apply -f manifests\networkpolicy\default-deny.yaml
```

## Verifying self-healing

```powershell
kubectl run crash-test --image=busybox -n aegis-workloads --restart=Always -- sh -c "exit 1"
kubectl logs -n aegis-system -l app=aegis-controller -f
```

Once the pod's restart count passes 5, the controller log will show it
deleting the pod to force a reschedule.

## Teardown

```powershell
kind delete cluster --name aegis
```
