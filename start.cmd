@echo off
rem ============================================================
rem  START — boots the whole free-model lane in one command.
rem
rem  What this starts (all hidden, no console windows):
rem    1. oc-cli-api.js    — OpenAI-compatible shim on 127.0.0.1:20142
rem    2. oc-cli-worker.js — fresh-session job runner (also boots the
rem                          persistent opencode serve on 20193)
rem
rem  Nothing else is required: point any OpenAI client at
rem    http://127.0.0.1:20142/v1        (standalone mode)
rem  and use model ids like glm-5.3-flash-free.
rem
rem  Optional: pass --with-router to also apply the node-9router
rem  registry patch and start 9router-go if its start script exists
rem  (only for users who route through 9router).
rem ============================================================
setlocal
set "DIR=%~dp0scripts"

rem --- 1. shim (API front-end) ---
powershell -NoProfile -Command "Get-CimInstance Win32_Process -Filter \"Name='node.exe'\" | Where-Object { $_.CommandLine -match 'oc-cli-api' } | Measure-Object | ForEach-Object { if ($_.Count -eq 0) { Start-Process -WindowStyle Hidden node -ArgumentList '%DIR%\oc-cli-api.js'; Write-Output 'shim started' } else { Write-Output 'shim already running' } }"

rem --- 2. worker (fresh-session runner; serve boots inside it) ---
powershell -NoProfile -Command "Get-CimInstance Win32_Process -Filter \"Name='node.exe'\" | Where-Object { $_.CommandLine -match 'oc-cli-worker' } | Measure-Object | ForEach-Object { if ($_.Count -eq 0) { Start-Process -WindowStyle Hidden node -ArgumentList '%DIR%\oc-cli-worker.js'; Write-Output 'worker started' } else { Write-Output 'worker already running' } }"

rem --- 3. optional: 9router integration ---
if /i "%1"=="--with-router" (
    if exist "%DIR%\patch-node9router-oc.js" (
        node "%DIR%\patch-node9router-oc.js"
        echo [start] node-9router oc/ lane patched if needed.
    )
    if exist "<9router-install>\rotation\start-9router-go-rotation.cmd" (
        call "<9router-install>\rotation\start-9router-go-rotation.cmd"
    )
)

rem --- health check ---
timeout /t 3 /nobreak >nul
curl -s -m 5 http://127.0.0.1:20142/health
echo.
echo [start] done. Endpoint: http://127.0.0.1:20142/v1  (model example: glm-5.3-flash-free)
endlocal
