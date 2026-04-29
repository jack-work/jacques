$token = az account get-access-token --resource https://help.kusto.windows.net --query accessToken -o tsv
if (-not $token) {
    Write-Error "Failed to get token. Run 'az login' first."
    exit 1
}

$envPath = Join-Path $PSScriptRoot ".env"
$lines = @(Get-Content $envPath -ErrorAction SilentlyContinue)
$found = $false
$newLines = @()
foreach ($line in $lines) {
    if ($line -match '^KUSTO_TOKEN=') {
        $newLines += "KUSTO_TOKEN=$token"
        $found = $true
    } else {
        $newLines += $line
    }
}
if (-not $found) {
    $newLines += "KUSTO_TOKEN=$token"
}
$newLines | Set-Content $envPath -NoNewline:$false

Write-Host "Token refreshed in $envPath"
