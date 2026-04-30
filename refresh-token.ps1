param(
    [string]$Connection = "cap-analytics"
)

$token = az account get-access-token --resource https://help.kusto.windows.net --query accessToken -o tsv
if (-not $token) {
    Write-Error "Failed to get token. Run 'az login' first."
    exit 1
}

$env:KUSTO_TOKEN = $token
$jacques = Get-Command jacques -ErrorAction SilentlyContinue
if ($jacques) {
    & $jacques.Source config set-token $Connection
} else {
    & "$PSScriptRoot\..\dev\jacques\jacques.exe" config set-token $Connection
}

Write-Host "Also set `$env:KUSTO_TOKEN for this session."
