@echo off
rem ============================================================
rem  STOP — shuts the free-model lane down.
rem  Kills exactly the shim + worker processes (and the opencode
rem  serve the worker spawned). Nothing else is touched.
rem ============================================================
powershell -NoProfile -Command "Get-CimInstance Win32_Process -Filter \"Name='node.exe'\" | Where-Object { $_.CommandLine -match 'oc-cli-api|oc-cli-worker' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force; Write-Output ('stopped ' + $_.ProcessId) }"
powershell -NoProfile -Command "Get-CimInstance Win32_Process -Filter \"Name='opencode.exe'\" | ForEach-Object { Stop-Process -Id $_.ProcessId -Force; Write-Output ('serve stopped ' + $_.ProcessId) }"
echo [stop] done.
