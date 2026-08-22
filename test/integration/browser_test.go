// SentinelDesk
// A collaborative operating system for people and AI agents.
//
// Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
//
// Licensed under the Apache License, Version 2.0.
//
// This product's name and logo are trademarks of Federico Pereira and are not
// covered by the license above. See the README for the trademark policy.
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package integration

// The browser, over the DevTools Protocol.
//
// These are the hardest family to check through a different door, because CDP
// is the only way into a page — confirming browser_type with browser_eval is
// two tools sharing one connection. The way out is the window title: a page
// that mirrors its own state into document.title publishes it to the window
// manager, and wmctrl reads it without MCP being involved at all.
//
// That mirror is also what makes these tests worth having. browser_type used to
// assign el.value and fire a synthetic event, which any framework tracking its
// own value ignores; the page below counts what it actually received, so a tool
// that writes where the page cannot see is caught rather than believed.

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// pageHTML is a page that reports everything it notices in its title, so the
// window manager can be asked what happened instead of the browser.
const pageHTML = `<html><head><title>PROBE ready</title></head><body style="margin:0">
<h1 id="heading">Integration Probe</h1>
<p id="para">visible paragraph text</p>
<input id="field" type="text">
<button id="btn">Press Me</button>
<div id="late"></div>
<script>
var typed = 0, clicked = 0, trusted = 0;
function publish() {
  document.title = "PROBE t" + typed + " c" + clicked + " x" + trusted +
                   " v[" + document.getElementById("field").value + "]";
}
var f = document.getElementById("field");
// The listener a framework would use. A tool that assigns .value without the
// browser's own input pipeline never gets here.
f.addEventListener("input", function (e) { typed++; if (e.isTrusted) trusted++; publish(); });
document.getElementById("btn").addEventListener("click", function (e) {
  clicked++; if (e.isTrusted) trusted++; publish();
});
// Something that appears late, for the wait.
setTimeout(function () {
  var d = document.createElement("div");
  d.id = "appeared";
  d.textContent = "arrived late";
  document.getElementById("late").appendChild(d);
}, 2500);
publish();
</script></body></html>`

// openProbe writes the page into the container and opens it, returning nothing
// but leaving the browser on it.
func openProbe(t *testing.T) {
	t.Helper()
	writeProbe(t)
	devDesk(t).Call(t, "browser_open", map[string]any{"url": "file:///tmp/it-probe.html"})
	// The title is the channel everything else reads, so wait for it rather
	// than for the tool's reply.
	eventually(t, 20*time.Second, "the probe page's title to reach the window manager", func() bool {
		return strings.Contains(X(t, "wmctrl -l"), "PROBE")
	})
}

func writeProbe(t *testing.T) {
	t.Helper()
	// Written with a heredoc rather than through write_file: the page is the
	// instrument, and building it with a tool under test would make a broken
	// tool look like a broken page.
	Sh(t, "cat > /tmp/it-probe.html <<'ITPROBE'\n%s\nITPROBE", pageHTML)
}

// probeTitle is what the window manager currently shows, which is the page's
// own account of what it has received.
func probeTitle(t *testing.T) string {
	t.Helper()
	for _, line := range strings.Split(X(t, "wmctrl -l"), "\n") {
		if i := strings.Index(line, "PROBE"); i >= 0 {
			return line[i:]
		}
	}
	return ""
}

func TestBrowserOpen(t *testing.T) {
	control(t)
	writeProbe(t)
	Sh(t, "pkill -f 'chromium.*remote-debugging' 2>/dev/null || true")
	time.Sleep(1500 * time.Millisecond)

	out := devDesk(t).Call(t, "browser_open", map[string]any{"url": "file:///tmp/it-probe.html"})

	// It has to come back only once the page is really there — the reply says
	// "loaded", and the window manager is where that claim is checked.
	if !strings.Contains(X(t, "wmctrl -l"), "PROBE") {
		t.Fatalf("browser_open returned %q and no page window is on the display", trunc(out, 200))
	}
	// And the debugging port is answering, which is what the rest depends on.
	if !strings.Contains(Sh(t, `awk '$4=="0A"{split($2,a,":"); print a[2]}' /proc/net/tcp`), "2406") {
		t.Error("the browser is open and nothing is listening on 9222")
	}
}

func TestBrowserGoto(t *testing.T) {
	control(t)
	openProbe(t)

	// Somewhere else, then back. Navigating to the page you are already on
	// would pass for a tool that did nothing at all.
	Sh(t, "printf '<html><head><title>SECOND PAGE</title></head><body>second</body></html>' > /tmp/it-second.html")
	devDesk(t).Call(t, "browser_goto", map[string]any{"url": "file:///tmp/it-second.html"})

	eventually(t, 10*time.Second, "the window title to become the second page's", func() bool {
		return strings.Contains(X(t, "wmctrl -l"), "SECOND PAGE")
	})

	// A URL that cannot resolve has to be reported, not swallowed. This is the
	// case location.href used to hide behind a cheerful "navigating".
	devDesk(t).CallErr(t, "browser_goto", map[string]any{
		"url": "http://no-such-host-for-integration.invalid/", "timeout_ms": 8000})
}

func TestBrowserTabs(t *testing.T) {
	control(t)
	openProbe(t)

	out := devDesk(t).Call(t, "browser_tabs", nil)
	if !strings.Contains(out, "it-probe.html") {
		t.Fatalf("the open page is not among the tabs:\n%s", trunc(out, 300))
	}
}

func TestBrowserEval(t *testing.T) {
	control(t)
	openProbe(t)

	// Arithmetic the page could not have produced on its own, so the answer can
	// only have come from evaluating it there.
	out := devDesk(t).Call(t, "browser_eval", map[string]any{
		"expression": "String(6*7) + '-' + document.getElementById('heading').textContent"})
	if !strings.Contains(out, "42-Integration Probe") {
		t.Fatalf("browser_eval returned %q", trunc(out, 200))
	}
}

func TestBrowserText(t *testing.T) {
	control(t)
	openProbe(t)

	out := devDesk(t).Call(t, "browser_text", nil)
	for _, want := range []string{"Integration Probe", "visible paragraph text"} {
		if !strings.Contains(out, want) {
			t.Errorf("the page shows %q and browser_text does not have it:\n%s", want, trunc(out, 300))
		}
	}
	// Scoped to a selector, it must return only that element's text.
	out = devDesk(t).Call(t, "browser_text", map[string]any{"selector": "#para"})
	if strings.Contains(out, "Integration Probe") {
		t.Errorf("asked for #para and got the heading too: %s", trunc(out, 200))
	}
}

func TestBrowserType(t *testing.T) {
	control(t)
	openProbe(t)

	devDesk(t).Call(t, "browser_type", map[string]any{"selector": "#field", "text": "hola"})

	// The page's own count, read from the window title. A tool that set .value
	// without going through the browser's input layer would leave t0 here while
	// the field still showed the text — which is exactly what this used to do.
	eventually(t, 8*time.Second, "the page to notice the typing", func() bool {
		title := probeTitle(t)
		return strings.Contains(title, "v[hola]") && !strings.Contains(title, " t0 ")
	})
	if title := probeTitle(t); strings.Contains(title, " x0 ") {
		t.Errorf("the page saw the input as untrusted: %q", title)
	}
}

func TestBrowserClick(t *testing.T) {
	control(t)
	openProbe(t)

	devDesk(t).Call(t, "browser_click", map[string]any{"selector": "#btn"})

	eventually(t, 8*time.Second, "the page to count the click", func() bool {
		return !strings.Contains(probeTitle(t), " c0 ")
	})

	// A selector matching nothing has to fail rather than report a click.
	devDesk(t).CallErr(t, "browser_click", map[string]any{"selector": "#no-such-element"})
}

func TestBrowserWaitFor(t *testing.T) {
	control(t)
	openProbe(t)
	// The probe adds #appeared two and a half seconds after load, so reloading
	// puts the element reliably in the future.
	devDesk(t).Call(t, "browser_goto", map[string]any{"url": "file:///tmp/it-probe.html"})

	start := time.Now()
	devDesk(t).Call(t, "browser_wait_for", map[string]any{"selector": "#appeared", "timeout_ms": 15000})
	waited := time.Since(start)

	// It has to have actually waited — returning at once would mean it answered
	// about a page state that did not exist yet.
	if waited < time.Second {
		t.Errorf("returned after %v for an element that appears at 2.5s", waited)
	}
	// And the element really is there now.
	out := devDesk(t).Call(t, "browser_eval", map[string]any{
		"expression": "String(!!document.getElementById('appeared'))"})
	if !strings.Contains(out, "true") {
		t.Errorf("browser_wait_for returned and the element is not in the page")
	}
	// Something that never appears must time out rather than claim success.
	devDesk(t).CallErr(t, "browser_wait_for", map[string]any{
		"selector": "#never-going-to-exist", "timeout_ms": 2000})
}

func TestOpenAppAndWait(t *testing.T) {
	control(t)
	title := "OPENWAITWIN"
	start := time.Now()
	devDesk(t).Call(t, "open_app_and_wait", map[string]any{
		"command": fmt.Sprintf("xterm -T %s -e sleep 200", title),
		"match":   title, "timeout_ms": 20000})
	t.Cleanup(func() { X(t, "wmctrl -c %s 2>/dev/null || true", title) })

	// The point of this tool over launch_app is that it does not come back
	// until the window is there, so the window must already exist when it does
	// — with no waiting of our own in between.
	if !strings.Contains(X(t, "wmctrl -l"), title) {
		t.Fatalf("open_app_and_wait returned after %v and the window is not on the display", time.Since(start))
	}
}
