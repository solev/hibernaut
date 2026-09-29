import os
import time

from e2b import Sandbox

os.environ.update(
    E2B_API_KEY="e2b_53ae1fed82754c17ad8077fbc8bcdd90",
    E2B_API_URL="http://localhost:3000",
    E2B_SANDBOX_URL="http://localhost:3002",
)

def timed(label, fn):
    start = time.perf_counter()
    result = fn()
    print(f"{label}: {(time.perf_counter() - start) * 1000:.0f} ms")
    return result

sandbox = timed("create sandbox", lambda: Sandbox.create(template="base"))
print("sandbox id:", sandbox.sandbox_id)
out = timed("run command", lambda: sandbox.commands.run("uname -r; nproc; free -m | sed -n 2p; cat /etc/os-release | head -1"))
print(out.stdout.strip())

sandbox.commands.run("echo 'state survives pause' > /home/user/note.txt")
timed("pause (snapshot memory+disk)", sandbox.pause)
resumed = timed("resume", lambda: Sandbox.connect(sandbox.sandbox_id))
print("after resume:", resumed.commands.run("cat /home/user/note.txt").stdout.strip())

timed("kill", resumed.kill)
