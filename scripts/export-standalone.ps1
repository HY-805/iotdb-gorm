param(
    [string]$Destination = "D:\code\go\iotdb-client-go\_export\gorm-iotdb"
)

$source = "D:\code\go\iotdb-client-go\gorm-iotdb"
if (Test-Path $Destination) {
    Remove-Item -Recurse -Force $Destination
}

New-Item -ItemType Directory -Force $Destination | Out-Null
Copy-Item -Path (Join-Path $source "*") -Destination $Destination -Recurse -Force

$goMod = Join-Path $Destination "go.mod"
$content = Get-Content $goMod -Raw
$content = [regex]::Replace($content, "(?ms)\nreplace github.com/apache/iotdb-client-go/v2 => \.\./\s*$", "`n")
Set-Content $goMod $content

$readme = Join-Path $Destination "README.md"
$readmeContent = Get-Content $readme -Raw
$note = @"

## Export Notes

This exported copy has the parent-workspace `replace ../` directive removed so it can become its own repository.
Run `go mod tidy` in a network-enabled environment before the first push if the upstream IoTDB client module is not already cached.
"@
if ($readmeContent -notmatch "## Export Notes") {
    Set-Content $readme ($readmeContent + $note)
}

Write-Host "Exported standalone repository to $Destination"
