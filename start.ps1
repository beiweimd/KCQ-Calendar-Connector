# 本地启动日历服务；只读已有数据库，不在启动时自动联网更新。
[CmdletBinding()]
param(
    [string]$Address = '127.0.0.1:8094',
    [string]$Database = (Join-Path $PSScriptRoot 'data/calendar.db')
)
$ErrorActionPreference = 'Stop'
$taskBinary = Join-Path $PSScriptRoot 'bin/calendar-server.exe'
if (-not (Test-Path -LiteralPath $taskBinary -PathType Leaf)) {
    throw '请先在项目目录执行 go build -o bin/calendar-server.exe ./cmd/server'
}
if (-not (Test-Path -LiteralPath $Database -PathType Leaf)) {
    throw '请先按 README 执行 scripts/sync_calendar.py 生成日历数据库'
}
& $taskBinary --addr $Address --db $Database
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
