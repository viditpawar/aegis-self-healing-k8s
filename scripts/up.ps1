<#
.SYNOPSIS
    Brings up the full Aegis stack on a local kind cluster in one command.

.DESCRIPTION
    Runs every step from the README's "Running locally" section. Safe to
    re-run: an existing cluster is reused, Helm uses upgrade --install, and
    manifests are applied idempotently. Every kubectl/helm call is pinned to
    the kind cluster's context, so your current kubectl context is untouched.

.PARAMETER DiscordWebhookUrl
    Optional Discord webhook URL. When given (or when the discord-webhook
    Secret already exists from an earlier run), PodCrashLooping alerts are
    routed to Discord.

.EXAMPLE
    .\scripts\up.ps1
    .\scripts\up.ps1 -DiscordWebhookUrl https://discord.com/api/webhooks/...
#>
[CmdletBinding()]
param(
    [string]$ClusterName = "aegis",
    [string]$DiscordWebhookUrl = ""
)

$ErrorActionPreference = "Stop"
Set-Location (Split-Path -Parent $PSScriptRoot)

# Pinned so a re-run months from now installs what this repo was tested with.
$PromStackVersion = "88.3.0"
$ArgoRolloutsVersion = "v1.9.1"
$Ctx = "kind-$ClusterName"

function Step([string]$Message) {
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

# Windows PowerShell 5.1 doesn't stop on a failing native command, so check
# every exit code explicitly.
function Invoke-Native([scriptblock]$Command) {
    & $Command
    if ($LASTEXITCODE -ne 0) {
        throw "Command failed with exit code ${LASTEXITCODE}: $Command"
    }
}

function Kube {
    kubectl --context $Ctx @args
    if ($LASTEXITCODE -ne 0) {
        throw "kubectl $args failed with exit code $LASTEXITCODE"
    }
}

# Runs a native command for its stdout only, discarding stderr. In Windows
# PowerShell 5.1, redirecting native stderr under ErrorActionPreference=Stop
# turns any stderr line into a terminating error, so relax it locally.
# Returns the output; check $LASTEXITCODE for success.
function Invoke-Quiet([scriptblock]$Command) {
    $ErrorActionPreference = "Continue"
    & $Command 2>$null
}

Step "Checking prerequisites"
foreach ($tool in "docker", "kind", "kubectl", "helm") {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) {
        throw "$tool is not on PATH"
    }
}
Invoke-Native { docker info --format "{{.ServerVersion}}" | Out-Null }

Step "Cluster '$ClusterName'"
$clusters = @(Invoke-Quiet { kind get clusters })
if ($clusters -contains $ClusterName) {
    Write-Host "already exists, reusing it"
} else {
    Invoke-Native { kind create cluster --config kind\cluster-config.yaml --name $ClusterName }
}

Step "Namespaces + Pod Security Standards"
Kube apply -f manifests\namespaces.yaml

Step "Monitoring stack (kube-prometheus-stack $PromStackVersion)"
Invoke-Native { helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update | Out-Null }
Invoke-Quiet { kubectl --context $Ctx get secret discord-webhook -n aegis-system } | Out-Null
$discordSecretExists = ($LASTEXITCODE -eq 0)
$enableDiscord = ($DiscordWebhookUrl -ne "") -or $discordSecretExists

$helmArgs = @(
    "upgrade", "--install", "monitoring", "prometheus-community/kube-prometheus-stack",
    "--kube-context", $Ctx,
    "--namespace", "monitoring",
    "--version", $PromStackVersion,
    "--set", "grafana.enabled=true",
    "--set", "prometheus.prometheusSpec.retention=6h",
    "--wait", "--timeout", "10m"
)
if ($enableDiscord) { $helmArgs += @("-f", "manifests\monitoring\alertmanager-values.yaml") }
Invoke-Native { helm @helmArgs }
Kube apply -f manifests\prometheus-rules.yaml
Kube apply -f manifests\monitoring\grafana-dashboard.yaml

Step "Building and loading the controller image"
Invoke-Native { docker build -t aegis-controller:latest .\controller }
Invoke-Native { kind load docker-image aegis-controller:latest --name $ClusterName }

Step "Deploying aegis-controller"
Kube apply -f manifests\controller\rbac.yaml
Kube apply -f manifests\controller\deployment.yaml
Kube apply -f manifests\controller\servicemonitor.yaml
# The tag is always :latest, so restart to pick up the image just loaded.
Kube rollout restart deployment/aegis-controller -n aegis-system
Kube rollout status deployment/aegis-controller -n aegis-system --timeout=180s

if ($enableDiscord) {
    Step "Discord alerting"
    if ($DiscordWebhookUrl -ne "") {
        kubectl --context $Ctx create secret generic discord-webhook -n aegis-system `
            --from-literal=DISCORD_WEBHOOK_URL=$DiscordWebhookUrl --dry-run=client -o yaml |
            kubectl --context $Ctx apply -f -
        if ($LASTEXITCODE -ne 0) { throw "failed to create discord-webhook secret" }
    }
    Kube apply -f manifests\monitoring\alertmanager-discord.yaml
} else {
    Write-Host ""
    Write-Host "Skipping Discord alerting (pass -DiscordWebhookUrl to enable)" -ForegroundColor DarkGray
}

Step "Argo Rollouts $ArgoRolloutsVersion"
kubectl --context $Ctx create namespace argo-rollouts --dry-run=client -o yaml | kubectl --context $Ctx apply -f -
if ($LASTEXITCODE -ne 0) { throw "failed to create argo-rollouts namespace" }
Kube apply -n argo-rollouts -f "https://github.com/argoproj/argo-rollouts/releases/download/$ArgoRolloutsVersion/install.yaml"
Kube wait --for condition=established crd/rollouts.argoproj.io crd/analysistemplates.argoproj.io --timeout=60s
Kube rollout status deployment/argo-rollouts -n argo-rollouts --timeout=180s
Kube apply -f manifests\rollouts\canary-app.yaml
Kube apply -f manifests\rollouts\analysis-template.yaml
Kube apply -f manifests\rollouts\demo-app-servicemonitor.yaml

Step "NetworkPolicy"
Kube apply -f manifests\networkpolicy\default-deny.yaml
Kube apply -f manifests\networkpolicy\allow-demo-app.yaml

Step "Done"
Kube get pods -n aegis-system
Write-Host ""
Write-Host "Aegis is up on context $Ctx. Try:" -ForegroundColor Green
Write-Host "  kubectl config use-context $Ctx"
Write-Host "  kubectl logs -n aegis-system deploy/aegis-controller"
Write-Host "  kubectl port-forward -n monitoring svc/monitoring-grafana 3000:80"
