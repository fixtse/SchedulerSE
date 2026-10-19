"""Exercise the runtime image without network access, live credentials or schedules."""
import json
import subprocess
import sys
import time
import uuid

image = sys.argv[1]
name = "schedulerse-smoke-" + uuid.uuid4().hex[:12]

def docker(*args, check=True):
    return subprocess.run(["docker", *args], text=True, capture_output=True, check=check)

def wait_healthy():
    for attempt in range(30):
        if docker("exec", name, "/app/schedulerse", "healthcheck", check=False).returncode == 0:
            return
        time.sleep(0.2)
    raise AssertionError("healthcheck failed")

try:
    docker("run", "-d", "--name", name, "--network", "none", "--read-only",
           "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
           "--volume", name + "-state:/data",
           "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m",
           "-e", "TELEGRAM_API_URL=http://127.0.0.1:1/mock", image)
    wait_healthy()
    token = docker("exec", name, "/app/schedulerse", "setup-token").stdout.strip()
    assert len(token) >= 26, "setup token missing"
    plan = json.loads(docker("exec", name, "/app/schedulerse", "plan").stdout)
    assert plan["SchedulerEnabled"]
    assert plan["Jobs"] == [], "fresh installations must not contain bundled jobs"
    locked = docker("exec", name, "/app/schedulerse", "run", "example-task", check=False)
    assert locked.returncode != 0 and "run_started" not in locked.stdout, "volume lock bypassed"
    # Restart preserves the bootstrap token and journal; no missed runs are replayed.
    docker("restart", "--time", "20", name)
    wait_healthy()
    assert docker("exec", name, "/app/schedulerse", "setup-token").stdout.strip() == token
    docker("exec", name, "/app/schedulerse", "healthcheck")
    print("Runtime smoke test passed: health, plan, setup token, lock and restart.")
finally:
    docker("rm", "-f", name, check=False)
    docker("volume", "rm", name + "-state", check=False)
