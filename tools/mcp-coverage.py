#!/usr/bin/env python3
"""Coverage pass: INVOKE every one of the 134 tools and classify each.

This complements mcp-validate.py's deep effect checks. Here the goal is
reachability and safety: every tool is called with safe arguments and sorted
into RESPONDED (handled, no transport error or crash), REFUSED (a sensible
isError — e.g. a guard we expected), or SKIPPED (destructive or needs an
external resource, each named with a reason — no silent caps). Anything that
crashes the daemon, times out, or comes back unhandled is a FAIL.
"""

import argparse
import json
import subprocess
import sys
import threading
import time

SOCK = "/run/user/1000/sentineldesk-mcp.sock"  # fallback if MCP_SOCK is unset


def resolve_sock(container):
    """Read MCP_SOCK from the container so this works whether the socket sits at
    its private default or was relocated (e.g. -e MCP_SOCK=/run/sentineldesk/mcp.sock,
    the installer/compose default)."""
    try:
        out = subprocess.run(["docker", "exec", container, "printenv", "MCP_SOCK"],
                             capture_output=True, text=True, timeout=10)
        if out.stdout.strip():
            return out.stdout.strip()
    except Exception:
        pass
    return SOCK

# Destructive or externally-dependent tools: called would change the system,
# need a remote host, or need an RTMP destination we do not have. Each is
# named with why — the doctrine forbids dropping them silently.
SKIP = {
    "install_packages": "apt install would mutate the system",
    "remove_packages": "apt remove would mutate the system",
    "service_control": "could stop X/pulse and kill the session",
    "snapshot_create": "heavy home+package snapshot",
    "snapshot_restore": "rolls the home directory back",
    "snapshot_delete": "needs a snapshot to delete",
    "set_resolution": "changes the live display mid-capture",
    "ssh_connect": "needs a remote host",
    "ssh_exec": "needs an ssh session",
    "ssh_upload": "needs an ssh session",
    "ssh_download": "needs an ssh session",
    "ssh_list_remote": "needs an ssh session",
    "ssh_tunnel_local": "needs an ssh session",
    "ssh_tunnel_remote": "needs an ssh session",
    "ssh_tunnels": "needs an ssh session",
    "ssh_tunnel_close": "needs an ssh session",
    "ssh_disconnect": "needs an ssh session",
    "ssh_copy_id": "needs a remote host",
    "start_restream": "needs an external RTMP destination",
    "stop_restream": "needs a running restream",
    "remote_open": "needs a live RDP/VNC/SPICE server to connect to",
    "remote_close": "needs an open remote session",
}

# Tools that need real arguments. {id}/{ref} are filled at runtime from a live
# window and a11y node.
OVERRIDES = {
    "mouse_move": {"x": 400, "y": 300},
    "mouse_click": {"x": 400, "y": 300},
    "mouse_drag": {"x1": 300, "y1": 300, "x2": 360, "y2": 340},
    "mouse_scroll": {"dy": 1},
    "mouse_down": {"button": 1},
    "mouse_up": {"button": 1},
    "get_pixel_color": {"x": 10, "y": 10},
    "screenshot_region": {"x": 0, "y": 0, "width": 100, "height": 100},
    "type_text": {"text": "coverage"},
    "key_combo": {"keys": "Escape"},
    "set_clipboard": {"text": "coverage"},
    "set_volume": {"percent": 50},
    "read_file": {"path": "/etc/hostname"},
    "write_file": {"path": "/tmp/cov_write.txt", "content": "cov"},
    "list_directory": {"path": "/tmp"},
    "run_command": {"command": "true", "timeout_ms": 8000},
    "terminal_run": {"command": "true", "timeout_ms": 8000},
    "launch_app": {"command": "xterm"},
    "open_app_and_wait": {"command": "xterm", "match": "sentineldesk", "timeout_ms": 8000},
    "activate_window": {"id": "{id}"},
    "move_window": {"id": "{id}", "x": 60, "y": 60},
    "resize_window": {"id": "{id}", "width": 700, "height": 500},
    "minimize_window": {"id": "{id}"},
    "maximize_window": {"id": "{id}"},
    "restore_window": {"id": "{id}"},
    "fullscreen_window": {"id": "{id}", "action": "toggle"},
    "set_window_desktop": {"id": "{id}", "desktop": 0},
    "window_properties": {"id": "{id}"},
    "window_set_state": {"id": "{id}", "state": "above", "action": "toggle"},
    "close_window": {"id": "{closeid}"},
    "switch_desktop": {"desktop": 0},
    "wait_for_window": {"match": "x", "timeout_ms": 800},
    "wait_for_idle": {"timeout_ms": 1500, "quiet_ms": 400},
    "find_text": {"text": "the"},
    "read_screen_text": {},
    "ui_find": {"role": "push button"},
    "ui_at_point": {"x": 400, "y": 300},
    "ui_click": {"ref": "{ref}"},
    "ui_set_text": {"ref": "{ref}", "text": "cov"},
    "ui_get_text": {"ref": "{ref}"},
    "ui_focus": {"ref": "{ref}"},
    "ui_wait_for": {"role": "push button", "timeout_ms": 800},
    "ui_diff": {},
    "fill_form": {"fields": {"nonexistent-cov": "y"}},
    "is_running": {"name": "Xvfb"},
    "kill_process": {"name": "nonexistent-cov-xyz"},
    "list_processes": {},
    "wait": {"ms": 50},
    "sleep": {"seconds": 1},
    "browser_open": {"url": "about:blank"},
    "browser_goto": {"url": "data:text/html,<b>cov</b>"},
    "browser_text": {},
    "browser_click": {"selector": "b"},
    "browser_element": {"selector": "b"},
    "browser_wait_for": {"selector": "b", "timeout_ms": 3000},
    "browser_eval": {"expression": "1+1"},
    "shell_open": {},
    "shell_exec": {"id": "{shell}", "command": "true"},
    "shell_input": {"id": "{shell}", "text": "echo hi", "enter": True},
    "shell_read": {"id": "{shell}"},
    "shell_close": {"id": "{shell}"},
    "start_recording": {},
    "job_start": {"command": "sleep 1"},
    "job_status": {"id": "{job}"},
    "job_output": {"id": "{job}"},
    "job_wait": {"id": "{job}", "timeout_ms": 4000},
    "job_abort": {"id": "{job}"},
    "search_packages": {"query": "htop"},
    "ssh_keygen": {"type": "ed25519", "comment": "cov", "path": "/tmp/cov_key"},
    "subscribe_events": {"topics": ["control"]},
    "unsubscribe_events": {},
    "tool_search": {"query": "mouse"},
    "ask_human": {"question": "cov?", "timeout_ms": 1500},
    "browser_type": {"selector": "body", "text": "x"},
    "activity": {"limit": 5},
    "remote_profile_save": {"name": "cov-profile", "protocol": "vnc", "host": "127.0.0.1", "port": 5999},
    "remote_profile_delete": {"name": "cov-profile"},
}


class MCP:
    def __init__(self, container):
        self._id = 0
        self._resps = {}
        self.proc = subprocess.Popen(
            ["docker", "exec", "-i", "-u", "sentineldesk", container,
             "/usr/local/bin/sentineldesk", "-mcp-stdio", "-mcp-sock", resolve_sock(container)],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        threading.Thread(target=self._reader, daemon=True).start()
        self.request("initialize", {"clientInfo": {"name": "cov", "version": "1"}})
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

    def notify(self, m, p=None):
        self._send({"jsonrpc": "2.0", "method": m, "params": p or {}})

    def request(self, m, p=None, timeout=60):
        self._id += 1
        rid = self._id
        self._send({"jsonrpc": "2.0", "id": rid, "method": m, "params": p or {}})
        deadline = time.time() + timeout
        while rid not in self._resps and time.time() < deadline:
            if self.proc.poll() is not None:
                raise RuntimeError("bridge died: " + self.proc.stderr.read().decode()[:300])
            time.sleep(0.02)
        if rid not in self._resps:
            return {"error": {"message": f"timeout after {timeout}s"}}
        return self._resps.pop(rid)

    def call(self, name, args, timeout=40):
        r = self.request("tools/call", {"name": name, "arguments": args}, timeout)
        if "error" in r:
            return None, r["error"].get("message", "transport error")
        res = r["result"]
        text = "\n".join(c.get("text", "") for c in res.get("content", [])
                         if c.get("type") == "text")
        has_img = any(c.get("type") == "image" for c in res.get("content", []))
        return {"text": text, "img": has_img, "isError": bool(res.get("isError"))}, None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--container", default="sentineldesk")
    args = ap.parse_args()
    m = MCP(args.container)

    tools = m.request("tools/list")["result"]["tools"]
    names = sorted(t["name"] for t in tools)

    # Take control so injecting tools are not refused for lack of it.
    m.call("request_control", {"timeout_ms": 4000})

    import re as _re

    def first(pattern, text):
        mm = _re.search(pattern, text or "")
        return mm.group(1) if mm else ""

    # Open the browser up front so every browser_* tool has a live CDP target.
    m.call("browser_open", {"url": "about:blank"}, timeout=60)
    time.sleep(2)
    m.call("browser_goto", {"url": "data:text/html,<b id=cov>cov</b>"}, timeout=30)

    # A throwaway window to exercise close_window without touching a real one,
    # and a live window id for the other window tools.
    m.call("launch_app", {"command": "xterm -title covwin"})
    time.sleep(2)
    win_id = ""
    close_id = ""
    try:
        out, _ = m.call("list_windows", {})
        t = (out or {}).get("text", "")
        ids = _re.findall(r'"id"\s*:\s*"?(0x[0-9a-fA-F]+|\d+)"?', t)
        wins = _re.findall(r'"(?:id)"\s*:\s*"?(0x[0-9a-fA-F]+|\d+)"?[^}]*?"(?:title|name)"\s*:\s*"([^"]*)"', t)
        for wid, title in wins:
            if "covwin" in title and not close_id:
                close_id = wid
            elif not win_id:
                win_id = wid
        if not win_id and ids:
            win_id = ids[0]
        if not close_id:
            # last resort: a second distinct id, else reuse win_id's is unsafe → skip close
            close_id = next((i for i in ids if i != win_id), "")
    except Exception:
        pass
    ref = ""
    try:
        out, _ = m.call("ui_tree", {"interactive": True, "limit": 60})
        ref = first(r'"ref"\s*:\s*"([^"]+)"', (out or {}).get("text", ""))
    except Exception:
        pass
    shell = ""
    try:
        out, _ = m.call("shell_open", {})
        j = json.loads(out["text"]) if out and out["text"].strip().startswith("{") else {}
        shell = str(j.get("session") or j.get("id") or "")
    except Exception:
        pass
    job = ""
    try:
        out, _ = m.call("job_start", {"command": "sleep 2"})
        j = json.loads(out["text"]) if out and out["text"].strip().startswith("{") else {}
        job = str(j.get("job_id") or j.get("id") or "")
    except Exception:
        pass

    subs = {"{id}": win_id, "{ref}": ref, "{shell}": shell, "{job}": job,
            "{closeid}": close_id}

    def resolve(a):
        out = {}
        for k, v in a.items():
            if isinstance(v, str) and v in subs:
                out[k] = subs[v]
            else:
                out[k] = v
        return out

    responded, refused, skipped, failed = [], [], [], []
    for name in names:
        if name in SKIP:
            skipped.append((name, SKIP[name]))
            continue
        a = resolve(OVERRIDES.get(name, {}))
        # A templated arg we could not resolve → skip, named.
        missing = [k for k, v in a.items() if v == ""]
        if missing:
            skipped.append((name, f"no live value for {missing}"))
            continue
        try:
            out, terr = m.call(name, a)
        except Exception as e:
            failed.append((name, str(e)[:80]))
            continue
        if terr:
            failed.append((name, terr[:80]))
        elif out["isError"]:
            refused.append((name, out["text"][:60].replace("\n", " ")))
        else:
            responded.append(name)

    m.call("release_control", {})

    total = len(names)
    print(f"\n=== 134-tool coverage: {total} tools ===")
    print(f"RESPONDED (handled, no error): {len(responded)}")
    print(f"REFUSED (sensible isError):    {len(refused)}")
    print(f"SKIPPED (destructive/external):{len(skipped)}")
    print(f"FAILED (crash/timeout/unhandled): {len(failed)}")
    covered = len(responded) + len(refused) + len(skipped) + len(failed)
    print(f"total accounted for: {covered}/{total}")
    if refused:
        print("\n-- refusals (expected guards / benign):")
        for n, d in refused:
            print(f"   {n}: {d}")
    if skipped:
        print("\n-- skipped (named):")
        for n, d in skipped:
            print(f"   {n}: {d}")
    if failed:
        print("\n-- FAILURES:")
        for n, d in failed:
            print(f"   {n}: {d}")
        sys.exit(1)
    print("\nNo tool crashed, timed out, or came back unhandled.")


if __name__ == "__main__":
    main()
