# oc-chain-watchdog: keep the shim + worker alive.
# Mirrors 9router-go-watchdog.ps1 behaviour: if a process is missing, start ONE instance.
$ErrorActionPreference = "SilentlyContinue"

function Get-Count([string]$pattern) {
  @(Get-CimInstance Win32_Process -Filter "Name='node.exe'" |
    Where-Object { $_.CommandLine -match $pattern }).Count
}

$worker = Get-Count 'oc-cli-worker'
if ($worker -eq 0) {
  Start-Process -WindowStyle Hidden node -ArgumentList "D:\scripts\oc-cli-worker.js"
  "$(Get-Date -Format o) worker missing -> started" | Add-Content "D:\scripts\oc-chain-watchdog.log"
} elseif ($worker -gt 1) {
  # duplicate workers delete each other's run dirs; keep only the first
  Get-CimInstance Win32_Process -Filter "Name='node.exe'" |
    Where-Object { $_.CommandLine -match 'oc-cli-worker' } |
    Select-Object -Skip 1 | ForEach-Object {
      Stop-Process -Id $_.ProcessId -Force
      "$(Get-Date -Format o) duplicate worker killed $($_.ProcessId)" | Add-Content "D:\scripts\oc-chain-watchdog.log"
    }
}

$api = Get-Count 'oc-cli-api'
if ($api -eq 0) {
  Start-Process -WindowStyle Hidden node -ArgumentList "D:\scripts\oc-cli-api.js"
  "$(Get-Date -Format o) api missing -> started" | Add-Content "D:\scripts\oc-chain-watchdog.log"
}

# ---------------------------------------------------------------------------
# 9router process hygiene (auto-clean the "mob"): keep exactly ONE router
# owning port 20128, reap stuck opencode run children, sweep stale results.
# ---------------------------------------------------------------------------
$log = "D:\scripts\oc-chain-watchdog.log"

# 1. Zombie router instances: node processes running 9router code that do
#    NOT own the 20128 listening socket (a cli.js leftover, or an extra
#    custom-server that lost the bind race). Exactly one owner survives.
$routerProcs = Get-CimInstance Win32_Process -Filter "Name='node.exe'" |
  Where-Object { $_.CommandLine -like '*9router*custom-server.js*' -or $_.CommandLine -like '*9router*cli.js*' }
$listenerPid = $null
foreach ($line in (netstat -ano)) {
  if ($line -match ':20128\s+\S+\s+LISTENING\s+(\d+)') { $listenerPid = $Matches[1]; break }
}
if ($routerProcs.Count -gt 1 -and $listenerPid) {
  $routerProcs | Where-Object { [string]$_.ProcessId -ne [string]$listenerPid } | ForEach-Object {
    Stop-Process -Id $_.ProcessId -Force
    "$(Get-Date -Format o) zombie 9router killed $($_.ProcessId) (port owned by $listenerPid)" | Add-Content $log
  }
}

# 2. Stuck opencode run children: a spawn whose parent died or that outlived
#    every sane timeout. Anything older than 10 minutes gets the tree killed.
$cutoff = (Get-Date).AddMinutes(-10)
Get-CimInstance Win32_Process -Filter "Name='opencode.exe'" |
  Where-Object { $_.CommandLine -match 'run --format json' -and $_.CreationDate -lt $cutoff } |
  ForEach-Object {
    taskkill /pid $_.ProcessId /T /F 2>$null | Out-Null
    "$(Get-Date -Format o) stuck opencode run reaped $($_.ProcessId) (age >10min)" | Add-Content $log
  }

# 3. Stale result files: .out.json older than an hour in the jobs dir —
#    results whose shim-side waiter is long gone (timeout orphans).
Get-ChildItem 'D:\scratch\oc-cli-api\jobs' -Filter '*.out.json' -ErrorAction SilentlyContinue |
  Where-Object { $_.LastWriteTime -lt (Get-Date).AddHours(-1) } |
  ForEach-Object {
    Remove-Item $_.FullName -Force
    "$(Get-Date -Format o) stale result swept $($_.Name)" | Add-Content $log
  }
