# 开发中插件打包脚本
# 用法: .\package-dev.ps1 [插件名，默认全部打包]

param(
    [string]$PluginName = "",
    [string]$DevDir = "$PSScriptRoot"
)

$ErrorActionPreference = "Stop"

if ($PluginName) {
    $folders = @(Get-ChildItem (Join-Path $DevDir $PluginName) -Directory)
} else {
    $folders = Get-ChildItem $DevDir -Directory
}

foreach ($folder in $folders) {
    $pluginJson = Join-Path $folder.FullName "plugin.json"
    if (-not (Test-Path $pluginJson)) {
        continue
    }

    $zipPath = Join-Path $DevDir "$($folder.Name).zip"
    if (Test-Path $zipPath) { Remove-Item $zipPath -Force }

    Compress-Archive -Path "$($folder.FullName)\*" -DestinationPath $zipPath
    Write-Host "  ✓ $($folder.Name).zip" -ForegroundColor Green
}

Write-Host "`n打包完成。" -ForegroundColor Cyan
