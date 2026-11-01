@echo off
rem ============================================================
rem  START - boots 9router + Reset-Lane detached in background
rem ============================================================
setlocal
title 9Router + Reset-Lane Starter
cd /d "%~dp0"

echo.
echo ============================================================
echo   Starting 9Router + OpenCode Reset-Lane (GLM-5.3-Flash)...
echo ============================================================

if exist "tools\launch_services.py" (
    python tools\launch_services.py
) else (
    set "DIR=%~dp0reset-lane"
    set "APP_DIR=%APPDATA%\npm\node_modules\9router"
    powershell -NoProfile -ExecutionPolicy Bypass -Command "$c = Get-NetTCPConnection -LocalPort 20194 -ErrorAction SilentlyContinue | Where-Object { $_.State -eq 'Listen' }; if (-not $c) { Start-Process -WindowStyle Hidden node -ArgumentList '%DIR%\worker\oc-cli-worker.js' }"
    powershell -NoProfile -ExecutionPolicy Bypass -Command "$c = Get-NetTCPConnection -LocalPort 20142 -ErrorAction SilentlyContinue | Where-Object { $_.State -eq 'Listen' }; if (-not $c) { Start-Process -WindowStyle Hidden node -ArgumentList '%DIR%\shim\oc-cli-api.js' }"
    powershell -NoProfile -ExecutionPolicy Bypass -Command "$c = Get-NetTCPConnection -LocalPort 20128 -ErrorAction SilentlyContinue | Where-Object { $_.State -eq 'Listen' }; if (-not $c) { $env:PORT='20128'; $env:HOSTNAME='0.0.0.0'; $env:NODE_PATH='%APPDATA%\9router\runtime\node_modules;%APP_DIR%\app\node_modules'; Start-Process -WindowStyle Hidden node -ArgumentList '--dns-result-order=ipv4first', '--max-old-space-size=6144', '%APP_DIR%\app\custom-server.js' }"
)

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
