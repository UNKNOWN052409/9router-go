@echo off
rem ============================================================
rem  START - boots 9router + Reset-Lane in one command
rem  All background processes run hidden (no annoying windows).
rem ============================================================
setlocal
title 9Router + Reset-Lane Starter
cd /d "%~dp0"

echo.
echo ============================================================
echo   Starting 9Router + OpenCode Reset-Lane (GLM-5.3-Flash)...
echo ============================================================

set "DIR=%~dp0reset-lane"
set "APP_DIR=%APPDATA%\npm\node_modules\9router"

rem --- 1. Reset-Lane Worker (Port 20194) ---
powershell -NoProfile -ExecutionPolicy Bypass -Command "$c = Get-NetTCPConnection -LocalPort 20194 -ErrorAction SilentlyContinue | Where-Object { $_.State -eq 'Listen' }; if (-not $c) { Start-Process -WindowStyle Hidden node -ArgumentList '%DIR%\worker\oc-cli-worker.js'; Write-Host '[+] Started Reset-Lane Worker' } else { Write-Host '[=] Reset-Lane Worker already running' }"

rem --- 2. Reset-Lane Shim (Port 20142) ---
powershell -NoProfile -ExecutionPolicy Bypass -Command "$c = Get-NetTCPConnection -LocalPort 20142 -ErrorAction SilentlyContinue | Where-Object { $_.State -eq 'Listen' }; if (-not $c) { Start-Process -WindowStyle Hidden node -ArgumentList '%DIR%\shim\oc-cli-api.js'; Write-Host '[+] Started Reset-Lane Shim' } else { Write-Host '[=] Reset-Lane Shim already running' }"

rem --- 3. 9Router Gateway (Port 20128) ---
powershell -NoProfile -ExecutionPolicy Bypass -Command "$c = Get-NetTCPConnection -LocalPort 20128 -ErrorAction SilentlyContinue | Where-Object { $_.State -eq 'Listen' }; if (-not $c) { $env:PORT='20128'; $env:HOSTNAME='0.0.0.0'; $env:NODE_PATH='%APPDATA%\9router\runtime\node_modules;%APP_DIR%\app\node_modules'; Start-Process -WindowStyle Hidden node -ArgumentList '--dns-result-order=ipv4first', '--max-old-space-size=6144', '%APP_DIR%\app\custom-server.js'; Write-Host '[+] Started 9Router Gateway' } else { Write-Host '[=] 9Router Gateway already running' }"

rem --- 4. Wait 2 seconds and verify status ---
ping 127.0.0.1 -n 3 >nul
echo.
echo ============================================================
node -e "const net = require('net'); const ports = { 20142: 'Reset-Lane Shim  (Port 20142)', 20194: 'Reset-Lane Worker(Port 20194)', 20128: '9Router Gateway  (Port 20128)' }; for (const [p, name] of Object.entries(ports)) { const s = net.connect(Number(p), '127.0.0.1', () => { console.log('  [OK] ' + name + ' : ONLINE'); s.destroy(); }); s.on('error', () => console.log('  [!]  ' + name + ' : FAILED TO START')); }"
ping 127.0.0.1 -n 2 >nul
echo ============================================================
echo   Status: Everything is ready!
echo   Claude Code / Cursor / Cline can now connect.
echo ============================================================
echo.
pause
endlocal
