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

package mcp

// The ui_* tools operate the desktop through the interface's STRUCTURE (AT-SPI)
// rather than through pixels. Knowing which widgets exist, what they are called
// and what they accept,
// and invoke their actions directly: no screenshots, no OCR, no coordinates.
//
// The AT-SPI query itself lives in a11y.py (pyatspi); this file exposes it as
// tools and normalises the JSON.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const a11yScript = "/usr/local/bin/a11y.py"
const sessionBus = "unix:path=/run/user/1000/bus"

func (s *Server) buildUITools() []toolDef {
	return []toolDef{
		{
			Name:       "ui_at_point",
			Risk:       riskRead,
			Visibility: visHidden,
			Description: "What is at these screen coordinates: the element, and the " +
				"chain of things containing it. Use this after a screenshot, when you " +
				"can see something and need its ref — it descends to the point instead " +
				"of walking every window, so it costs a fraction of ui_tree and answers " +
				"in one call rather than a search. The coordinates are the screen's, the " +
				"same ones screenshot and get_mouse_position use. Returns nothing useful " +
				"inside a browser page, which has its own tree: use browser_text there.",
			InputSchema: schema(map[string]any{
				"x": pInt("screen x"),
				"y": pInt("screen y"),
			}, "x", "y"),
		},
		{
			Name:        "ui_tree",
			Risk:        riskRead,
			Description: "Read the ACCESSIBILITY TREE of the desktop: every window and widget with its role, name, text, state, screen coordinates and the actions it accepts. This is how you SEE what is on screen as structured data instead of taking a screenshot and guessing. Prefer this over `screenshot` whenever you need to operate an application. Use interactive=true to keep only the parts you can act on.",
			InputSchema: schema(map[string]any{
				"app":         pStr("only this application (substring of its name)"),
				"interactive": pBool("keep only actionable/labelled elements (recommended)"),
				"depth":       pIntDef("max depth (default 12)", 12),
				"limit":       pIntDef("max elements (default 200)", 200),
			}),
		},
		{
			Name: "ui_find",
			Risk: riskRead,
			// Measured at ~770ms against a real desktop (stage 1 §11.1). Ten
			// seconds is far past anything healthy and short enough that a
			// hung accessibility bridge is noticed rather than waited on.
			TimeoutMS:   10000,
			Description: "Find UI elements by role, name or text — e.g. the button called 'Sign in', or every text entry. Returns each match with its `ref` (use it with ui_click / ui_set_text) plus its screen coordinates. This replaces OCR + find_text for real applications.",
			InputSchema: schema(map[string]any{
				"role": pStr("role as the TOOLKIT reports it — run ui_tree first to see the " +
					"actual names, which are not always the obvious ones: a GTK dialog's " +
					"button is 'button' and its text field is 'text', while a Chromium " +
					"button may be 'toggle button'"),
				"name": pStr("accessible name (substring, case-insensitive)"),
				"text": pStr("visible text (substring)"),
				"app":  pStr("restrict to this application"),
				// 200, not 20. The description said 20 for as long as it existed
				// and the bridge has always applied 200 — a11y.py gives `find`
				// the same --limit default as `tree`. The TEXT was corrected
				// rather than the code: 200 is what this tool has always
				// returned, and narrowing it now would silently start dropping
				// matches an agent may be relying on, to make a sentence true.
				"limit": pIntDef("max results (default 200)", 200),
			}),
		},
		{
			Name:            "ui_click",
			Visibility:      visInjects,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Invoke an element's action DIRECTLY by its ref (from ui_find/ui_tree) — presses the button, opens the menu, toggles the checkbox. The pointer never moves, so it cannot miss and it does not matter if the window is partly covered.",
			InputSchema: schema(map[string]any{
				"ref":    pStr("element ref, e.g. '2/0/3/1'"),
				"action": pStr("action name if the element has several (default: the first, usually 'click')"),
			}, "ref"),
		},
		{
			Name:            "ui_set_text",
			Visibility:      visInjects,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Write text straight into an editable field by ref, replacing its content. Unlike type_text this does not depend on which window has focus.",
			InputSchema: schema(map[string]any{
				"ref": pStr("element ref of the entry/text field"), "text": pStr("text to set"),
			}, "ref", "text"),
		},
		{
			Name:        "ui_get_text",
			Risk:        riskRead,
			Description: "Read the text/label of an element by ref (no OCR involved).",
			InputSchema: schema(map[string]any{"ref": pStr("element ref")}, "ref"),
		},
		{
			Name:            "ui_focus",
			Visibility:      visInjects,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Give keyboard focus to an element by ref (then type_text goes where you want).",
			InputSchema:     schema(map[string]any{"ref": pStr("element ref")}, "ref"),
		},
		{
			Name:        "ui_wait_for",
			Risk:        riskRead,
			Description: "Wait until a UI element matching role/name/text exists — the reliable way to wait for a dialog, a page or a button to appear, instead of guessing a `wait` duration.",
			InputSchema: schema(map[string]any{
				"name": pStr("accessible name (substring)"), "role": pStr("role"),
				"text": pStr("visible text"), "app": pStr("restrict to this application"),
				"timeout_ms": pIntDef("timeout, default 15000", 15000),
			}),
		},
	}
}

// a11yRaw invokes the accessibility bridge with the right environment — display
// and session bus — and returns its raw output, for the tools that process the
// JSON themselves (ui_diff, fill_form) rather than passing it straight through.
func (s *Server) a11yRaw(args ...string) (string, error) {
	cmd := exec.Command("python3", append([]string{a11yScript}, args...)...)
	cmd.Env = append(os.Environ(),
		"DISPLAY="+s.display,
		"XDG_RUNTIME_DIR=/run/user/1000",
		"DBUS_SESSION_BUS_ADDRESS="+sessionBus)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return "", fmt.Errorf("a11y %s: %v", args[0], err)
	}
	return string(out), nil
}

func (s *Server) a11y(args ...string) ([]map[string]any, bool) {
	cmd := exec.Command("python3", append([]string{a11yScript}, args...)...)
	cmd.Env = append(os.Environ(),
		"DISPLAY="+s.display,
		"XDG_RUNTIME_DIR=/run/user/1000",
		"DBUS_SESSION_BUS_ADDRESS="+sessionBus)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return textContent("a11y %s failed: %v", args[0], err), true
	}
	var parsed any
	if e := json.Unmarshal(out, &parsed); e != nil {
		return textContent("%s", strings.TrimSpace(string(out))), err != nil
	}
	if m, ok := parsed.(map[string]any); ok {
		if msg, bad := m["error"].(string); bad {
			return textContent("%s", msg), true
		}
	}
	return jsonContent(parsed), false
}

func (s *Server) dispatchUI(ctx context.Context, name string, args map[string]any) ([]map[string]any, bool, bool) {
	opt := func(flag, key string) []string {
		if v := argStr(args, key); v != "" {
			return []string{flag, v}
		}
		return nil
	}
	optInt := func(flag, key string) []string {
		if n := argInt(args, key); n > 0 {
			return []string{flag, strconv.Itoa(n)}
		}
		return nil
	}

	switch name {
	case "ui_at_point":
		content, isErr := s.toolUIAtPoint(args)
		return content, isErr, true
	case "ui_tree":
		a := []string{"tree"}
		a = append(a, opt("--app", "app")...)
		a = append(a, optInt("--depth", "depth")...)
		a = append(a, optInt("--limit", "limit")...)
		if b, _ := args["interactive"].(bool); b {
			a = append(a, "--interactive")
		}
		c, e := s.a11y(a...)
		return c, e, true

	case "ui_find", "ui_wait_for":
		a := []string{"find"}
		if name == "ui_wait_for" {
			a = []string{"waitfor"}
		}
		a = append(a, opt("--role", "role")...)
		a = append(a, opt("--name", "name")...)
		a = append(a, opt("--text", "text")...)
		a = append(a, opt("--app", "app")...)
		a = append(a, optInt("--limit", "limit")...)
		if name == "ui_wait_for" {
			ms := argInt(args, "timeout_ms")
			if ms <= 0 {
				ms = 15000
			}
			a = append(a, "--timeout-ms", strconv.Itoa(ms))
		}
		c, e := s.a11y(a...)
		return c, e, true

	case "ui_click":
		a := []string{"click", "--ref", argStr(args, "ref")}
		a = append(a, opt("--action", "action")...)
		c, e := s.a11y(a...)
		return c, e, true

	case "ui_set_text":
		c, e := s.a11y("settext", "--ref", argStr(args, "ref"), "--text", argStr(args, "text"))
		return c, e, true

	case "ui_get_text":
		c, e := s.a11y("gettext", "--ref", argStr(args, "ref"))
		return c, e, true

	case "ui_focus":
		c, e := s.a11y("focus", "--ref", argStr(args, "ref"))
		return c, e, true
	}
	return nil, false, false
}

// --- CDP: driving the browser through the real DOM ------------------------

func (s *Server) buildBrowserTools() []toolDef {
	return []toolDef{
		{
			Name:            "browser_open",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Launch Chromium with the DevTools Protocol enabled (port 9222) so the other browser_* tools can drive the real DOM. Optionally opens a URL. If it is already running this just reports it.",
			InputSchema:     schema(map[string]any{"url": pStr("optional URL to open")}),
		},
		{
			Name:        "browser_tabs",
			Risk:        riskRead,
			Description: "List the open browser tabs with their title and URL.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:            "browser_goto",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			// Beyond this verb's own default timeout_ms of 30000, so its own
			// bound wins first and this is the backstop for a load that hangs
			// past it. A deadline equal to the parameter would race it.
			TimeoutMS:   45000,
			Description: "Navigate the active tab to a URL and wait for the load to finish.",
			InputSchema: schema(map[string]any{
				"url": pStr("URL"), "timeout_ms": pIntDef("how long to wait for the load, default 30000", 30000),
			}, "url"),
		},
		{
			Name:            "browser_eval",
			Visibility:      visVisible,
			Risk:            riskDanger,
			RequiresControl: true,
			Description:     "Run JavaScript in the page and return the result. The most powerful browser tool: you can read anything from the DOM without screenshots.",
			InputSchema:     schema(map[string]any{"expression": pStr("JavaScript expression")}, "expression"),
		},
		{
			Name:            "browser_click",
			Visibility:      visInjects,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Click an element in the page by CSS selector — exact, no coordinates involved.",
			InputSchema:     schema(map[string]any{"selector": pStr("CSS selector, e.g. '#login-btn' or 'button.primary'")}, "selector"),
		},
		{
			Name:            "browser_type",
			Visibility:      visInjects,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Type text into an input/textarea selected by CSS selector (fires the events a real page expects).",
			InputSchema:     schema(map[string]any{"selector": pStr("CSS selector"), "text": pStr("text")}, "selector", "text"),
		},
		{
			Name:        "browser_text",
			Risk:        riskRead,
			Description: "Get the visible text of the page, or of the element matching a CSS selector. This is what replaces OCR for web content.",
			InputSchema: schema(map[string]any{
				"selector":  pStr("optional CSS selector (default: whole page)"),
				"max_chars": pIntDef("truncate (default 4000)", 4000),
			}),
		},
		{
			Name: "browser_element",
			Risk: riskRead,
			Description: "Everything about one element, by CSS selector: whether it exists, " +
				"whether it is really VISIBLE, its text and accessible label, its box, and " +
				"whether it is disabled. For a <video> or <audio> it also reports " +
				"currentTime, duration, paused, muted and ended. Use this to CHECK — before " +
				"clicking something, and afterwards to find out whether the click did " +
				"anything. Reading a media element's clock twice, a second apart, is how you " +
				"tell 'still playing, my click did nothing' from 'a new item started'.",
			InputSchema: schema(map[string]any{
				"selector": pStr("CSS selector, e.g. '.ytp-skip-ad-button' or 'video'"),
			}, "selector"),
		},
		{
			Name: "browser_wait_until",
			Risk: riskRead,
			Description: "Wait until a JavaScript condition is TRUE in the page, and return " +
				"how long it took. This is how you wait for something that is not an " +
				"element appearing: a video reaching its end, a counter passing a number, " +
				"an upload finishing.\n\n" +
				"Use it instead of looping. Checking a condition yourself costs a step each " +
				"time you look, so waiting three minutes for a video by taking a reading " +
				"every fifteen seconds spends twelve steps to learn one fact. This spends " +
				"ONE, however long the wait, because the waiting happens in the page.\n\n" +
				"Examples: \"document.querySelector('video')?.ended === true\" waits for a " +
				"video to finish; \"document.querySelector('video').currentTime > 60\" waits " +
				"for it to pass a minute; \"!document.querySelector('.ad-showing')\" waits " +
				"for an advertisement to end.\n\n" +
				"Returns {ok, waited_ms, value} — ok false means the timeout was reached, " +
				"and value is what the condition evaluated to at the end, so a wait that " +
				"gave up still tells you where things stood.\n\n" +
				"IT POLLS, so the condition has to still be true when it looks. An EVENT " +
				"is not: a video's `ended` is true from the moment it finishes until the " +
				"page moves on, which on YouTube is milliseconds, and a poll lands either " +
				"side of it. That exact wait sat for five minutes while the video it was " +
				"watching finished, autoplayed the next one, and started somebody else's " +
				"video — and reported false the whole time.\n\n" +
				"Use `setup` for those: one expression, run ONCE before the waiting " +
				"starts, to latch the event into a flag the condition can then read. " +
				"setup: \"window.__done=false; document.querySelector('video')" +
				".addEventListener('ended',()=>window.__done=true,{once:true})\" with " +
				"expression: \"window.__done\" catches an end that lasted an instant. " +
				"Anything an event fires for — ended, load, a custom event, a " +
				"MutationObserver — becomes waitable this way.\n\n" +
				"Write such an expression as the BARE flag, not \"flag === true\". " +
				"Both wait correctly, but `value` reports what the expression evaluated " +
				"to: the comparison says \"false\" both when the flag is false and when " +
				"it never existed, which is what a page reload leaves behind. The bare " +
				"flag says \"undefined\" for the second, so a wait that outlived a " +
				"navigation can be told from one that is still waiting.",
			InputSchema: schema(map[string]any{
				"expression": pStr("a JavaScript expression that becomes true"),
				"setup": pStr("JavaScript to run ONCE before waiting, to latch an event " +
					"into a flag the expression can read. Use this for anything that is " +
					"an instant rather than a state"),
				"timeout_ms": pIntDef("give up after this long, default 60000", 60000),
				"poll_ms":    pIntDef("how often to test it, default 250", 250),
			}, "expression"),
		},
		{
			Name: "browser_wait_for",
			Risk: riskRead,
			Description: "Wait until an element matching a CSS selector is VISIBLE — rendered, " +
				"sized, not hidden and not transparent. That is almost always the real " +
				"question: pages keep controls mounted and unusable, so waiting for mere " +
				"presence can return in milliseconds on a button nobody can press yet. " +
				"Use state=present when you only want to know the node exists, and " +
				"state=gone to wait for something to disappear — an overlay, a spinner, " +
				"an advertisement.",
			InputSchema: schema(map[string]any{
				"selector":   pStr("CSS selector"),
				"state":      pStr("visible (default) | present | gone"),
				"timeout_ms": pIntDef("timeout, default 15000", 15000),
			}, "selector"),
		},
	}
}

func (s *Server) dispatchBrowser(ctx context.Context, name string, args map[string]any) ([]map[string]any, bool, bool) {
	switch name {
	case "browser_open":
		c, e := s.toolBrowserOpen(ctx, argStr(args, "url"))
		return c, e, true
	case "browser_tabs":
		targets, err := cdpTargets()
		if err != nil {
			return textContent("browser_tabs failed: %v", err), true, true
		}
		var tabs []map[string]any
		for _, t := range targets {
			tabs = append(tabs, map[string]any{"title": t.Title, "url": t.URL, "id": t.ID})
		}
		return jsonContent(tabs), false, true
	case "browser_goto":
		// Waits for the load rather than reporting the intention to start one.
		// The old form assigned location.href and returned "navigating", which
		// was true when said and stale by the time anything read it, so every
		// tool called next raced the page.
		ms := argInt(args, "timeout_ms")
		if ms <= 0 {
			ms = 30000
		}
		res, err := cdpNavigate(argStr(args, "url"), time.Duration(ms)*time.Millisecond)
		if err != nil {
			return textContent("browser_goto failed: %v", err), true, true
		}
		return textContent("%s", res), false, true
	case "browser_eval":
		c, e := s.cdpEvalReport(argStr(args, "expression"))
		return c, e, true
	case "browser_click":
		res, err := cdpClick(argStr(args, "selector"))
		if err != nil {
			return textContent("browser_click failed: %v", err), true, true
		}
		return textContent("%s", res), false, true
	case "browser_type":
		res, err := cdpType(argStr(args, "selector"), argStr(args, "text"))
		if err != nil {
			return textContent("browser_type failed: %v", err), true, true
		}
		return textContent("%s", res), false, true
	case "browser_text":
		sel := argStr(args, "selector")
		max := argInt(args, "max_chars")
		if max <= 0 {
			max = 4000
		}
		target := "document.body"
		if sel != "" {
			target = fmt.Sprintf("document.querySelector(%s)", jsStr(sel))
		}
		c, e := s.cdpEvalReport(fmt.Sprintf(
			"(()=>{const el=%s; if(!el) return 'ERROR: no element';"+
				"return (el.innerText||el.textContent||'').trim().slice(0,%d)})()", target, max))
		return c, e, true
	case "browser_element":
		c, e := s.cdpEvalReport(elementReportJS(argStr(args, "selector")))
		return c, e, true
	case "browser_wait_for":
		c, e := s.toolBrowserWaitFor(ctx, argStr(args, "selector"), argStr(args, "state"), argInt(args, "timeout_ms"))
		return c, e, true
	case "browser_wait_until":
		c, e := s.toolBrowserWaitUntil(ctx, argStr(args, "expression"), argStr(args, "setup"),
			argInt(args, "timeout_ms"), argInt(args, "poll_ms"))
		return c, e, true
	}
	return nil, false, false
}

// toolBrowserWaitUntil waits for a condition to become true, inside the page.
//
// # Why this exists, and what it is worth
//
// An agent asked to play a video and stop when it ends has no way to say that.
// It can read the clock — browser_element reports currentTime and ended — but
// reading is a STEP, so it reads, waits, reads, waits, and a three-minute video
// costs a dozen steps to learn one fact. That is not a hypothetical: it is what
// happened, and the run was cut off at its step limit with the video still
// playing and the recording still running.
//
// The waiting belongs where the answer is. One evaluate goes out, the page
// tests the condition on its own timer, and the promise resolves the moment it
// holds. One step, however long the wait.
//
// # Why polling in the page rather than a MutationObserver
//
// browser_wait_for uses an observer, correctly: a node appearing IS a mutation.
// A condition is not. "currentTime > 60" changes with no DOM event at all, and
// an observer would sleep through it. So this polls — but it polls in the page,
// at a cost of one comparison per tick, instead of across a WebSocket at the
// cost of a step.
//
// The expression is evaluated as written, in the page, with the page's
// privileges. That is browser_eval's bargain and this is the same one; it is
// marked riskRead because waiting reads, but what it reads is arbitrary.
func (s *Server) toolBrowserWaitUntil(ctx context.Context, expr, setup string, timeoutMs, pollMs int) ([]map[string]any, bool) {
	expr = strings.TrimSpace(expr)
	setup = strings.TrimSpace(setup)
	if expr == "" {
		return textContent("browser_wait_until needs an expression to wait for"), true
	}
	if timeoutMs <= 0 {
		timeoutMs = 60000
	}
	// A ceiling, because this holds a socket and a step open. Ten minutes is
	// longer than any single thing on a page and short enough that a run which
	// asked for a wait that will never end still gets its answer today.
	if timeoutMs > 600000 {
		timeoutMs = 600000
	}
	if pollMs <= 0 {
		pollMs = 250
	}
	if pollMs < 50 {
		pollMs = 50
	}

	// The condition is wrapped in its own try, so a page that throws — a null
	// dereference on an element that has not appeared yet, which is the common
	// case — is a FALSE rather than an error that ends the wait. Waiting for
	// something that does not exist yet is the whole point.
	// setup runs ONCE, before the first test, and its own throw is swallowed the
	// same way the condition's is: a listener attached to an element that has
	// not appeared yet must leave the wait running rather than end it. What it
	// exists for is the gap between an EVENT and a STATE — a poll can only see
	// states, and `ended` stops being true the instant the page moves on.
	//
	// Deliberately not a separate step. Latching an event and then waiting on
	// the flag in two calls works, and costs two steps for one wait — which is
	// the arithmetic this whole tool exists to fix.
	js := waitUntilScript(expr, setup, pollMs, timeoutMs)

	// The read deadline is the page's timeout plus room for the round trip. A
	// deadline shorter than the wait would abandon the socket just before the
	// answer the caller asked for — which is the bug cdpEvalTimeout exists to
	// let callers avoid.
	out, err := cdpEvalTimeout(js, time.Duration(timeoutMs)*time.Millisecond+10*time.Second)
	if err != nil {
		return textContent("browser_wait_until failed: %v", err), true
	}
	return textContent("%s", out), false
}

// waitUntilScript is the page-side program the wait runs.
//
// Built apart from sending it so a test can read it. What it has to get right
// is not observable from the tool's return value: whether setup ran BEFORE the
// first test (after it, an event in the gap is lost), and whether a setup that
// throws ends the wait (it must not — a listener attached to an element that
// has not appeared yet is the ordinary case).
func waitUntilScript(expr, setup string, pollMs, timeoutMs int) string {
	setupJS := ""
	if setup != "" {
		setupJS = fmt.Sprintf("try { %s } catch (e) {}\n  ", setup)
	}

	return fmt.Sprintf(`(() => new Promise(resolve => {
  %sconst started = Date.now();
  const test = () => { try { return !!(%s); } catch (e) { return false; } };
  const done = ok => resolve(JSON.stringify({
    ok, waited_ms: Date.now() - started,
    value: (() => { try { return String(%s); } catch (e) { return 'threw: ' + e.message; } })(),
  }));
  if (test()) return done(true);
  const t = setInterval(() => {
    if (test()) { clearInterval(t); clearTimeout(bail); done(true); }
  }, %d);
  const bail = setTimeout(() => { clearInterval(t); done(false); }, %d);
}))()`, setupJS, expr, expr, pollMs, timeoutMs)
}

// toolBrowserWaitFor waits for a selector to match, from inside the page.
//
// It used to ask the browser fifty times whether the node had appeared, and
// each of those questions opened a fresh WebSocket, ran a query and closed it
// again — a full handshake three times a second to be told "not yet". The
// answer also arrived up to 300ms late, which on a page that then gets clicked
// is long enough to matter.
//
// A MutationObserver moves the waiting to where the change happens. One
// evaluate goes out, its promise resolves the instant a matching node is
// inserted, and nothing is asked again. This is what Playwright does, and for
// the same reason: the page already knows.
func (s *Server) toolBrowserWaitFor(ctx context.Context, sel, state string, timeoutMs int) ([]map[string]any, bool) {
	if timeoutMs <= 0 {
		timeoutMs = 15000
	}
	state = strings.ToLower(strings.TrimSpace(state))
	switch state {
	case "", "visible", "present", "gone":
	default:
		return textContent("unknown state %q: use visible, present or gone", state), true
	}
	// The page resolves rather than rejects on timeout, so a miss comes back as
	// a value to report instead of an exception to translate.
	//
	// `visible` is the default and the old behaviour — resolving on presence
	// alone — is now `present`, because presence is almost never the question.
	// A run waiting for a YouTube skip button got "appeared" in SIX
	// MILLISECONDS: the element is in the DOM for the whole advertisement and
	// only becomes usable when the countdown ends. The wait returned instantly,
	// truthfully, and told the caller nothing it did not already know, which is
	// the same defect as a tool that returns ok and does nothing.
	//
	// getClientRects().length is the test for "is rendered", not offsetParent —
	// that is null for position:fixed, which is what a skip button, a cookie
	// banner and most modals are. Zero-size and visibility:hidden and opacity:0
	// are all things a page uses to keep a control mounted and unusable, so all
	// three count as not visible.
	//
	// A MutationObserver alone is not enough here, and this is the part that is
	// easy to get wrong. It fires on DOM and attribute changes, but an element
	// can become visible with neither: a CSS transition finishing, a stylesheet
	// arriving, an ancestor being scrolled into view, a @keyframes animation. So
	// there is also a 100ms poll. The observer keeps the common case instant;
	// the poll is what stops the uncommon one hanging until the timeout.
	want, ok := waitPredicate(state)
	if !ok {
		return textContent("unknown state %q: use visible, present or gone", state), true
	}

	expr := fmt.Sprintf(`new Promise(resolve => {
  const sel = %s;
  const ok = %s;
  const hit = () => ok(document.querySelector(sel));
  const done = (why) => { clearTimeout(timer); clearInterval(poll); if (obs) obs.disconnect(); resolve(why); };
  let obs, poll, timer;
  timer = setTimeout(() => done("timeout"), %d);
  if (hit()) { done("found"); return; }
  obs = new MutationObserver(() => { if (hit()) done("found"); });
  obs.observe(document.documentElement || document,
              {childList: true, subtree: true, attributes: true});
  poll = setInterval(() => { if (hit()) done("found"); }, 100);
})`, jsStr(sel), want, timeoutMs)

	// Give the socket a margin over the page's own timer: the page is the one
	// keeping time, and a read deadline that expired first would turn its
	// orderly "timeout" into a connection error.
	res, err := cdpEvalTimeout(expr, time.Duration(timeoutMs)*time.Millisecond+10*time.Second)
	if err != nil {
		// A navigation during the wait destroys the execution context and takes
		// the promise with it. Saying which happened beats a bare CDP error.
		if strings.Contains(err.Error(), "Execution context") || strings.Contains(err.Error(), "destroyed") {
			return textContent("the page navigated away while waiting for %s", sel), true
		}
		return textContent("browser_wait_for failed: %v", err), true
	}
	if ctx.Err() != nil {
		return textContent("cancelled while waiting for %s", sel), true
	}
	if strings.Contains(res, "found") {
		if state == "gone" {
			return textContent("%s is gone", sel), false
		}
		if state == "present" {
			return textContent("%s is in the page — NOT checked for being visible or usable", sel), false
		}
		return textContent("%s is visible", sel), false
	}
	// A timeout says which of the two things was missing, because they need
	// opposite responses: a selector that never matched is probably the wrong
	// selector, while one that matched and stayed hidden is the right selector
	// on a control the page is not offering yet. The old message could not tell
	// them apart and neither could anybody reading it.
	if state != "gone" {
		if why, err := cdpEval(fmt.Sprintf(
			`(()=>{const el=document.querySelector(%s); if(!el) return "no element ever matched";
        const r=el.getBoundingClientRect(); const st=getComputedStyle(el);
        return "the element is there but not visible: "+JSON.stringify(
          {w:Math.round(r.width),h:Math.round(r.height),visibility:st.visibility,opacity:st.opacity,display:st.display});})()`,
			jsStr(sel))); err == nil && why != "" {
			return textContent("timed out after %d ms waiting for %s to be %s — %s",
				timeoutMs, sel, orDefault(state, "visible"), why), true
		}
	}
	return textContent("timed out after %d ms waiting for %s to be %s",
		timeoutMs, sel, orDefault(state, "visible")), true
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (s *Server) toolBrowserOpen(ctx context.Context, url string) ([]map[string]any, bool) {
	if targets, err := cdpTargets(); err == nil && len(targets) > 0 {
		if url != "" {
			// Same correction as browser_goto: return when the page is there,
			// not when the navigation has been asked for.
			res, err := cdpNavigate(url, 30*time.Second)
			if err != nil {
				return textContent("browser_open failed: %v", err), true
			}
			return textContent("%s", res), false
		}
		return textContent("the browser is already open with CDP (%d tabs)", len(targets)), false
	}
	cmdline := "chromium --remote-debugging-port=9222 --remote-allow-origins=* " +
		"--no-first-run --no-default-browser-check --force-renderer-accessibility"
	if url != "" {
		cmdline += " " + url
	}
	cmd := exec.Command("setsid", "sh", "-c", cmdline)
	cmd.Env = append(os.Environ(), "DISPLAY="+s.display,
		"DBUS_SESSION_BUS_ADDRESS="+sessionBus)
	if err := cmd.Start(); err != nil {
		return textContent("browser_open failed: %v", err), true
	}
	go cmd.Wait()

	// Polling is right here and nowhere else in this file. There is no event to
	// wait on before a process has begun listening: the socket that would carry
	// it is the thing being waited for.
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if t, err := cdpTargets(); err == nil && len(t) > 0 {
			if url == "" {
				return textContent("browser open with CDP (%d tabs)", len(t)), false
			}
			// Chromium was handed the URL on its command line and is already
			// fetching it, so there is no navigation to start — only a load to
			// wait for, and it may have finished while the port was coming up.
			// Asking the page settles both cases without a race: a document
			// that is already complete resolves at once, and one still loading
			// resolves on its own load event.
			res, err := cdpEvalTimeout(`new Promise(resolve => {
  if (document.readyState === "complete") { resolve("complete"); return; }
  const timer = setTimeout(() => resolve(document.readyState), 25000);
  window.addEventListener("load", () => { clearTimeout(timer); resolve("complete"); }, {once: true});
})`, 35*time.Second)
			if err != nil {
				return textContent("browser open with CDP (%d tabs), but the page could not be read: %v", len(t), err), false
			}
			if res == "complete" {
				return textContent("browser open with CDP (%d tabs), %s loaded", len(t), url), false
			}
			return textContent("browser open with CDP (%d tabs), %s is still %s", len(t), url, res), false
		}
		if !sleepCtx(ctx, 700*time.Millisecond) {
			break
		}
	}
	return textContent("the browser started but CDP did not answer in time"), true
}

func (s *Server) cdpEvalReport(expr string) ([]map[string]any, bool) {
	res, err := cdpEval(expr)
	if err != nil {
		return textContent("browser eval failed: %v", err), true
	}
	return textContent("%s", res), false
}

// jsStr serialises a string for embedding in JavaScript source.
func jsStr(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// --- what is at a point ---------------------------------------------------------

// toolUIAtPoint answers "what is under these coordinates" without walking the
// tree.
//
// It exists because the cheapest question an agent asks after looking at a
// screenshot — *what is that thing* — was the most expensive one to answer.
// ui_find and ui_tree are O(tree): every application, every window, every
// widget, three quarters of a second and up to twenty thousand tokens. AT-SPI's
// Component interface answers it in O(depth), which is what a screen reader
// following a pointer has always used.
//
// Two coordinate systems, and combining them is the whole design. X owns where
// a window is and EWMH reports it correctly. AT-SPI claims to own it too and on
// this desktop is simply wrong: a dialog the window manager places at (805, 429)
// reports itself at (0, 0), and still does after being moved to (100, 200) — it
// never knew rather than having gone stale. So the window is resolved here,
// where the answer is right, and only the in-window descent is asked of the
// bridge, where AT-SPI is reliable.
func (s *Server) toolUIAtPoint(args map[string]any) ([]map[string]any, bool) {
	x, y := argInt(args, "x"), argInt(args, "y")
	if _, ok := args["x"]; !ok {
		return textContent("`x` and `y` are missing: screen coordinates, the kind screenshot and get_mouse_position report"), true
	}

	// Topmost window containing the point. The list is in stacking order with
	// the most recently mapped last, so it is scanned backwards: the first
	// match walking forwards would be whatever happens to be underneath.
	windows := s.listWindows()
	var win *windowInfo
	for i := len(windows) - 1; i >= 0; i-- {
		w := windows[i]
		if x >= w.X && x < w.X+w.W && y >= w.Y && y < w.Y+w.H {
			win = &windows[i]
			break
		}
	}
	if win == nil {
		return jsonContent(map[string]any{
			"found": false, "x": x, "y": y,
			"hint": "no window covers that point — it is the desktop background, " +
				"or the coordinates are off-screen. list_windows shows what is where.",
		}), false
	}

	// The application name AT-SPI knows is not the window class X knows, and
	// neither is a superset of the other. The class is the better bet and the
	// bridge matches on a substring, so `Zenity` finds `zenity`.
	app := win.Class
	if i := strings.LastIndex(app, "."); i >= 0 {
		app = app[i+1:] // wmctrl reports WM_CLASS as "instance.Class"
	}

	out, err := s.a11yRaw("atpoint",
		"--app", app,
		"--x", strconv.Itoa(x-win.X),
		"--y", strconv.Itoa(y-win.Y))
	if err != nil {
		return textContent("accessibility bridge: %v", err), true
	}
	var res map[string]any
	if json.Unmarshal([]byte(out), &res) != nil {
		return textContent("accessibility bridge returned non-JSON: %s", strings.TrimSpace(out)), true
	}

	// The window goes back with the answer whether or not an element was found.
	// "Nothing accessible there" plus "it is Chromium" is a useful answer;
	// "nothing accessible there" on its own is a dead end, and the next step
	// (browser_* instead of ui_*) depends entirely on which it was.
	res["window"] = win
	if found, _ := res["found"].(bool); !found {
		res["hint"] = fmt.Sprintf(
			"%s is at that point and exposes nothing accessible there. "+
				"If it is a browser, the page has its own tree — use browser_text "+
				"or browser_click. Otherwise the toolkit has no accessibility support "+
				"and mouse_click with these coordinates is the way in.", win.Title)
	}
	return jsonContent(res), false
}

// waitPredicate is the JavaScript that decides whether browser_wait_for is
// satisfied, and it is a separate function so the SEMANTICS can be tested
// without a browser.
//
// The default is `visible`, and that is the whole point of this having been
// rewritten. Presence used to be the answer, and presence is almost never the
// question: a run waiting for a YouTube skip button was told it had "appeared"
// in six milliseconds, because the element is mounted for the entire
// advertisement and only becomes usable when the countdown ends. The wait
// returned instantly, truthfully, and taught the caller nothing — the same
// defect as a tool that returns ok and does nothing.
//
// getClientRects().length is the test for "is rendered", NOT offsetParent,
// which is null for position:fixed — and a skip button, a cookie banner and
// most modals are exactly that. Zero size, visibility:hidden and opacity:0 are
// all ways a page keeps a control mounted and unusable, so all three fail.
func waitPredicate(state string) (string, bool) {
	const visible = `(el) => {
    if (!el) return false;
    if (el.getClientRects().length === 0) return false;
    const st = getComputedStyle(el);
    if (st.visibility === "hidden" || st.visibility === "collapse") return false;
    if (parseFloat(st.opacity) === 0) return false;
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  }`

	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "visible":
		return visible, true
	case "present":
		return `(el) => !!el`, true
	case "gone":
		// Both halves, because "the overlay went away" is satisfied by the node
		// being removed AND by it being hidden where it stands, and a page picks
		// whichever it likes. Waiting for only one of them hangs on the other.
		return `(el) => !el || !(` + visible + `)(el)`, true
	}
	return "", false
}

// elementReportJS asks the page about one element and nothing else.
//
// This exists because of a policy hole rather than a missing feature. Reading a
// page meant browser_eval, which runs arbitrary JavaScript — it can navigate,
// fetch, submit a form, rewrite the document — and is riskDanger for good
// reason. But that made MCP_POLICY=safe grant exactly the wrong pair: an agent
// could CLICK (browser_click is riskWrite) and could not CHECK. Acting without
// being able to verify is worse than not acting, and it is the reverse of what
// a safety level is for.
//
// So the answer is not to weaken browser_eval. It is a tool narrow enough to be
// riskRead honestly: a fixed expression with one selector substituted, which
// cannot navigate, cannot write and cannot be talked into it.
//
// The media fields are here rather than in a tool of their own because they are
// the same question — what is the state of this element — and because they are
// what tells a caller its click did nothing. An advertisement whose currentTime
// advanced by the wall-clock time that passed is the SAME advertisement still
// playing, not a new one; a run that could not see that clicked a skip button
// four times and reported success each time.
func elementReportJS(sel string) string {
	return fmt.Sprintf(`(() => {
  const el = document.querySelector(%s);
  if (!el) return JSON.stringify({present: false, visible: false});
  const r = el.getBoundingClientRect();
  const st = getComputedStyle(el);
  const visible = el.getClientRects().length > 0 &&
    st.visibility !== "hidden" && st.visibility !== "collapse" &&
    parseFloat(st.opacity) !== 0 && r.width > 0 && r.height > 0;
  const out = {
    present: true, visible: visible,
    tag: el.tagName.toLowerCase(),
    text: (el.innerText || el.textContent || "").trim().slice(0, 400),
    label: (el.getAttribute("aria-label") || el.getAttribute("title") || "").trim(),
    // Where a link GOES, which is a different question from what it says and
    // the only one some tasks have. Reading an id out of a search result to
    // open it directly needs this; without it the only way to follow a link is
    // to click it and find out where you landed, which on YouTube means
    // arriving inside a Mix that then autoplays into somebody else's video.
    //
    // Resolved rather than raw: el.href is absolute even when the attribute is
    // relative, so a caller never has to know the page's base to use it.
    href: (el.tagName === "A" || el.tagName === "AREA") ? el.href : undefined,
    disabled: !!el.disabled || el.getAttribute("aria-disabled") === "true",
    // A toggle's STATE, which is the question you have before you click one:
    // is it already on? Absent for anything that is not a toggle, so its
    // presence is itself the answer to "is this a switch". aria-pressed is the
    // same idea spelled for buttons, and native inputs carry it as a property.
    //
    // Without this the only readable clue was the label, which is in the
    // page's language — so "is autoplay on" was answerable in English and not
    // in Spanish, and a skill written against one broke on the other.
    checked: (() => {
      const a = el.getAttribute("aria-checked") || el.getAttribute("aria-pressed");
      if (a === "true" || a === "false") return a === "true";
      if (typeof el.checked === "boolean") return el.checked;
      return undefined;
    })(),
    rect: {x: Math.round(r.left), y: Math.round(r.top),
           w: Math.round(r.width), h: Math.round(r.height)},
    style: {display: st.display, visibility: st.visibility, opacity: st.opacity},
  };
  // What is on TOP of its middle. A control that is visible and covered is one
  // a click will not reach, and the two are indistinguishable from every field
  // above.
  if (visible) {
    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    out.covered = !!(hit && hit !== el && !el.contains(hit));
    if (out.covered) out.covered_by = hit.tagName.toLowerCase() +
      (hit.className && typeof hit.className === "string" ? "." + hit.className.split(" ")[0] : "");
  }
  if (typeof el.currentTime === "number" && typeof el.paused === "boolean") {
    out.media = {
      currentTime: el.currentTime, duration: el.duration,
      paused: el.paused, muted: el.muted, ended: el.ended,
      readyState: el.readyState,
    };
  }
  return JSON.stringify(out);
})()`, jsStr(sel))
}
