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

// The tool registry: what each tool is, what it can do to the machine, and how
// an agent finds the handful it needs among a hundred and twenty.
//
// Two problems are solved here, and they turn out to be the same problem.
//
// The first is classification. Risk used to live in two flat maps at the top of
// mcppolicy.go, three hundred lines away from the tools they described, and
// nothing connected one to the other. A tool missing from both maps was refused
// under MCP_POLICY=readonly and permitted under MCP_POLICY=safe — quietly, with
// no way to notice. By the time the maps were checked against the catalogue,
// forty-six of the hundred and fourteen tools had drifted into that gap: not
// only harmless ones like shell_read and room_state being refused, but
// terminal_run and terminal_open — which type a command line into a shell and
// press Return — being allowed under the level whose entire promise is that it
// does not run code. Risk now lives on the toolDef, beside the schema, and the
// maps are derived from it. The failure it replaces was silent; this one is a
// startup error.
//
// The second is discovery. A hundred and twenty schemas is a large fraction of
// a model's context spent before it reads the request, and most hosts pay it on
// every turn. Some of them already defer tool loading and search on demand;
// where the host does not, MCP_DISCOVERY=1 does it from this side — tools/list
// answers with a small core plus tool_search, and everything else stays
// callable by name. That asymmetry is the whole design: discovery narrows what
// is *advertised*, never what is *permitted*. Policy remains the only thing
// that can refuse a call, so a hidden tool is one the model has not been told
// about yet, not one it is forbidden to use.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sentineldesk/desktop/internal/toolsearch"
	"github.com/sentineldesk/desktop/pkg/capability"
)

// --- categories --------------------------------------------------------------

// categoryOf is the shared classifier, re-exported under the name this package
// has always used. The rules moved to internal/toolsearch with the ranking that
// consumes them — see ADR-003 — and a one-line forwarder is cheaper than
// renaming every call site to prove a point about where a function lives.
func categoryOf(name string) string { return toolsearch.CategoryOf(name) }

// --- validation ----------------------------------------------------------------

// validateCatalogue is the structural check from internal/capability plus the
// one rule that is this adapter's own: MCP's search vocabulary must not name a
// tool the catalogue no longer has. The stranded entry would never match
// anything and nothing would ever say so — the tool would simply get harder to
// find, which is the failure mode this whole registry was written to stop
// being silent. The check stays here rather than moving with the type because
// tool_search is an MCP feature, not a property of a capability.
func validateCatalogue(tools []toolDef) error {
	var problems []string
	if err := capability.Validate(tools); err != nil {
		problems = append(problems, err.Error())
	}
	seen := capability.BuildNameIndex(tools)
	var stranded []string
	for _, name := range toolsearch.KeywordedTools() {
		if !seen[name] {
			stranded = append(stranded, name)
		}
	}
	if len(stranded) > 0 {
		sort.Strings(stranded)
		problems = append(problems, fmt.Sprintf(
			"toolsearch's vocabulary names %d tool(s) that are not in the catalogue: %s",
			len(stranded), strings.Join(stranded, ", ")))
	}
	if len(problems) > 0 {
		return fmt.Errorf("mcp catalogue: %s", strings.Join(problems, "; "))
	}
	return nil
}

// The index types moved to internal/capability with the definition they key.
// The names below survive as aliases and forwarders because the catalogue and
// its tests speak them; the implementations live beside the type so the
// DataChannel adapter reads the same answers.

type riskIndex = capability.RiskIndex

func buildRiskIndex(tools []toolDef) riskIndex { return capability.BuildRiskIndex(tools) }

type controlIndex = capability.ControlIndex

func buildControlIndex(tools []toolDef) controlIndex { return capability.BuildControlIndex(tools) }

// --- the shared catalogue ---------------------------------------------------------

// Catalogue is the menu, bound: every verb this desktop understands, with its
// indexes built, as one handle another adapter can hold.
//
// This is what main.go hands the Room so the DataChannel gates its verbs off
// the same card the agent's calls are gated by (§4.6). It is a package
// function rather than a Server method because the menu exists whether or not
// an MCP server was started — the definitions are literals, and building them
// off a zero Server is exactly what the registry tests have always done.
func Catalogue() *capability.Catalogue {
	return capability.NewCatalogue((&Server{}).buildTools())
}

// --- denial kinds ----------------------------------------------------------------

// denialKind says why a tools/call did not succeed, in a form a program can
// branch on.
//
// The sentence a caller gets back is written for a model to read, and it is
// rewritten whenever a better sentence is found — this repository does that
// routinely. A client that has to tell a policy refusal from a room refusal by
// matching substrings is therefore one wording change away from breaking, and
// the three cases need genuinely different responses: a policy refusal is
// final and the model should be told the capability is not available; a room
// refusal means ask a person and try again; a tool failure may be worth
// retrying. Guessing wrong turns "wait your turn" into "give up".
//
// So the reason travels twice: the prose in the content, unchanged, and this
// alongside it.
type denialKind string

const (
	// denialPolicy — MCP_POLICY, MCP_DENY or MCP_ALLOW refused the call. Final:
	// nothing the caller does will change it within this connection.
	denialPolicy denialKind = "policy"

	// denialRoom — the tool needs the desktop's controls and the agent does not
	// hold them. Not final: call request_control, or wait for whoever is
	// driving to finish.
	denialRoom denialKind = "room"

	// denialUnknown — no such tool in the catalogue.
	denialUnknown denialKind = "unknown_tool"

	// denialToolError — the tool ran and reported failure. This is the residual
	// case and it is deliberately coarse: tools validate their own arguments
	// and report in prose, so a per-tool argument kind would mean touching all
	// of them.
	denialToolError denialKind = "tool_error"

	// denialBadArgs — an argument was sent that the tool does not have.
	//
	// This is the case the note above said to split out when something needed
	// it. Something did. Every tool ignored arguments it did not recognise, so
	// a caller that misremembered a name got no signal at all: ui_tree was
	// called with max_depth instead of depth three times, returned the full
	// tree each time, and read as a tool whose depth setting did nothing.
	// Reproduced on tools with nothing in common — wait accepted
	// totally_made_up_parameter, screenshot accepted nonsense_arg — so it was
	// never a tool's oversight but the absence of a check.
	//
	// It matters more for a model than for a person. A person notices the
	// output did not change; a model has only the reply, and a reply that
	// reports success for a call that quietly did something else is
	// indistinguishable from one that did what was asked.
	denialBadArgs denialKind = "bad_arguments"

	// denialCancelled — the call was stopped, by notifications/cancelled or by
	// the connection closing.
	//
	// This exists because without it a cancelled call reports success. A killed
	// process is just a process with a non-zero exit status, so run_command
	// answered {"exit_code": -1} and no error at all — true about the process
	// and a lie about the request. A client reading that would tell the model
	// its command ran and failed, when in fact the client itself had stopped
	// it. Whatever the tool managed to return describes work that was
	// interrupted, not work that was done.
	denialCancelled denialKind = "cancelled"

	// denialEmergency — this connection has been halted. Distinct from policy
	// because it is about who is calling rather than what they called, and it
	// is lifted by an operator rather than by asking differently.
	denialEmergency denialKind = "emergency"

	// denialApproval — MCP_POLICY=approve put the call to the people in the
	// room and it did not come back allowed: somebody said no, nobody
	// answered, or nobody was there to ask. Distinct from policy because it is
	// not final in the same way: a policy refusal will repeat forever on this
	// connection, while this one can end differently the next time somebody is
	// present and willing. The prose says which of the three it was.
	denialApproval denialKind = "approval"
)

// toolCallResult builds the tools/call result. An empty kind means success.
//
// _meta is where the specification puts extension data on a result, and the key
// is namespaced for the same reason the requiresControl annotation is: this is
// ours, and it should not collide with a field the protocol may later define.
func toolCallResult(content []map[string]any, kind denialKind) map[string]any {
	res := map[string]any{"content": content, "isError": kind != ""}
	if kind != "" {
		res["_meta"] = map[string]any{"sentineldesk/denial": string(kind)}
	}
	return res
}

type nameIndex = capability.NameIndex

func buildNameIndex(tools []toolDef) nameIndex { return capability.BuildNameIndex(tools) }

type argIndex = capability.ArgIndex

func buildArgIndex(tools []toolDef) argIndex { return capability.BuildArgIndex(tools) }

// --- discovery -----------------------------------------------------------------

// coreTools is what tools/list advertises when MCP_DISCOVERY is on: enough to
// look at the desktop, read its structure, click, type and run something — plus
// the one tool that finds the rest. An agent that never calls tool_search can
// still do useful work with only these; the point is that it no longer pays for
// a hundred and twenty schemas to find out whether it needs ssh_tunnel_remote.
var coreTools = map[string]bool{
	"tool_search":  true,
	"screenshot":   true,
	"ui_tree":      true,
	"ui_find":      true,
	"ui_click":     true,
	"mouse_click":  true,
	"type_text":    true,
	"key_combo":    true,
	"list_windows": true,
	"run_command":  true,
	"wait":         true,
	"room_state":   true,
}

// discoveryEnabled reports whether tools/list should be trimmed to the core.
//
// Off by default, and deliberately so: a host that already defers tool loading
// does this better than the server can, because it sees the conversation. This
// is for the hosts that do not.
func discoveryEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MCP_DISCOVERY"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// listedTools is what tools/list answers with: the connection's policy applied
// always, and the core-set narrowing applied only when discovery is on.
//
// The two filters are not the same kind of thing and must not be confused. The
// policy one is a permission and is enforced a second time in handleToolCall;
// this one is only about what gets mentioned, and nothing enforces it anywhere.
// A tool that discovery leaves out is still callable the moment the model knows
// its name — which is the entire mechanism, not a hole in it.
func (s *Server) listedTools(policy *Policy) []toolDef {
	allowed := policy.Filter(s.tools)
	if !s.discovery {
		return allowed
	}
	out := make([]toolDef, 0, len(coreTools))
	for _, t := range allowed {
		if coreTools[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// --- search --------------------------------------------------------------------

type searchHit struct {
	Name        string          `json:"name"`
	Category    string          `json:"category"`
	Risk        string          `json:"risk"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	score       int
}

// searchTools ranks the catalogue and puts the schemas back on.
//
// The ranking itself is in internal/toolsearch, shared with the agent runtime.
// What stays here is what only this side has: the input schema, so a caller can
// use a tool the moment it finds one rather than making a second request for
// its shape, and the risk level, so it knows what it is about to reach for.
func searchTools(tools []toolDef, query string, limit int) []searchHit {
	byName := make(map[string]toolDef, len(tools))
	flat := make([]toolsearch.Tool, 0, len(tools))
	for _, t := range tools {
		// tool_search does not answer questions about the desktop, and a model
		// running it already has it. Leaving it in means every search returns
		// itself, which is a wasted slot in a list of ten.
		if t.Name == "tool_search" {
			continue
		}
		byName[t.Name] = t
		flat = append(flat, toolsearch.Tool{Name: t.Name, Description: t.Description})
	}
	ranked := toolsearch.Rank(flat, query, limit)

	out := make([]searchHit, 0, len(ranked))
	for _, hit := range ranked {
		full := byName[hit.Name]
		out = append(out, searchHit{
			Name: hit.Name, Category: hit.Category, Risk: full.Risk.String(),
			Description: hit.Description, InputSchema: full.InputSchema,
			score: hit.Score,
		})
	}
	return out
}

// --- the tool itself -------------------------------------------------------------

func (s *Server) buildRegistryTools() []toolDef {
	return []toolDef{
		{
			Name: "tool_search",
			Description: "Find the tools for a task without loading all of them. " +
				"Give it a plain description of what you want to do — 'give someone " +
				"remote access', 'read the text on screen', 'open a tunnel' — and it " +
				"returns the best matching tools with their full input schemas, so " +
				"you can call them straight away. Every tool is callable by name " +
				"whether or not tools/list advertised it; this is how you learn the " +
				"names. Pass category to list a whole theme (ssh, browser, shell, " +
				"terminal, accessibility, windows, input, screen, files, processes, " +
				"packages, snapshot, recording, restream, room, audio, clipboard, " +
				"desktops, system).",
			Risk: riskRead,
			InputSchema: schema(map[string]any{
				"query":    pStr("what you are trying to do, in words"),
				"category": pStr("optional: restrict to one theme"),
				"limit":    pIntDef("how many to return (default 10)", 10),
			}),
		},
	}
}

// dispatchRegistry takes the connection's policy rather than reading one off
// the Server, because requests from different connections run concurrently and
// each may have restricted itself differently. Threading it through is a wider
// signature; the alternative was shared mutable state and a race.
func (s *Server) dispatchRegistry(ctx context.Context, name string, args map[string]any, policy *Policy) ([]map[string]any, bool, bool) {
	if name != "tool_search" {
		return nil, false, false
	}
	query := argStr(args, "query")
	category := strings.ToLower(strings.TrimSpace(argStr(args, "category")))
	limit := argInt(args, "limit")

	// The catalogue is filtered by the connection's policy before it is
	// searched. Turning up a tool the connection may never call would be a
	// worse answer than turning up nothing.
	pool := s.tools
	if policy != nil {
		pool = policy.Filter(pool)
	}
	if category != "" {
		var narrowed []toolDef
		for _, t := range pool {
			if categoryOf(t.Name) == category {
				narrowed = append(narrowed, t)
			}
		}
		// A category on its own is a valid request: list the theme, and only
		// the theme.
		if strings.TrimSpace(query) == "" {
			pool = narrowed
			if limit <= 0 {
				limit = len(pool)
			}
			query = category
		} else {
			// A category ALONGSIDE a query is a hint about where to look, not
			// a wall. It used to be a wall, and a wall excludes the right
			// answer whenever the guess is off by one theme: asking
			// category=packages for "list installed applications" returned
			// install, remove and search — the three tools that CHANGE what is
			// installed — while list_installed_apps sat under `processes` and
			// was never considered. The model gave up on tools and shelled out
			// to `ls /usr/share/applications`, which is a correct answer
			// arrived at the expensive way.
			//
			// Now the category boosts its own theme and everything else still
			// competes, so a good query cannot be beaten by a bad guess about
			// where its answer lives. The ranking already weighs a category
			// match above a description one; this just stops the filter from
			// deciding the outcome before the ranking runs.
			query = category + " " + query
		}
	}
	if strings.TrimSpace(query) == "" && category == "" {
		return textContent("`query` is missing: describe what you are trying to do"), true, true
	}

	hits := searchTools(pool, query, limit)
	if len(hits) == 0 {
		return textContent(
			"nothing matched %q. Categories: ssh, browser, shell, terminal, "+
				"accessibility, windows, input, screen, files, processes, packages, "+
				"snapshot, recording, restream, room, audio, clipboard, desktops, "+
				"system, general.", query), false, true
	}
	return jsonContent(map[string]any{
		"matched": len(hits),
		"of":      len(pool),
		"tools":   hits,
	}), false, true
}
