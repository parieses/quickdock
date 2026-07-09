# QuickDock 内置插件打包脚本
# 将 plugins/builtin/ 下的每个插件打包为 zip

param(
    [string]$PluginDir = "$PSScriptRoot/../plugins/builtin",
    [string]$OutputDir = "$PSScriptRoot/../plugins/builtin"
)

$ErrorActionPreference = "Stop"

# 转为绝对路径
$PluginDir = Resolve-Path $PluginDir
$OutputDir = Resolve-Path $OutputDir

Write-Host "打包内置插件..." -ForegroundColor Cyan

$pluginFolders = Get-ChildItem $PluginDir -Directory

foreach ($folder in $pluginFolders) {
    $pluginJson = Join-Path $folder.FullName "plugin.json"
    if (-not (Test-Path $pluginJson)) {
        Write-Host "  跳过 ${$folder.Name}: 无 plugin.json" -ForegroundColor Yellow
        continue
    }

    $zipPath = Join-Path $OutputDir "$($folder.Name).zip"

    # 删除已存在的 zip
    if (Test-Path $zipPath) {
        Remove-Item $zipPath -Force
    }

    # 压缩文件夹内容（不含外层目录）
    Compress-Archive -Path "$($folder.FullName)\*" -DestinationPath $zipPath

    Write-Host "  ✓ $($folder.Name).zip 已创建" -ForegroundColor Green
}

Write-Host "`n完成！打包了 $($pluginFolders.Count) 个插件。" -ForegroundColor Cyan
