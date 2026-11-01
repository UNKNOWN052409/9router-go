import subprocess, os, sys, time

node_exe = r"C:\Program Files\nodejs\node.exe"
app_data = os.environ.get("APPDATA", r"C:\Users\binga\AppData\Roaming")

services = [
    {
        "name": "worker",
        "cmd": [node_exe, r"C:\Users\binga\9router-go\reset-lane\worker\oc-cli-worker.js"],
        "cwd": r"C:\Users\binga\9router-go",
        "env": os.environ.copy()
    },
    {
        "name": "shim",
        "cmd": [node_exe, r"C:\Users\binga\9router-go\reset-lane\shim\oc-cli-api.js"],
        "cwd": r"C:\Users\binga\9router-go",
        "env": os.environ.copy()
    },
    {
        "name": "proxy_harvester",
        "cmd": [sys.executable, r"C:\Users\binga\9router-go\tools\auto_proxy_harvester.py"],
        "cwd": r"C:\Users\binga\9router-go",
        "env": os.environ.copy()
    },
    {
        "name": "9router",
        "cmd": [node_exe, "--dns-result-order=ipv4first", "--max-old-space-size=6144", os.path.join(app_data, r"npm\node_modules\9router\app\custom-server.js")],
        "cwd": os.path.join(app_data, r"npm\node_modules\9router\app"),
        "env": {
            **os.environ.copy(),
            "PORT": "20128",
            "HOSTNAME": "0.0.0.0",
            "INITIAL_PASSWORD": "Admin@123456",
            "NODE_PATH": os.path.join(app_data, r"9router\runtime\node_modules") + ";" + os.path.join(app_data, r"npm\node_modules\9router\app\node_modules")
        }
    }
]

flags = 0x00000008 | 0x00000200 | 0x01000000 # DETACHED | NEW_PROCESS_GROUP | BREAKAWAY

for s in services:
    try:
        p = subprocess.Popen(s["cmd"], cwd=s["cwd"], env=s["env"], creationflags=flags, close_fds=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        print("Spawned", s["name"], "PID:", p.pid)
    except Exception as e:
        print("Failed to spawn", s["name"], e)

time.sleep(3)
print("Finished spawning services.")
