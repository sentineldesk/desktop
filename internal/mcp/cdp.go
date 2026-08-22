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

// A minimal Chrome DevTools Protocol client.
//
// With CDP the browser stops being a picture: the real DOM can be queried and
// operated — read text, click by selector, run JavaScript. It is to web pages
// what AT-SPI is to native applications.
//
// Tab discovery goes over HTTP (/json); commands go over a WebSocket to the
// chosen tab.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const cdpEndpoint = "http://127.0.0.1:9222"

type cdpTarget struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Debugger string `json:"webSocketDebuggerUrl"`
}

// cdpTargets lists the available tabs.
func cdpTargets() ([]cdpTarget, error) {
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(cdpEndpoint + "/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var all []cdpTarget
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return nil, err
	}
	var pages []cdpTarget
	for _, t := range all {
		if t.Type == "page" && t.Debugger != "" && !strings.HasPrefix(t.URL, "devtools://") {
			pages = append(pages, t)
		}
	}
	return pages, nil
}

// cdpConn is one WebSocket to one tab.
//
// CDP multiplexes two things down this socket: replies, which carry the id of
// the request they answer, and events, which carry a method and no id. A client
// that only ever wants replies can discard everything without an id, which is
// what this one did — and is why every browser wait in the catalogue was a
// poll. Separating the two is what makes it possible to ask the browser to say
// when something happens rather than asking it fifty times whether it has.
type cdpConn struct {
	ws   *websocket.Conn
	next int
}

// cdpOpen connects to the first page target.
func cdpOpen() (*cdpConn, error) {
	targets, err := cdpTargets()
	if err != nil {
		return nil, fmt.Errorf("CDP unavailable (did you open the browser with browser_open?): %w", err)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no tabs are open")
	}
	dialer := websocket.Dialer{HandshakeTimeout: 8 * time.Second}
	ws, _, err := dialer.Dial(targets[0].Debugger, nil)
	if err != nil {
		return nil, fmt.Errorf("CDP connection: %w", err)
	}
	return &cdpConn{ws: ws}, nil
}

func (c *cdpConn) Close() { c.ws.Close() }

// send writes a command and returns the id to await it by.
func (c *cdpConn) send(method string, params map[string]any) (int, error) {
	c.next++
	req := map[string]any{"id": c.next, "method": method}
	if params != nil {
		req["params"] = params
	}
	return c.next, c.ws.WriteJSON(req)
}

// reply reads until the answer to id arrives, discarding events on the way.
func (c *cdpConn) reply(id int, deadline time.Time) (map[string]any, error) {
	c.ws.SetReadDeadline(deadline)
	for {
		var raw map[string]any
		if err := c.ws.ReadJSON(&raw); err != nil {
			return nil, err
		}
		got, ok := raw["id"].(float64)
		if !ok || int(got) != id {
			continue // an event, or an answer to something else
		}
		if e, ok := raw["error"].(map[string]any); ok {
			return nil, fmt.Errorf("CDP: %v", e["message"])
		}
		result, _ := raw["result"].(map[string]any)
		return result, nil
	}
}

// event reads until one of the named events arrives, discarding replies.
//
// The names are plural because a page can finish in more than one way and a
// waiter that knew only the happy one would sit until the deadline on every
// other: a navigation that is cancelled, or answered by a download, never fires
// a load event at all.
func (c *cdpConn) event(deadline time.Time, methods ...string) (string, map[string]any, error) {
	want := map[string]bool{}
	for _, m := range methods {
		want[m] = true
	}
	c.ws.SetReadDeadline(deadline)
	for {
		var raw map[string]any
		if err := c.ws.ReadJSON(&raw); err != nil {
			return "", nil, err
		}
		method, ok := raw["method"].(string)
		if !ok || !want[method] {
			continue
		}
		params, _ := raw["params"].(map[string]any)
		return method, params, nil
	}
}

// evalResult turns Runtime.evaluate's reply into the string tools report.
func evalResult(result map[string]any) (string, error) {
	if ex, ok := result["exceptionDetails"].(map[string]any); ok {
		return "", fmt.Errorf("JS: %v", ex["text"])
	}
	inner, _ := result["result"].(map[string]any)
	if v, ok := inner["value"]; ok {
		switch typed := v.(type) {
		case string:
			return typed, nil
		default:
			b, _ := json.Marshal(typed)
			return string(b), nil
		}
	}
	if d, ok := inner["description"].(string); ok {
		return d, nil
	}
	return "(no value)", nil
}

// cdpEval runs JavaScript in the active tab and returns the result.
func cdpEval(expression string) (string, error) {
	return cdpEvalTimeout(expression, 30*time.Second)
}

// cdpEvalTimeout is cdpEval with the read deadline under the caller's control.
//
// It matters for expressions that deliberately take their time. A page-side
// wait resolves its promise when a node appears, which can be a minute away,
// and a fixed thirty-second deadline would abandon the socket long before the
// answer the caller asked for.
func cdpEvalTimeout(expression string, timeout time.Duration) (string, error) {
	c, err := cdpOpen()
	if err != nil {
		return "", err
	}
	defer c.Close()

	id, err := c.send("Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
		// Unlocks APIs that demand a "user gesture" — autoplay and friends.
		"userGesture": true,
	})
	if err != nil {
		return "", err
	}
	result, err := c.reply(id, time.Now().Add(timeout))
	if err != nil {
		return "", err
	}
	return evalResult(result)
}

// cdpClick clicks an element the way a pointer would, not the way script does.
//
// el.click() dispatches one synthetic click on the node and nothing else: no
// pointer movement, no mousedown, no mouseup, and isTrusted false. Pages that
// check for a trusted event — payment flows, anti-automation — refuse it, and
// interfaces built on the surrounding events rather than on click alone simply
// do not respond. It also cannot fail: a node covered by a modal, a cookie
// banner or a disabled overlay is clicked regardless, because the DOM call
// never asks what is on top. The caller is told it clicked, and something else
// entirely received the user's attention.
//
// Dispatching through Input at the element's centre restores all of that, and
// makes the covered case visible: elementFromPoint says who would actually be
// hit, and disagreeing with the target is worth reporting rather than clicking
// through.
func cdpClick(sel string) (string, error) {
	c, err := cdpOpen()
	if err != nil {
		return "", err
	}
	defer c.Close()
	deadline := time.Now().Add(20 * time.Second)

	id, err := c.send("Runtime.evaluate", map[string]any{
		"expression": fmt.Sprintf(`(()=>{
  const el = document.querySelector(%s);
  if (!el) return JSON.stringify({error: "no such element " + %s});
  el.scrollIntoView({block: "center", inline: "center"});
  const r = el.getBoundingClientRect();
  if (r.width === 0 && r.height === 0) return JSON.stringify({error: "the element has no box on screen"});
  const x = r.left + r.width / 2, y = r.top + r.height / 2;
  const hit = document.elementFromPoint(x, y);
  // A descendant counts: clicking a button's label is clicking the button.
  const covered = hit && !el.contains(hit) && hit !== el;
  return JSON.stringify({
    x: x, y: y,
    covered: covered,
    by: covered ? (hit.tagName.toLowerCase() + (hit.id ? "#" + hit.id : "")) : "",
  });
})()`, jsStr(sel), jsStr(sel)),
		"returnByValue": true,
		"userGesture":   true,
	})
	if err != nil {
		return "", err
	}
	result, err := c.reply(id, deadline)
	if err != nil {
		return "", err
	}
	raw, err := evalResult(result)
	if err != nil {
		return "", err
	}
	var box struct {
		X, Y    float64
		Error   string
		Covered bool
		By      string
	}
	if err := json.Unmarshal([]byte(raw), &box); err != nil {
		return "", fmt.Errorf("could not read the element's position: %s", raw)
	}
	if box.Error != "" {
		return "", fmt.Errorf("%s", box.Error)
	}
	if box.Covered {
		return "", fmt.Errorf("%s is behind %s at that point — clicking would hit the other element", sel, box.By)
	}

	// Move first. Interfaces that open on hover need the pointer to arrive
	// before the press, and a press with no preceding move is not a gesture any
	// person could make.
	for _, ev := range []map[string]any{
		{"type": "mouseMoved", "x": box.X, "y": box.Y, "button": "none", "buttons": 0},
		{"type": "mousePressed", "x": box.X, "y": box.Y, "button": "left", "buttons": 1, "clickCount": 1},
		{"type": "mouseReleased", "x": box.X, "y": box.Y, "button": "left", "buttons": 0, "clickCount": 1},
	} {
		id, err := c.send("Input.dispatchMouseEvent", ev)
		if err != nil {
			return "", err
		}
		if _, err := c.reply(id, deadline); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("clicked %s", sel), nil
}

// cdpType puts text into a field through the browser's own input pipeline.
//
// Assigning el.value and firing a synthetic input event — which is what this
// did — writes something the page can see and, on any framework that tracks its
// own value, nothing the page believes. React replaces the value property with
// an accessor and remembers what it last saw, so an assignment updates the
// tracker on its way through; when the synthetic event then arrives, the value
// and the tracked value already agree and onChange is suppressed as a
// non-change. The field shows the text and the component's state stays empty,
// so a submit sends nothing and validation never runs. Reproduced against a
// page implementing that tracking: the input read "hello" and the page counted
// zero changes, while the tool reported success.
//
// Input.insertText goes in below all of that, at the same layer a keystroke
// arrives on, so the value change comes from the browser rather than from an
// assignment the framework can attribute to itself. It is what Playwright's
// fill() uses, for this exact reason.
func cdpType(sel, text string) (string, error) {
	c, err := cdpOpen()
	if err != nil {
		return "", err
	}
	defer c.Close()
	deadline := time.Now().Add(20 * time.Second)

	// Focus, and select what is already there so the text replaces rather than
	// appends. contenteditable has no select(), hence the range.
	id, err := c.send("Runtime.evaluate", map[string]any{
		"expression": fmt.Sprintf(`(()=>{
  const el = document.querySelector(%s);
  if (!el) return "ERROR: no such element " + %s;
  el.focus();
  if (typeof el.select === "function") { el.select(); }
  else {
    const r = document.createRange(); r.selectNodeContents(el);
    const s = getSelection(); s.removeAllRanges(); s.addRange(r);
  }
  return "ok";
})()`, jsStr(sel), jsStr(sel)),
		"returnByValue": true,
		"userGesture":   true,
	})
	if err != nil {
		return "", err
	}
	result, err := c.reply(id, deadline)
	if err != nil {
		return "", err
	}
	if got, err := evalResult(result); err != nil {
		return "", err
	} else if strings.HasPrefix(got, "ERROR") {
		return "", fmt.Errorf("%s", got)
	}

	// Empty text means clear the field, and inserting nothing does not delete a
	// selection — the caret event does.
	if text == "" {
		id, err = c.send("Input.dispatchKeyEvent", map[string]any{
			"type": "keyDown", "windowsVirtualKeyCode": 46, "key": "Delete", "code": "Delete",
		})
		if err != nil {
			return "", err
		}
		if _, err := c.reply(id, deadline); err != nil {
			return "", err
		}
		return "cleared " + sel, nil
	}

	id, err = c.send("Input.insertText", map[string]any{"text": text})
	if err != nil {
		return "", err
	}
	if _, err := c.reply(id, deadline); err != nil {
		return "", err
	}
	return "typed into " + sel, nil
}

// cdpNavigate goes to a URL and waits until the page has actually loaded.
//
// browser_goto used to assign location.href and report "navigating" — true at
// the instant it was said and useless a moment later, because the caller was
// then holding a success message for a page that did not exist yet. Every tool
// called after it raced the load, and the usual repair was for the model to
// guess at a sleep.
//
// Page.navigate reports failures location.href cannot: a bad scheme, a blocked
// URL, a host that does not resolve, all of which are silent when assigned to
// href. Page.loadEventFired then says when the document is done, which is the
// question the caller was really asking.
func cdpNavigate(url string, timeout time.Duration) (string, error) {
	c, err := cdpOpen()
	if err != nil {
		return "", err
	}
	defer c.Close()
	deadline := time.Now().Add(timeout)

	// Page.enable first, or the load events never arrive at all.
	id, err := c.send("Page.enable", nil)
	if err != nil {
		return "", err
	}
	if _, err := c.reply(id, deadline); err != nil {
		return "", fmt.Errorf("enable page events: %w", err)
	}

	id, err = c.send("Page.navigate", map[string]any{"url": url})
	if err != nil {
		return "", err
	}
	result, err := c.reply(id, deadline)
	if err != nil {
		return "", err
	}
	if msg, ok := result["errorText"].(string); ok && msg != "" {
		return "", fmt.Errorf("navigation refused: %s", msg)
	}

	method, _, err := c.event(deadline, "Page.loadEventFired", "Page.frameStoppedLoading")
	if err != nil {
		// The navigation was accepted; only the confirmation is missing. Saying
		// so is more useful than an error, because the page may well be there.
		return fmt.Sprintf("navigated to %s, but it had not finished loading after %s", url, timeout), nil
	}
	if method == "Page.frameStoppedLoading" {
		return fmt.Sprintf("navigated to %s (frame stopped loading)", url), nil
	}
	return fmt.Sprintf("loaded %s", url), nil
}
