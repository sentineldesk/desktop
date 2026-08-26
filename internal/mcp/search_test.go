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

// How well tool_search finds the right tool.
//
// This is the first thing an agent does with any goal — decide which tool it is
// holding — and until this file existed there was no number attached to how
// often it got it right. The catalogue was checked against itself for
// consistency and never against the way a task is actually described.
//
// The corpus below is one query per tool, phrased as the goal rather than as
// the tool. That constraint is the whole value: writing "launch an app" for
// launch_app would measure nothing, because the answer is in the question. The
// queries here say "open the calculator application", and the first run of this
// test put launch_app nowhere in the top ten — four tools with the word "open"
// in their names took the slots.
//
// Two numbers are reported. recall@10 is what tool_search actually returns by
// default, so it is the one that decides whether the agent can see the tool at
// all. recall@3 is the one that decides whether it picks it without reading ten
// schemas first. The thresholds are floors that a regression trips, not targets
// to celebrate reaching.

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// searchCorpus is a natural-language goal for every tool in the catalogue.
//
// The rule when adding to it: describe what you want to happen, using the words
// someone would use who does not know the tool exists. If the query contains
// the tool's own name it is not a test, it is a lookup.
var searchCorpus = map[string]string{
	"action_log":            "show me the history of what has been done so far",
	"activate_window":       "bring the editor to the front",
	"ask_human":             "ask the person watching which one they meant",
	"browser_click":         "click the submit button on the web page",
	"browser_eval":          "run some javascript inside the page",
	"browser_goto":          "send the browser to a different address",
	"browser_open":          "open a website",
	"browser_tabs":          "what pages are open in the browser",
	"browser_text":          "read the contents of the web page",
	"browser_type":          "fill in a text box on the website",
	"browser_wait_for":      "wait until an element shows up in the page",
	"browser_wait_until":    "wait until the video finishes playing",
	"deliver_recording":     "send me the video you recorded earlier",
	"browser_element":       "is this button on the page actually visible and can I click it",
	"check_errors":          "did anything fail or report a problem",
	"close_window":          "close that window",
	"fill_form":             "complete this form with these values",
	"find_text":             "where on screen does it say Save",
	"fullscreen_window":     "make the window fill the entire display",
	"desktop_state":         "where do things stand on the desktop right now",
	"get_active_window":     "which window has focus right now",
	"get_audio_state":       "is the sound muted",
	"get_clipboard":         "what did I copy earlier",
	"get_desktop_info":      "which virtual workspace am I on",
	"get_mouse_position":    "where is the pointer right now",
	"get_pixel_color":       "what colour is the dot at that spot",
	"get_recording_status":  "am I still recording",
	"get_screen_info":       "what resolution is the display",
	"install_packages":      "install gimp",
	"is_running":            "is firefox running",
	"key_combo":             "press control and s together",
	"kill_process":          "kill the frozen program",
	"launch_app":            "open the calculator application",
	"list_commands":         "what programs can I run",
	"list_desktops":         "how many workspaces are there",
	"list_directory":        "what is inside this folder",
	"list_installed_apps":   "what applications are installed",
	"list_processes":        "what is running right now",
	"list_recordings":       "show me the videos I have made",
	"list_restreams":        "what broadcasts are live",
	"list_windows":          "what windows are open",
	"maximize_window":       "make the window as big as it goes",
	"minimize_window":       "get the window out of the way for now",
	"mouse_click":           "click at these coordinates",
	"mouse_down":            "hold the mouse button down",
	"mouse_drag":            "drag this icon onto that one",
	"mouse_move":            "move the pointer over there",
	"mouse_scroll":          "scroll further down",
	"mouse_up":              "let go of the mouse button",
	"move_window":           "put the window in the top left corner",
	"open_app_and_wait":     "start the editor and do not return until its window is there",
	"read_file":             "show me the contents of this file",
	"read_screen_text":      "what does the screen say",
	"release_control":       "give the controls back",
	"remove_packages":       "uninstall gimp",
	"remote_open":           "connect to a windows machine over rdp and show its desktop",
	"remote_close":          "disconnect the remote desktop session",
	"remote_list":           "which remote desktops are open right now",
	"remote_profile_save":   "save these remote desktop connection details to reuse later",
	"remote_profile_list":   "list my saved remote desktop profiles",
	"remote_profile_delete": "delete a saved remote desktop profile",
	"request_control":       "take the controls so I can act",
	"resize_window":         "make the window narrower",
	"restore_window":        "put the window back to the size it was",
	"room_state":            "who else is connected",
	"run_command":           "run this shell command once",
	"job_start":             "start a long download in the background and let it run",
	"job_status":            "is that background task still going or did it finish",
	"job_output":            "show me what the background task printed",
	"job_wait":              "wait until the download finishes before doing the next step",
	"sleep":                 "pause for three minutes while the screen recording runs",
	"job_abort":             "stop the task that is running, it was the wrong one",
	"job_list":              "what background work is running on this machine",
	"activity":              "what happened on this desktop while I was not looking",
	"secret_list":           "what passwords does this machine have that I can use",
	"type_secret":           "enter the password into the login form without showing it to me",
	"screenshot":            "take a picture of the screen",
	"screenshot_region":     "capture just this rectangle of the screen",
	"search_packages":       "is there a package for editing video",
	"service_control":       "restart the audio daemon",
	"system_updates":        "are there security patches pending on this desktop",
	"set_clipboard":         "put this text on the clipboard",
	"set_resolution":        "change the display to 1280 by 720",
	"set_volume":            "turn the sound down",
	"set_window_desktop":    "send this window to workspace two",
	"shell_close":           "end the persistent shell session",
	"shell_exec":            "run a command in the session I opened",
	"shell_input":           "send a line to the program that is waiting for input",
	"shell_list":            "what sessions do I have open",
	"shell_open":            "start a background session I can keep using",
	"shell_read":            "read what the session has printed",
	"snapshot_create":       "save the state of the desktop so I can come back to it",
	"snapshot_delete":       "throw away that checkpoint",
	"snapshot_list":         "what checkpoints exist",
	"snapshot_restore":      "roll back to the earlier state",
	"ssh_connect":           "log in to a remote machine",
	"ssh_copy_id":           "set up passwordless login on that server",
	"ssh_disconnect":        "close the connection to the remote machine",
	"ssh_download":          "fetch a file from the remote host",
	"ssh_exec":              "run a command on the remote machine",
	"ssh_keygen":            "make a new key pair",
	"ssh_list":              "which remote hosts am I connected to",
	"ssh_list_remote":       "list the files on the remote server",
	"ssh_tunnel_close":      "shut down that port forward",
	"ssh_tunnel_local":      "forward a local port to the remote machine",
	"ssh_tunnel_remote":     "expose my local service on the remote machine",
	"ssh_tunnels":           "what port forwards are open",
	"ssh_upload":            "send this file to the server",
	"start_recording":       "record a video of the desktop",
	"start_restream":        "broadcast the screen to youtube",
	"stop_recording":        "stop the video",
	"stop_restream":         "stop the broadcast",
	"subscribe_events":      "tell me when something changes instead of me asking every time",
	"unsubscribe_events":    "stop sending me notifications",
	"sudo_status":           "can I run things as root",
	"switch_desktop":        "go to the next workspace",
	"terminal_open":         "open a terminal window on the desktop",
	"terminal_read":         "what does the terminal show",
	"terminal_run":          "type a command into the terminal window",
	"type_text":             "type hello world",
	"ui_at_point":           "what is that thing at these coordinates on screen",
	"ui_click":              "click the OK button in the dialog",
	"ui_diff":               "what changed on screen since I last looked",
	"ui_find":               "find the search box in the application",
	"ui_focus":              "put the cursor in that field",
	"ui_get_text":           "read what is in that field",
	"ui_set_text":           "put this text into the field",
	"ui_tree":               "show me the structure of the application",
	"ui_wait_for":           "wait for the dialog to appear",
	"wait":                  "pause for two seconds",
	"wait_for_idle":         "wait until the screen stops changing",
	"wait_for_event":        "wait until something happens on the desktop and tell me what it was",
	"wait_for_window":       "wait until the window opens",
	"window_hierarchy":      "show the parent and child windows",
	"window_properties":     "what are the details of that window",
	"window_set_state":      "keep this window above the others",
	"write_file":            "save this text to a file",
}

// rankOf returns the 1-based position of want in the results, or 0.
func rankOf(hits []searchHit, want string) int {
	for i, h := range hits {
		if h.Name == want {
			return i + 1
		}
	}
	return 0
}

// TestSearchCorpusCoversTheCatalogue keeps the corpus honest. A tool added
// without a query would quietly stop being measured, and the recall number
// would go up because the hard cases were the ones left out.
func TestSearchCorpusCoversTheCatalogue(t *testing.T) {
	tools := (&Server{}).buildTools()
	var missing []string
	for _, tool := range tools {
		// tool_search does not search for itself, by design.
		if tool.Name == "tool_search" {
			continue
		}
		if _, ok := searchCorpus[tool.Name]; !ok {
			missing = append(missing, tool.Name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d tools have no query in searchCorpus, so their recall is unmeasured: %s",
			len(missing), strings.Join(missing, ", "))
	}

	catalogue := map[string]bool{}
	for _, tool := range tools {
		catalogue[tool.Name] = true
	}
	var stale []string
	for name := range searchCorpus {
		if !catalogue[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("searchCorpus names tools that no longer exist: %s", strings.Join(stale, ", "))
	}
}

// TestSearchRecall is the measurement. It fails on a floor rather than asserting
// a fixed number, because the useful signal is "this got worse", and a test that
// pins the exact figure would have to be edited on every improvement — which is
// how a threshold quietly becomes a rubber stamp.
func TestSearchRecall(t *testing.T) {
	tools := (&Server{}).buildTools()

	// top10 is the one that is an invariant rather than a score. A tool the
	// search never returns cannot be called by an agent that does not already
	// know its name, so it is not a badly-ranked tool, it is an unreachable one.
	// Every tool in the catalogue is reachable today; a new one that is not
	// should fail the build rather than be discovered later by an agent that
	// went and did the job the hard way instead.
	//
	// The other two are floors under the current 82% and 93%, with room for a
	// legitimate change to move things about. They are not targets: the eight
	// queries that land at rank four to seven are all *found*, and pushing them
	// higher means tuning the searcher against a corpus written in this file,
	// which measures nothing except how well it was tuned.
	const (
		minTop1  = 0.78
		minTop3  = 0.90
		minTop10 = 1.0
	)

	var top1, top3, top10 int
	type miss struct {
		tool, query string
		rank        int
	}
	var misses []miss

	for _, tool := range tools {
		query, ok := searchCorpus[tool.Name]
		if !ok {
			continue
		}
		// Ten is what the tool returns when the caller does not say otherwise,
		// so it is what the agent actually sees.
		rank := rankOf(searchTools(tools, query, 10), tool.Name)
		switch {
		case rank == 1:
			top1++
			top3++
			top10++
		case rank >= 2 && rank <= 3:
			top3++
			top10++
		case rank >= 4:
			top10++
		}
		if rank == 0 || rank > 3 {
			misses = append(misses, miss{tool.Name, query, rank})
		}
	}

	total := len(searchCorpus)
	r1 := float64(top1) / float64(total)
	r3 := float64(top3) / float64(total)
	r10 := float64(top10) / float64(total)

	sort.Slice(misses, func(i, j int) bool {
		if misses[i].rank != misses[j].rank {
			// Not found at all is worse than found at eight.
			if misses[i].rank == 0 {
				return true
			}
			if misses[j].rank == 0 {
				return false
			}
			return misses[i].rank > misses[j].rank
		}
		return misses[i].tool < misses[j].tool
	})

	var report strings.Builder
	fmt.Fprintf(&report, "recall over %d queries: top1 %.0f%%  top3 %.0f%%  top10 %.0f%%",
		total, r1*100, r3*100, r10*100)
	for _, m := range misses {
		where := fmt.Sprintf("rank %d", m.rank)
		if m.rank == 0 {
			where = "NOT FOUND"
		}
		fmt.Fprintf(&report, "\n  %-22s %-9s %q", m.tool, where, m.query)
	}
	t.Log(report.String())

	if r10 < minTop10 {
		t.Errorf("top10 recall %.0f%% is below the %.0f%% floor — an agent cannot call a tool it never sees",
			r10*100, minTop10*100)
	}
	if r3 < minTop3 {
		t.Errorf("top3 recall %.0f%% is below the %.0f%% floor", r3*100, minTop3*100)
	}
	if r1 < minTop1 {
		t.Errorf("top1 recall %.0f%% is below the %.0f%% floor", r1*100, minTop1*100)
	}
}
