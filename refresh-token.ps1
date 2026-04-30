param(
    [string]$Connection = "cap-analytics"
)

$token = az account get-access-token --resource https://help.kusto.windows.net --query accessToken -o tsv
if (-not $token) {
    Write-Error "Failed to get token. Run 'az login' first."
    exit 1
}

$env:KUSTO_TOKEN = $token

$jacques = Get-Command jacques.exe -CommandType Application -ErrorAction SilentlyContinue
if (-not $jacques) {
    $jacques = Get-Command jacques -CommandType Application -ErrorAction SilentlyContinue
}
if ($jacques) {
    & $jacques.Path config set-token $Connection
} else {
    Write-Warning "jacques not found on PATH. Token set in `$env:KUSTO_TOKEN only."
}

Write-Host "Also set `$env:KUSTO_TOKEN for this session."
