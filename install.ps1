Push-Location $PSScriptRoot
go install .
Pop-Location
Write-Host "jacques installed to $(go env GOPATH)\bin\jacques.exe"
