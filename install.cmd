@echo off
rem ============================================================
rem  INSTALL — one-time setup checks for a new machine.
rem
rem  Verifies: Node.js and the opencode CLI, creates the data
rem  directories (under OC_DATA_DIR when set), and registers the
rem  optional watchdog task (every 2 min, fully hidden) so crashes
rem  self-heal. No login, no account, no API key is needed.
rem  Run START.CMD whenever you want to use the models.
rem ============================================================
setlocal

echo [install] checking Node.js...
node --version >nul 2>&1
if errorlevel 1 (
    echo [install] FAIL: Node.js not found. Install Node 18+ from https://nodejs.org
    exit /b 1
)
node --version

echo [install] checking opencode CLI...
set "OC_EXE=%APPDATA%\npm\node_modules\opencode-ai\bin\opencode.exe"
if not exist "%OC_EXE%" (
    echo [install] FAIL: opencode CLI not found at %OC_EXE%
    echo [install] Install it with:  npm install -g opencode-ai
    echo [install] No login, no account, no API key is needed afterwards — the
    echo [install] free tier works anonymously, verified on a fresh profile.
    exit /b 1
)
echo [install] opencode CLI found.
echo [install] NOTE: no login, no account, no API key is needed — the free
echo [install] tier works anonymously, verified on a fresh profile.

rem Data root mirrors the scripts' derivation: OC_DATA_DIR env wins, then the
rem Windows default the scripts use. Keep both sides on the same root.
if "%OC_DATA_DIR%"=="" set "OC_DATA_DIR=D:\scratch\oc-cli-api"
echo [install] creating data directories under %OC_DATA_DIR% ...
if not exist "%OC_DATA_DIR%\jobs"       mkdir "%OC_DATA_DIR%\jobs"
if not exist "%OC_DATA_DIR%\partials"   mkdir "%OC_DATA_DIR%\partials"
if not exist "%OC_DATA_DIR%\work"       mkdir "%OC_DATA_DIR%\work"
if not exist "%OC_DATA_DIR%\attachments" mkdir "%OC_DATA_DIR%\attachments"

echo [install] registering hidden watchdog, optional self-healing, every 2 min...
rem Single -Command line: cmd cannot carry a multi-line quoted PowerShell block
rem — the continuation lines leak out and execute as batch commands.
powershell -NoProfile -Command "$act = New-ScheduledTaskAction -Execute 'wscript.exe' -Argument '\"%~dp0scripts\oc-chain-watchdog-hidden.vbs\"'; $trigger = New-ScheduledTaskTrigger -Once -At (Get-Date) -RepetitionInterval (New-TimeSpan -Minutes 2) -RepetitionDuration (New-TimeSpan -Days 3650); Register-ScheduledTask -TaskName 'oc-chain-watchdog' -Action $act -Trigger $trigger -Force | Out-Null; Write-Output 'watchdog task registered'" 2>&1 | findstr /i "watchdog registered FAIL"

echo.
echo [install] DONE. Next steps:
echo   1. Run START.CMD
echo   2. Point any OpenAI client at http://127.0.0.1:20142/v1
echo      (model example: glm-5.3-flash-free — no API key needed locally)
echo   See README.md for the full guide.
endlocal
