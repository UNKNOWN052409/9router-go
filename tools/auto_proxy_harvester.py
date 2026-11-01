#!/usr/bin/env python3
"""
Auto Proxy Harvester & Health Checker for 9Router Reset Lane.
Fetches fresh free proxies from multiple public feeds,
verifies connectivity and low latency, and continuously
keeps proxies.txt updated with working IPs.
"""

import time
import requests
import concurrent.futures
import os
import sys

BASE_DIR = r"C:\Users\binga\9router-go"
PROXIES_TXT = os.path.join(BASE_DIR, "proxies.txt")
WORKER_PROXIES_TXT = os.path.join(BASE_DIR, "reset-lane", "worker", "proxies.txt")

FEEDS = [
    "https://api.proxyscrape.com/v2/?request=displayproxies&protocol=http&timeout=3000&country=all&ssl=all&anonymity=all",
    "https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/http.txt",
    "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/http.txt"
]

def fetch_raw_proxies():
    pool = set()
    for url in FEEDS:
        try:
            r = requests.get(url, timeout=8)
            if r.status_code == 200:
                for line in r.text.splitlines():
                    p = line.strip()
                    if p and ":" in p and not p.startswith("#"):
                        pool.add(p)
        except Exception:
            pass
    return list(pool)

def verify_proxy(p):
    proxies = {"http": f"http://{p}", "https": f"http://{p}"}
    t0 = time.time()
    try:
        # Quick check against lightweight cloudflare-hosted endpoint
        r = requests.get("https://api.ipify.org?format=json", proxies=proxies, timeout=3.5)
        if r.status_code == 200:
            elapsed = round((time.time() - t0) * 1000)
            return (p, elapsed)
    except Exception:
        pass
    return None

def harvest_and_update():
    print(f"[{time.strftime('%Y-%m-%d %H:%M:%S')}] Harvesting fresh proxies from feeds...")
    candidates = fetch_raw_proxies()
    print(f"[{time.strftime('%Y-%m-%d %H:%M:%S')}] Fetched {len(candidates)} raw proxies. Testing quality...")

    working = []
    # Test candidates with 30 concurrent threads
    with concurrent.futures.ThreadPoolExecutor(max_workers=30) as executor:
        futures = [executor.submit(verify_proxy, p) for p in candidates[:150]]
        for f in concurrent.futures.as_completed(futures):
            res = f.result()
            if res:
                working.append(res)
            if len(working) >= 20: # Keep top 20 verified
                break

    if working:
        # Sort by lowest latency
        working.sort(key=lambda x: x[1])
        sorted_proxies = [w[0] for w in working]
        content = "\n".join(sorted_proxies) + "\n"

        with open(PROXIES_TXT, "w", encoding="utf-8") as f:
            f.write(content)
        try:
            with open(WORKER_PROXIES_TXT, "w", encoding="utf-8") as f:
                f.write(content)
        except Exception:
            pass

        print(f"[{time.strftime('%Y-%m-%d %H:%M:%S')}] SUCCESS: Updated {len(sorted_proxies)} active proxies (Fastest: {working[0][1]}ms).")
    else:
        print(f"[{time.strftime('%Y-%m-%d %H:%M:%S')}] Warning: No fast proxies found in this cycle.")

def daemon_loop(interval_sec=900): # 15 minutes cycle
    while True:
        try:
            harvest_and_update()
        except Exception as e:
            print("Harvester error:", e)
        time.sleep(interval_sec)

if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--once":
        harvest_and_update()
    else:
        daemon_loop()
