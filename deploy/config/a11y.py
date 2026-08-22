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
"""AT-SPI bridge for the MCP's ui_* tools.

Queries and drives the desktop's accessibility tree: which widgets exist, what
they are called, where they are and which actions they accept. This is what lets
applications be operated by their STRUCTURE — invoking the "Save" button —
instead of guessing with screenshots and clicks at coordinates.

Every element is identified by a `ref`: the path of indices from the desktop,
for example "2/0/3/1". It stays stable as long as the window does not change
its structure, and it is cheap to resolve.

Sub-commands (JSON on stdout):
    tree     [--app NAME] [--depth N] [--interactive]
    find     [--role R] [--name N] [--text T] [--app A] [--limit N]
    click    --ref R [--action click]
    settext  --ref R --text T
    gettext  --ref R
    focus    --ref R
    waitfor  [--role R] [--name N] [--text T] [--timeout-ms N]
"""

import argparse
import json
import sys
import time

try:
    import pyatspi
except ImportError:
    print(json.dumps({"error": "pyatspi is not installed"}))
    sys.exit(1)


def safe(fn, default=None):
    try:
        return fn()
    except Exception:
        return default


def label_of(obj):
    """The text of whatever labels this element, through AT-SPI's relation."""
    relations = safe(obj.getRelationSet, []) or []
    for rel in relations:
        if safe(rel.getRelationType) != pyatspi.RELATION_LABELLED_BY:
            continue
        n = safe(rel.getNTargets, 0) or 0
        for i in range(n):
            target = safe(lambda: rel.getTarget(i))
            if target is None:
                continue
            name = safe(lambda: target.name or "", "")
            if name:
                return name
    return ""


def describe(obj, ref):
    """One element: role, name, state, geometry and actions."""
    info = {
        "ref": ref,
        "role": safe(obj.getRoleName, "?"),
        "name": safe(lambda: obj.name or "", ""),
    }
    desc = safe(lambda: obj.description or "", "")
    if desc:
        info["description"] = desc

    comp = safe(obj.queryComponent)
    if comp:
        ext = safe(lambda: comp.getExtents(pyatspi.DESKTOP_COORDS))
        if ext and (ext.width or ext.height):
            info.update({"x": ext.x, "y": ext.y, "width": ext.width, "height": ext.height,
                         "center_x": ext.x + ext.width // 2,
                         "center_y": ext.y + ext.height // 2,
                         # Said out loud because it is not what the constant
                         # promises. DESKTOP_COORDS returns window-relative
                         # values here: a dialog the window manager places at
                         # (805, 429) reports (0, 0), and still does after being
                         # moved. Anything wanting a screen coordinate has to add
                         # the window's own origin, which list_windows knows and
                         # this bridge does not.
                         "coords": "window-relative"})

    act = safe(obj.queryAction)
    if act:
        names = safe(lambda: [act.getName(i) for i in range(act.nActions)], [])
        if names:
            info["actions"] = names

    txt = safe(obj.queryText)
    if txt:
        content = safe(lambda: txt.getText(0, 200), "")
        if content and content.strip():
            info["text"] = content.strip()

    val = safe(obj.queryValue)
    if val:
        info["value"] = safe(lambda: val.currentValue)

    # The label a toolkit put next to this element rather than inside it.
    #
    # GTK does not name an entry after the caption beside it: the caption is a
    # separate label object, and the two are joined by a LABELLED_BY relation.
    # So a form whose field reads "Name:" on screen has an entry whose own name
    # is the empty string, and anything matching by name finds the label — an
    # element with no editable text, which is the wrong half of the pair.
    #
    # Exposing it here means one lookup makes every caller able to address a
    # field the way a person would describe it, instead of each of them
    # rediscovering the relation.
    label = safe(lambda: label_of(obj), "")
    if label:
        info["label"] = label

    states = safe(lambda: obj.getState().getStates(), [])
    flags = []
    for st, label in ((pyatspi.STATE_FOCUSED, "focused"),
                      (pyatspi.STATE_SELECTED, "selected"),
                      (pyatspi.STATE_CHECKED, "checked"),
                      (pyatspi.STATE_ENABLED, "enabled"),
                      (pyatspi.STATE_VISIBLE, "visible"),
                      (pyatspi.STATE_SHOWING, "showing"),
                      (pyatspi.STATE_EDITABLE, "editable")):
        if st in states:
            flags.append(label)
    if flags:
        info["state"] = flags
    return info


# The roles that almost always matter when driving an interface.
INTERACTIVE_ROLES = {
    "push button", "button", "toggle button", "radio button", "check box",
    "menu item", "check menu item", "radio menu item", "menu", "combo box",
    "text", "entry", "password text", "link", "list item", "table cell",
    "tab", "page tab", "slider", "spin button", "tree item", "icon",
}


def walk(obj, ref, depth, max_depth, out, interactive_only=False):
    if depth > max_depth:
        return
    info = describe(obj, ref)
    role = info.get("role", "")
    keep = True
    if interactive_only and depth > 1:
        # Keep whatever is actionable, or carries useful text or a name
        keep = (role in INTERACTIVE_ROLES or "actions" in info
                or bool(info.get("text")) or bool(info.get("name")))
    if keep:
        out.append(info)
    for i, child in enumerate(obj):
        if child is None:
            continue
        walk(child, f"{ref}/{i}" if ref else str(i), depth + 1, max_depth, out,
             interactive_only)


def resolve(ref):
    """Turns "2/0/3" into the matching object."""
    node = pyatspi.Registry.getDesktop(0)
    for part in str(ref).split("/"):
        if part == "":
            continue
        node = node[int(part)]
    return node


def apps(app_filter=None):
    desktop = pyatspi.Registry.getDesktop(0)
    for i, app in enumerate(desktop):
        if app is None:
            continue
        name = safe(lambda: app.name or "", "")
        if app_filter and app_filter.lower() not in name.lower():
            continue
        yield i, app


def cmd_tree(args):
    out = []
    for i, app in apps(args.app):
        walk(app, str(i), 1, args.depth, out, args.interactive)
    return {"count": len(out), "elements": out[:args.limit]}


def matches(info, args):
    if args.role and args.role.lower() not in info.get("role", "").lower():
        return False
    if args.name:
        # The label counts as a name. Without this a GTK form is unaddressable
        # by the words printed on it: the entry is nameless and the caption
        # beside it belongs to a different object.
        haystack = (info.get("name", "") + " " + info.get("label", "")).lower()
        if args.name.lower() not in haystack:
            return False
    if args.text and args.text.lower() not in (info.get("text", "") + info.get("name", "")).lower():
        return False
    return True


def cmd_find(args):
    found = []
    for i, app in apps(args.app):
        collected = []
        walk(app, str(i), 1, args.depth, collected, False)
        for info in collected:
            if matches(info, args):
                found.append(info)
                if len(found) >= args.limit:
                    return {"count": len(found), "elements": found}
    return {"count": len(found), "elements": found}


def locate(args, prefer=None):
    """The object to act on: an explicit ref, or the best match for a name.

    fill_form has been calling `settext --name X` since it was written, and
    settext only ever accepted --ref. argparse refused the call, exited 2 with
    an empty stdout, and the Go side reported the empty output as a failed
    field — so the tool has never filled anything, and its `submit` never
    clicked anything either. The fix is to make the verbs addressable the way
    every caller already assumed they were.

    `prefer` picks between candidates when a name matches more than one. It
    matters for exactly the case that motivated the labelled-by lookup: asking
    for "Name" in a GTK dialog finds both the caption and the entry it labels,
    and only one of them can be typed into.
    """
    if getattr(args, "ref", None):
        return resolve(args.ref), args.ref, None
    name = getattr(args, "name", None)
    if not name:
        return None, "", {"error": "give either --ref or --name"}

    # An explicit filter rather than args itself. settext's --text is the value
    # to write, and handing that to the search would look for an element that
    # already contains what is about to be typed into it — which matches
    # nothing on the first run and the wrong thing on the second.
    query = argparse.Namespace(
        app=getattr(args, "app", None),
        depth=getattr(args, "depth", 12),
        limit=getattr(args, "limit", 200),
        role=getattr(args, "role", None),
        name=name,
        text=None,
    )
    found = cmd_find(query)["elements"]
    if not found:
        return None, "", {"error": f"nothing matches --name {name!r}",
                          "hint": "ui_tree shows what is there and what its role is called"}
    if prefer:
        for info in found:
            if prefer(info):
                return resolve(info["ref"]), info["ref"], None
    return resolve(found[0]["ref"]), found[0]["ref"], None


def editable(info):
    return "editable" in info.get("state", []) or info.get("role") in (
        "text", "entry", "password text", "spin button", "combo box")


def actionable(info):
    return bool(info.get("actions"))


def cmd_click(args):
    obj, ref, err = locate(args, prefer=actionable)
    if err:
        return err
    try:
        act = obj.queryAction()
    except Exception:
        return {"error": "the element has no actions to perform"}
    names = [act.getName(i) for i in range(act.nActions)]
    idx = 0
    if args.action:
        for i, n in enumerate(names):
            if args.action.lower() in n.lower():
                idx = i
                break
        else:
            return {"error": f"action {args.action!r} is not available", "actions": names}
    act.doAction(idx)
    return {"ok": True, "action": names[idx] if names else "", "ref": ref}


def cmd_settext(args):
    obj, ref, err = locate(args, prefer=editable)
    if err:
        return err
    try:
        editable_iface = obj.queryEditableText()
    except Exception:
        return {"error": "the element is not editable",
                "hint": "with --name this may have matched the caption rather "
                        "than the field it labels; ui_tree shows both"}
    editable_iface.setTextContents(args.text)
    return {"ok": True, "ref": ref, "chars": len(args.text)}


def cmd_gettext(args):
    obj = resolve(args.ref)
    info = describe(obj, args.ref)
    # describe() caps text at 200 characters to keep the tree small, which is
    # right for a label and useless for a terminal. Read it again in full here.
    text = info.get("text", "")
    txt = safe(obj.queryText)
    if txt:
        n = safe(lambda: txt.characterCount, 0) or 0
        if n:
            full = safe(lambda: txt.getText(0, min(n, args.max_chars)), "")
            if full:
                text = full
    return {"ref": args.ref, "text": text, "name": info.get("name", ""),
            "role": info.get("role", "")}


def cmd_atpoint(args):
    """What is at a point inside a window, found by descending rather than walking.

    Two coordinate systems, and the split is the whole design.

    X owns where a window is. AT-SPI claims to as well, and on this desktop it
    is wrong: a zenity dialog the window manager reports at (805, 429) reports
    itself at (0, 0) through DESKTOP_COORDS, and still says (0, 0) after being
    moved to (100, 200). Not staleness — it never knew. So desktop coordinates
    from this bridge cannot be trusted, and the caller resolves the window with
    EWMH (which is correct) and passes a point already relative to it.

    Inside the window AT-SPI is reliable, and WINDOW_COORDS descent is O(depth):
    each element is asked which of its children holds the point. The walk that
    ui_find and ui_tree do is O(tree) — every application, every widget, three
    quarters of a second and up to twenty thousand tokens to answer one
    question. The Component interface exists precisely so a screen reader can
    follow a pointer without doing that.

    The ancestor chain comes back with the element, because "the button named
    Save" is ambiguous across three open applications and "Save, in the dialog
    Export, in GIMP" is not.
    """
    x, y = args.x, args.y
    want_app = (args.app or "").lower()

    best = None
    for i, app in enumerate(pyatspi.Registry.getDesktop(0)):
        if app is None:
            continue
        if want_app and want_app not in (safe(lambda: app.name, "") or "").lower():
            continue
        for j, win in enumerate(app):
            if win is None:
                continue
            comp = safe(win.queryComponent)
            if not comp:
                continue
            # Descend from this window. A window that does not hold the point
            # yields nothing on the first step, which is how the right one is
            # picked when an application has several.
            chain = [describe(win, f"{i}/{j}")]
            node, ref = win, f"{i}/{j}"
            for _ in range(args.max_depth):
                c = safe(node.queryComponent)
                if not c:
                    break
                child = safe(lambda: c.getAccessibleAtPoint(x, y, pyatspi.WINDOW_COORDS))
                if child is None:
                    break
                # getIndexInParent, not a scan comparing with ==. Two wrappers
                # around the same accessible do not compare equal, so the scan
                # never matched, the walk stopped at the first step, and every
                # lookup reported that nothing was there — a wrong answer that
                # looked exactly like a correct one about an empty point.
                idx = safe(child.getIndexInParent, -1)
                if idx is None or idx < 0:
                    break
                ref = f"{ref}/{idx}"
                node = child
                chain.append(describe(node, ref))
            # The deepest answer wins: a point inside a button is also inside
            # the panel and the frame that contain it, and the button is what
            # was asked about.
            if len(chain) > 1 and (best is None or len(chain) > len(best)):
                best = chain

    if not best:
        return {"found": False, "x": x, "y": y,
                "hint": "nothing accessible is at that point in that window — "
                        "the toolkit may not expose an accessibility tree "
                        "(Chromium content is a common case; use browser_* there)"}

    return {"found": True, "x": x, "y": y,
            "element": best[-1], "ancestors": best[:-1]}


def cmd_focus(args):
    obj, ref, err = locate(args)
    if err:
        return err
    ok = False
    comp = safe(obj.queryComponent)
    if comp:
        ok = bool(safe(comp.grabFocus, False))
    return {"ok": ok, "ref": ref}


# The events that can bring a matching element into existence. children-changed
# covers a widget being added, state-changed covers one that existed but was not
# showing, and the window and document ones catch the wholesale replacements —
# a dialog opening, a page finishing — where the individual node events arrive
# too fast to be worth counting.
WAIT_EVENTS = (
    "object:children-changed",
    "object:state-changed",
    "window:activate",
    "window:create",
    "document:load-complete",
)

# How long to let a burst settle before searching. An application drawing itself
# emits hundreds of children-changed in a few milliseconds, and searching on
# each one would be slower than the polling this replaces. Coalescing them into
# one search after the burst keeps the cost proportional to what happened rather
# than to how loudly it was announced.
WAIT_DEBOUNCE_MS = 120


def cmd_waitfor(args):
    """Wait for an element by listening, not by asking.

    This used to walk every application's accessibility tree four times a
    second and filter the result. Each node in that walk is a D-Bus round trip,
    so waiting fifteen seconds for a dialog meant sixty full traversals of every
    open application to be told fifty-nine times that nothing had changed — and
    on a desktop where nothing is happening, every one of those was wasted.

    AT-SPI already announces the changes the walk was looking for. Registering
    for them means a still desktop costs nothing at all, and an element that
    appears is found as it appears rather than up to 250ms afterwards.
    """
    start = time.time()

    # Look before listening. Between deciding to wait and the listener being
    # live there is a gap in which the element can appear, and a wait that
    # registered first would sleep through it to the timeout.
    res = cmd_find(args)
    if res["count"]:
        return {"found": True, "elements": res["elements"][:3],
                "waited_ms": 0, "via": "already present"}

    from gi.repository import GLib

    result = {}
    pending = [False]

    # search only reports; stopping the loop is the caller's business, because
    # the last call happens after the loop has already ended and stopping a
    # registry that is not running is not something to find out in production.
    def search(via):
        found = cmd_find(args)
        if not found["count"]:
            return False
        result.update({"found": True, "elements": found["elements"][:3],
                       "waited_ms": int((time.time() - start) * 1000), "via": via})
        return True

    def debounced():
        pending[0] = False
        if search("event"):
            pyatspi.Registry.stop()
        return False  # one shot

    def on_event(_event):
        # Schedule rather than drop. Ignoring events while one is pending would
        # lose the last of a burst, which is the one most likely to be the
        # change being waited for.
        if not pending[0]:
            pending[0] = True
            GLib.timeout_add(WAIT_DEBOUNCE_MS, debounced)

    def on_timeout():
        pyatspi.Registry.stop()
        return False

    pyatspi.Registry.registerEventListener(on_event, *WAIT_EVENTS)
    GLib.timeout_add(int(args.timeout_ms), on_timeout)
    try:
        pyatspi.Registry.start()
    finally:
        try:
            pyatspi.Registry.deregisterEventListener(on_event, *WAIT_EVENTS)
        except Exception:
            pass

    if result:
        return result

    # One last look. The loop can end with a debounce still outstanding, and an
    # element that arrived in those final milliseconds is there whether or not
    # anything got round to noticing.
    if search("final check"):
        return result
    return {"found": False, "error": "timed out waiting for the element",
            "waited_ms": int((time.time() - start) * 1000)}


def main():
    ap = argparse.ArgumentParser(description="AT-SPI bridge for the ui_* tools")
    sub = ap.add_subparsers(dest="cmd", required=True)

    def common(p, with_filters=True):
        p.add_argument("--app")
        p.add_argument("--depth", type=int, default=12)
        p.add_argument("--limit", type=int, default=200)
        if with_filters:
            p.add_argument("--role")
            p.add_argument("--name")
            p.add_argument("--text")

    p = sub.add_parser("tree")
    common(p, with_filters=False)
    p.add_argument("--interactive", action="store_true")

    p = sub.add_parser("find")
    common(p)

    # --ref or --name, for the three verbs that act on one element. Not
    # required=True on --ref any more: that is what made `settext --name X`
    # exit 2 before it reached any of this.
    def addressable(p):
        p.add_argument("--ref")
        common(p, with_filters=False)
        p.add_argument("--role")
        p.add_argument("--name")

    p = sub.add_parser("click")
    addressable(p)
    p.add_argument("--action")

    p = sub.add_parser("settext")
    addressable(p)
    p.add_argument("--text", required=True)

    p = sub.add_parser("gettext")
    p.add_argument("--ref", required=True)
    p.add_argument("--max-chars", type=int, default=20000)

    p = sub.add_parser("focus")
    addressable(p)

    p = sub.add_parser("atpoint")
    p.add_argument("--x", type=int, required=True)   # relative to the window
    p.add_argument("--y", type=int, required=True)
    p.add_argument("--app")
    p.add_argument("--max-depth", type=int, default=25)

    p = sub.add_parser("waitfor")
    common(p)
    p.add_argument("--timeout-ms", type=int, default=15000)

    args = ap.parse_args()
    handlers = {"tree": cmd_tree, "find": cmd_find, "click": cmd_click,
                "settext": cmd_settext, "gettext": cmd_gettext,
                "focus": cmd_focus, "waitfor": cmd_waitfor,
                "atpoint": cmd_atpoint}
    try:
        print(json.dumps(handlers[args.cmd](args), ensure_ascii=False))
    except Exception as exc:
        print(json.dumps({"error": f"{type(exc).__name__}: {exc}"}))
        sys.exit(1)


if __name__ == "__main__":
    main()
