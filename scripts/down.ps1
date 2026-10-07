<#
.SYNOPSIS
    Deletes the local Aegis kind cluster and everything in it.
#>
[CmdletBinding()]
param(
    [string]$ClusterName = "aegis"
)

$ErrorActionPreference = "Stop"

# Relaxed locally: under Stop, PowerShell 5.1 turns redirected native
# stderr (kind prints "No kind clusters found." there) into an error.
$ErrorActionPreference = "Continue"
$clusters = @(kind get clusters 2>$null)
$ErrorActionPreference = "Stop"
if ($clusters -notcontains $ClusterName) {
    Write-Host "No kind cluster named '$ClusterName', nothing to do."
    exit 0
}

kind delete cluster --name $ClusterName
if ($LASTEXITCODE -ne 0) { throw "kind delete cluster failed" }
