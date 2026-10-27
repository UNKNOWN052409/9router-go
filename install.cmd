@echo off
rem ============================================================
rem  INSTALL — one-time setup checks for a new machine.
rem
rem  Verifies: Node.js, the opencode CLI (installed + logged in),
rem  and data directories. Registers the optional watchdog task
rem  (every 2 min, fully hidden) so crashes self-heal.
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
    echo [install] Then run:         opencode auth login   ^(choose the free/no-auth tier^)
    exit /b 1
)
echo [install] opencode CLI found.

echo [install] checking opencode login (free tier)...
"%OC_EXE%" providers list >nul 2>&1
if errorlevel 1 (
    echo [install] WARN: could not verify providers. Run: opencode auth login
)

echo [install] creating data directories...
if not exist "D:\scratch\oc-cli-api\jobs"     mkdir "D:\scratch\oc-cli-api\jobs"
if not exist "D:\scratch\oc-cli-api\partials" mkdir "D:\scratch\oc-cli-api\partials"
if not exist "D:\scratch\oc-cli-api\work"     mkdir "D:\scratch\oc-cli-api\work"
if not exist "D:\scratch\oc-cli-api\attachments" mkdir "D:\scratch\oc-cli-api\attachments"

echo [install] registering hidden watchdog (optional self-healing, every 2 min)...
powershell -NoProfile -Command "
$act = New-ScheduledTaskAction -Execute 'wscript.exe' -Argument '\"%~dp0scripts\oc-chain-watchdog-hidden.vbs\"'
$trigger = New-ScheduledTaskTrigger -Once -At (Get-Date) -RepetitionInterval (New-TimeSpan -Minutes 2) -RepetitionDuration (New-TimeSpan -Days 3650)
Register-ScheduledTask -TaskName 'oc-chain-watchdog' -Action $act -Trigger $trigger -Force | Out-Null
Write-Output 'watchdog task registered'
" 2>&1 | findstr /i "watchdog registered FAIL"

echo.
echo [install] DONE. Next steps:
echo   1. Run START.CMD
echo   2. Point any OpenAI client at http://127.0.0.1:20142/v1
echo      (model example: glm-5.3-flash-free — no API key needed locally)
echo   See README.md for the full guide.
endlocal
