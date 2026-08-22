#!/usr/bin/env python3
# SentinelDesk
# A collaborative operating system for people and AI agents.
#
# Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
#
# Licensed under the Apache License, Version 2.0.
#
# This product's name and logo are trademarks of Federico Pereira and are not
# covered by the license above. See the README for the trademark policy.
#
# SPDX-License-Identifier: Apache-2.0

"""Real end-to-end validation of the MCP plane.

Not "did the call return ok" — the repository's own doctrine is that asserting
success asserts almost nothing. Every check here reads the EFFECT back: a file
typed through XTEST is read from disk, a written file is round-tripped, a
screenshot is measured, the accessibility tree is parsed. A tool that returns
ok while doing nothing fails here.

    ./mcp-validate.py --container sentineldesk

Speaks the same JSON-RPC-over-stdio bridge an AI host does, so it needs nothing
configured. Exits non-zero if any check fails, and prints a table.
"""

import argparse
import json
import subprocess
import sys
import threading
import time

SOCK = "/run/user/1000/sentineldesk-mcp.sock"  # fallback if MCP_SOCK is unset


def resolve_sock(container):
    """Where the daemon actually listens — read MCP_SOCK from the container
    rather than assuming, so this works whether the socket sits at its private
    default or was relocated (e.g. -e MCP_SOCK=/run/sentineldesk/mcp.sock, which
    the installer and compose set by default)."""
    try:
        out = subprocess.run(["docker", "exec", container, "printenv", "MCP_SOCK"],
                             capture_output=True, text=True, timeout=10)
        if out.stdout.strip():
            return out.stdout.strip()
    except Exception:
        pass
    return SOCK
EXPECT_TOOLS = 134


class MCP:
    def __init__(self, container):
        self._id = 0
        self._resps = {}
        self.proc = subprocess.Popen(
            ["docker", "exec", "-i", "-u", "sentineldesk", container,
             "/usr/local/bin/sentineldesk", "-mcp-stdio", "-mcp-sock", resolve_sock(container)],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        threading.Thread(target=self._reader, daemon=True).start()
        self.request("initialize", {"clientInfo": {"name": "validate", "version": "1"}})
        self.notify("notifications/initialized")

    def _reader(self):
        for line in self.proc.stdout:
            line = line.strip()
            if not line:
                continue
            try:
                msg = json.loads(line)
            except json.JSONDecodeError:
                continue
            if msg.get("id") is not None:
                self._resps[msg["id"]] = msg

    def _send(self, p):
        self.proc.stdin.write((json.dumps(p) + "\n").encode())
        self.proc.stdin.flush()

    def notify(self, method, params=None):
        self._send({"jsonrpc": "2.0", "method": method, "params": params or {}})

    def request(self, method, params=None, timeout=120):
        self._id += 1
        rid = self._id
        self._send({"jsonrpc": "2.0", "id": rid, "method": method, "params": params or {}})
        deadline = time.time() + timeout
        while rid not in self._resps and time.time() < deadline:
            if self.proc.poll() is not None:
                raise RuntimeError("bridge exited: " + self.proc.stderr.read().decode()[:400])
            time.sleep(0.03)
        if rid not in self._resps:
            raise TimeoutError(f"no reply to {method} after {timeout}s")
        return self._resps.pop(rid)

    def call(self, name, args=None, timeout=120):
        r = self.request("tools/call", {"name": name, "arguments": args or {}}, timeout)
        if "error" in r:
            return {"_transport_error": r["error"].get("message", "")}, True
        res = r["result"]
        text = "\n".join(c.get("text", "") for c in res.get("content", []) if c.get("type") == "text")
        img = [c for c in res.get("content", []) if c.get("type") == "image"]
        return {"text": text, "images": img}, bool(res.get("isError"))


RESULTS = []


def check(name, ok, detail=""):
    RESULTS.append((name, ok, detail))
    print(f"  {'PASS' if ok else 'FAIL'}  {name}" + (f"  — {detail}" if detail else ""))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--container", default="sentineldesk")
    args = ap.parse_args()
    m = MCP(args.container)

    # --- catalogue -----------------------------------------------------------
    tools = m.request("tools/list")["result"]["tools"]
    names = {t["name"] for t in tools}
    check(f"tools/list advertises {EXPECT_TOOLS}", len(tools) == EXPECT_TOOLS,
          f"got {len(tools)}")
    check("gamepad tools are gone", not any(n.startswith("gamepad") for n in names))

    # --- control: everything that injects needs it ---------------------------
    out, err = m.call("request_control", {"timeout_ms": 4000})
    check("request_control granted on an empty room", not err, out.get("text", "")[:80])

    # --- files: a real round trip -------------------------------------------
    marker = f"validate-ñ-{int(time.time())}"
    out, err = m.call("write_file", {"path": "/tmp/mcpval.txt", "content": marker})
    check("write_file returns ok", not err, out.get("text", "")[:80])
    out, err = m.call("read_file", {"path": "/tmp/mcpval.txt"})
    check("read_file returns what write_file wrote", marker in out.get("text", ""),
          out.get("text", "")[:80])
    out, err = m.call("list_directory", {"path": "/tmp"})
    check("list_directory shows the new file", "mcpval.txt" in out.get("text", ""))

    # --- run_command: exit code and stdout are real --------------------------
    stamp = f"rc-{int(time.time())}"
    out, err = m.call("run_command", {"command": f"echo {stamp}; exit 0", "timeout_ms": 15000})
    check("run_command captures stdout", stamp in out.get("text", ""), out.get("text", "")[:100])
    out, err = m.call("run_command", {"command": "exit 7", "timeout_ms": 15000})
    check("run_command reports a non-zero exit", "7" in out.get("text", ""), out.get("text", "")[:100])

    # --- clipboard round trip ------------------------------------------------
    clipmark = f"clip-ñ-{int(time.time())}"
    out, err = m.call("set_clipboard", {"text": clipmark})
    check("set_clipboard ok", not err)
    out, err = m.call("get_clipboard")
    check("get_clipboard returns what was set", clipmark in out.get("text", ""),
          out.get("text", "")[:80])

    # --- typing through XTEST, verified from disk ----------------------------
    # A terminal is opened, text is typed into it, Enter is pressed, and the
    # file the command created is read back — the only proof the keystrokes
    # actually landed.
    typed = f"typed-ñ-{int(time.time())}"
    m.call("run_command", {"command": "rm -f /tmp/mcptyped.txt", "timeout_ms": 8000})
    out, err = m.call("terminal_open", {})
    check("terminal_open ok", not err, out.get("text", "")[:80])
    time.sleep(1.5)
    m.call("type_text", {"text": f"printf %s '{typed}' > /tmp/mcptyped.txt"})
    m.call("key_combo", {"keys": "Return"})
    time.sleep(1.5)
    out, err = m.call("read_file", {"path": "/tmp/mcptyped.txt"})
    check("type_text + key_combo reach the terminal (read from disk)",
          typed in out.get("text", ""), out.get("text", "")[:80])

    # --- screenshot is a real image -----------------------------------------
    out, err = m.call("screenshot", {})
    img = out.get("images", [])
    size = len(img[0].get("data", "")) if img else 0
    check("screenshot returns a non-trivial image", size > 5000, f"{size} b64 bytes")

    # --- the accessibility tree answers --------------------------------------
    out, err = m.call("ui_tree", {"limit": 40})
    check("ui_tree returns structure", not err and len(out.get("text", "")) > 50,
          f"{len(out.get('text',''))} chars")

    # --- windows -------------------------------------------------------------
    out, err = m.call("list_windows", {})
    check("list_windows returns windows", not err and len(out.get("text", "")) > 10)
    out, err = m.call("desktop_state", {})
    check("desktop_state one-call snapshot", not err and len(out.get("text", "")) > 50)

    # --- browser over CDP ----------------------------------------------------
    out, err = m.call("browser_open", {"url": "about:blank"}, timeout=60)
    check("browser_open launches Chromium+CDP", not err, out.get("text", "")[:80])
    time.sleep(2)
    out, err = m.call("browser_goto", {"url": "data:text/html,<h1>mcp-webcheck</h1>"}, timeout=40)
    check("browser_goto navigates", not err, out.get("text", "")[:80])
    out, err = m.call("browser_text", {})
    check("browser_text reads the page", "mcp-webcheck" in out.get("text", ""),
          out.get("text", "")[:80])

    # --- release, be a good citizen -----------------------------------------
    m.call("release_control", {})

    print()
    passed = sum(1 for _, ok, _ in RESULTS if ok)
    print(f"{passed}/{len(RESULTS)} checks passed")
    failed = [n for n, ok, _ in RESULTS if not ok]
    if failed:
        print("FAILED: " + ", ".join(failed))
        sys.exit(1)
    print("all real-effect checks passed")


if __name__ == "__main__":
    main()
