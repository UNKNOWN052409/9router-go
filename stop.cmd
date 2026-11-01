@echo off
rem ============================================================
rem  STOP - Shuts down 9router + Reset-Lane services.
rem ============================================================
echo Stopping 9Router, Worker, Shim, and OpenCode processes...
powershell -NoProfile -Command "Get-CimInstance Win32_Process -Filter \"Name='node.exe'\" | Where-Object { $_.CommandLine -match 'oc-cli-api|oc-cli-worker|custom-server.js' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force; Write-Output ('Stopped process ' + $_.ProcessId) }"
powershell -NoProfile -Command "Get-CimInstance Win32_Process -Filter \"Name='opencode.exe'\" | ForEach-Object { Stop-Process -Id $_.ProcessId -Force; Write-Output ('Stopped opencode serve ' + $_.ProcessId) }"
echo [stop] All services stopped.
pause
