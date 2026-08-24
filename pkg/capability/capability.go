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

// Package capability is the room's menu: every verb a room understands,
// defined once, wherever the order comes from.
//
// The analogy the design doc (§4.6) uses is a hotel's room service. A suite
// has one menu; you can order from the phone by the bed or the tablet on the
// desk, but both read the SAME card and both land on the same bill. Nobody
// prints a second, different menu for the tablet — that is how a guest ends up
// told two different prices for the same sandwich. This package is that one
// card. MCP and the browser's DataChannel are the phone and the tablet: two
// thin adapters over one definition of what a room can do.
//
// The card was printed inside internal/mcp for a long time, and the price of
// that address was measured before it was paid for again: the DataChannel grew
// its own opinions about which verbs need the desktop's controls, and by the
// time the two were compared they disagreed about screenshots and recording —
// gated for a person, free for the agent, with nothing anywhere saying which
// answer was meant. The same class of drift inside MCP itself once left
// forty-six tools unclassified. The cure is the same in both cases: the
// classification lives ON the definition, the definition lives in ONE package,
// and everything that enforces a rule derives it from here.
//
// What lives here is deliberately narrow: the definition type, its
// classification axes (risk, visibility, the control gate), the validation
// that refuses an unclassified verb at startup, and the indexes a dispatcher
// consults per call. What does NOT live here is just as deliberate:
//
//   - The catalogue DATA stays with the MCP adapter, whose descriptions are
//     written for a model to read. This package is the type system and the
//     rules, not the prose.
//   - Transport edges stay in their transport. Raw 60fps pointer events and
//     SDP/ICE belong to the human wire; skills, memory and job orchestration
//     belong to MCP. Forcing the edges into the core would be a fake
//     unification — the design doc's words, kept on purpose.
//   - Permission models are not merged. MCP_POLICY answers what an agent may
//     EVER do; RBAC answers what a person may do. Each adapter applies its
//     own. What both share is the control-lease gate (RequiresControl, one
//     controller, claimed never assumed) and the audit trail.
//
// It is a definition, not a service: data compiled into each runtime,
// executed in-process. One source of truth is not one instance — there is no
// capability server a hundred rooms can jam, the same way a printed menu in
// every suite does not queue at a central kitchen.
package capability

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// --- risk --------------------------------------------------------------------

// Risk is what a verb can do to the machine, and the only input the three
// MCP_POLICY levels need.
type Risk int

const (
	// RiskUnset is the zero value and never a valid answer. That is the point:
	// a Def written without a Risk fails at startup instead of inheriting
	// whatever the surrounding map happened to say.
	RiskUnset Risk = iota

	// RiskRead observes and changes nothing. These are the only verbs that
	// survive MCP_POLICY=readonly.
	RiskRead

	// RiskWrite drives the desktop — input, windows, volume, the clipboard. It
	// changes what is on screen, which is what an agent is for, but it cannot
	// reach past the desktop to the system underneath.
	RiskWrite

	// RiskDanger runs code, touches the system, or moves data outward. These
	// are what MCP_POLICY=safe removes.
	RiskDanger
)

func (r Risk) String() string {
	switch r {
	case RiskRead:
		return "read"
	case RiskWrite:
		return "write"
	case RiskDanger:
		return "danger"
	}
	return "unclassified"
}

// --- visibility ----------------------------------------------------------------

// Visibility is whether a person sharing the desktop sees a verb act.
type Visibility int

const (
	// VisUnset is the zero value and, for a verb that changes anything, never
	// a valid answer — same reasoning as RiskUnset. The difference is that
	// here there IS a correct default for one class of verb, and it is proved
	// rather than assumed: see VisHidden.
	VisUnset Visibility = iota

	// VisHidden changes state and puts nothing on the screen: write_file, and
	// the ssh_* and shell_* families.
	//
	// This list keeps shrinking, and that is the direction of travel rather
	// than an accident. run_command left it when every command started running
	// in a terminal window on the shared screen, and install_packages left it
	// for the same reason afterwards. What remains here is what nobody has yet
	// found a way to show.
	//
	// Every RiskRead verb is also this, by construction rather than by
	// declaration: a verb that changes nothing cannot be seen changing
	// something.
	VisHidden

	// VisVisible changes what is on the screen without injecting input:
	// launching an application, moving a window, driving the browser through
	// DevTools. A person watching sees the result appear; they do not see it
	// being done.
	VisVisible

	// VisInjects drives the desktop the way a person would — pointer, keyboard,
	// or text through the accessibility layer. A person watching sees
	// the pointer move and the characters arrive. These are exactly the verbs
	// that must hold the room's controls first, and Validate enforces that: a
	// verb claiming to inject without RequiresControl would be typing into
	// somebody else's session.
	VisInjects
)

func (v Visibility) String() string {
	switch v {
	case VisHidden:
		return "hidden"
	case VisVisible:
		return "visible"
	case VisInjects:
		return "injects"
	}
	return "unclassified"
}

// --- the definition -------------------------------------------------------------

// Def is one entry on the menu: name, description, input schema and what the
// verb is allowed to do to the machine.
type Def struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`

	// Risk is mandatory. It drives the MCP_POLICY levels and the annotations
	// published in tools/list, and it lives here rather than in a table
	// elsewhere so that adding a verb and classifying it are the same edit.
	// The zero value is RiskUnset and fails at startup; there is no safe
	// default to fall back on, because the two plausible ones point in
	// opposite directions.
	Risk Risk `json:"-"`

	// Visibility answers a question neither Risk nor RequiresControl can: will
	// a person sharing this desktop SEE this happen?
	//
	// It is a third axis, not a rewording of the other two. RequiresControl
	// looks like a proxy for it and is not — it means "injects events through
	// XTEST", so browser_open, which puts a page on the screen everyone is
	// watching, is ungated and therefore reads as invisible under that proxy.
	//
	// The case that forced it: run_command and terminal_run do the same job.
	// One is invisible and ungated, the other visible and gated. Nothing said
	// which was appropriate, so a model picked run_command every time and a
	// person watching the desktop while an agent worked saw nothing at all.
	// The runtime reads this field to substitute the visible verb when its
	// role says so.
	//
	// Mandatory for anything that changes something, like Risk. A RiskRead
	// verb is VisHidden by construction and does not declare it.
	Visibility Visibility `json:"-"`

	// RequiresControl marks the verbs that must hold the room's controls
	// before they run — the ones that put events into X, plus the ones that
	// publish the desktop outside the room.
	//
	// This is the ONE gate both wires share (§4.6): the agent passes it in
	// handleToolCall, the person passes it in the DataChannel handler, and
	// both read the answer from here so they cannot disagree about it.
	//
	// Unlike Risk this has a meaningful default: most verbs do not need the
	// desktop, and false is the conservative answer because it grants nothing.
	RequiresControl bool `json:"-"`

	// TimeoutMS is how long this verb may take before a client is entitled to
	// stop waiting for it. Zero means unbounded, which is the honest default:
	// most verbs answer immediately and a deadline on one that legitimately
	// takes minutes would be a limit that fires on success.
	//
	// It lives HERE, beside Risk and Visibility, for the reason those do: only
	// the verb's author knows how long it should take. A runtime keeping its
	// own table would be a second copy of a classification, and the copy that
	// drifts is always the one further from the tool.
	//
	// Cooperative, not a kill. A client that reaches this deadline stops
	// WAITING; the work may still be running here. That distinction is the
	// whole reason to publish a number rather than a promise — a client told
	// "cancelled" when nothing was cancelled would report a lie at the moment
	// it matters most.
	TimeoutMS int `json:"-"`
}

// EffectiveVisibility is what to publish: the declared value, or VisHidden for
// a read-only verb that correctly did not declare one.
func (t Def) EffectiveVisibility() Visibility {
	if t.Visibility != VisUnset {
		return t.Visibility
	}
	if t.Risk == RiskRead {
		return VisHidden
	}
	return VisUnset
}

// Annotations translates the classification into the hints the MCP
// specification defines, so a host that understands them can shape its own
// permission prompt without knowing anything about MCP_POLICY. It costs
// nothing to publish and it is the standard place for exactly this.
func (t Def) Annotations() map[string]any {
	a := map[string]any{
		"readOnlyHint":    t.Risk == RiskRead,
		"destructiveHint": t.Risk == RiskDanger,

		// Not in the specification, and namespaced so it cannot collide with
		// something that later is. It answers a question no standard hint does
		// and that a client cannot work out for itself: will this call be held
		// at the room gate until the caller holds the controls?
		//
		// Risk is no substitute. ui_click is write and gated, set_volume is
		// write and not; start_restream is danger and gated, write_file is
		// danger and not. Without this published, a client that wants to ask
		// for control at the right moment has to carry its own copy of the
		// list — which is the drift this whole package exists to end.
		"sentineldesk/requiresControl": t.RequiresControl,

		// Whether a person sharing the desktop sees this happen: hidden,
		// visible or injects. Also not in the specification, and also not
		// derivable by a client — requiresControl looks like the same question
		// and is not.
		"sentineldesk/visibility": t.EffectiveVisibility().String(),
	}

	// Published only when declared, so a client can tell "no deadline" from
	// "a deadline of zero" without a sentinel. Absent means unbounded.
	if t.TimeoutMS > 0 {
		a["sentineldesk/timeoutMs"] = t.TimeoutMS
	}
	return a
}

// MarshalJSON writes the wire form: the three fields the MCP specification
// requires plus the annotations derived above. Risk itself stays out — it is
// the input to the hints, not a second copy of them.
func (t Def) MarshalJSON() ([]byte, error) {
	type wire Def // a distinct type, so this method is not called again
	return json.Marshal(struct {
		wire
		Annotations map[string]any `json:"annotations"`
	}{wire(t), t.Annotations()})
}

// --- JSON Schema helpers -------------------------------------------------

// Schema assembles the input schema for a Def, with PStr/PInt/PIntDef/PBool as
// its property helpers. They are here rather than beside the catalogue data
// because they are part of what a definition IS, not part of any one
// transport's prose.

func Schema(props map[string]any, required ...string) json.RawMessage {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	b, _ := json.Marshal(m)
	return b
}

func PStr(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func PInt(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

// PIntDef is an integer parameter whose default is stated as a FACT as well as
// in the sentence.
//
// A description saying "timeout, default 15000" tells a model and tells
// nothing else. Everything that is not a model — a client drawing a countdown,
// a form generator, anything reading the catalogue as an interface — has to
// either ignore the default or regex English prose to find it, and the second
// is worse than the first. The prose keeps its "(default 15000)" because that
// is what a model reads, and the schema carries the same value where a program
// can find it; the registry tests assert the two agree, so the duplication
// cannot drift.
func PIntDef(desc string, def int) map[string]any {
	return map[string]any{"type": "integer", "description": desc, "default": def}
}
func PBool(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }

// --- validation ----------------------------------------------------------------

// Validate reports every verb that was defined without a classification.
//
// Adding a verb means writing a Def and a dispatch case; before this, it also
// meant remembering maps in other files, and forgetting them failed in the
// direction of granting access. The catalogue is the single source and the
// check runs on the way up, so the mistake costs a startup message rather than
// a permission nobody meant to give.
func Validate(defs []Def) error {
	var unclassified []string
	seen := map[string]bool{}
	var dupes []string
	var unseen, readNotHidden, injectsUngated []string
	for _, t := range defs {
		if t.Risk == RiskUnset {
			unclassified = append(unclassified, t.Name)
		}
		switch {
		case t.Risk == RiskRead:
			// Hidden by construction, so declaring anything else is a
			// contradiction rather than a preference. Declaring VisHidden
			// explicitly is allowed and redundant; declaring visible or
			// injects means one of the two fields is wrong.
			if t.Visibility != VisUnset && t.Visibility != VisHidden {
				readNotHidden = append(readNotHidden, t.Name)
			}
		case t.Visibility == VisUnset:
			unseen = append(unseen, t.Name)
		}
		if t.Visibility == VisInjects && !t.RequiresControl {
			injectsUngated = append(injectsUngated, t.Name)
		}
		if seen[t.Name] {
			dupes = append(dupes, t.Name)
		}
		seen[t.Name] = true
	}
	var problems []string
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		problems = append(problems, fmt.Sprintf(
			"%d tool(s) with no Risk: %s — add RiskRead, RiskWrite or RiskDanger to the definition",
			len(unclassified), strings.Join(unclassified, ", ")))
	}
	if len(dupes) > 0 {
		sort.Strings(dupes)
		problems = append(problems, "duplicate tool name(s): "+strings.Join(dupes, ", "))
	}
	if len(unseen) > 0 {
		sort.Strings(unseen)
		problems = append(problems, fmt.Sprintf(
			"%d tool(s) that change something with no Visibility: %s — "+
				"add VisHidden, VisVisible or VisInjects to the definition",
			len(unseen), strings.Join(unseen, ", ")))
	}
	if len(readNotHidden) > 0 {
		sort.Strings(readNotHidden)
		problems = append(problems, fmt.Sprintf(
			"read-only tool(s) declaring they can be seen: %s — a tool that "+
				"changes nothing cannot be visible; one of Risk or Visibility is wrong",
			strings.Join(readNotHidden, ", ")))
	}
	if len(injectsUngated) > 0 {
		sort.Strings(injectsUngated)
		problems = append(problems, fmt.Sprintf(
			"tool(s) that inject input without RequiresControl: %s — "+
				"they would be typing into somebody else's session",
			strings.Join(injectsUngated, ", ")))
	}
	if len(problems) > 0 {
		return fmt.Errorf("capability catalogue: %s", strings.Join(problems, "; "))
	}
	return nil
}

// --- indexes ---------------------------------------------------------------------

// The catalogue keyed for the questions a dispatcher asks per call. Built once
// at startup, which is why they are maps and not scans.

// RiskIndex answers "what may this verb do to the machine".
type RiskIndex map[string]Risk

func BuildRiskIndex(defs []Def) RiskIndex {
	idx := make(RiskIndex, len(defs))
	for _, t := range defs {
		idx[t.Name] = t.Risk
	}
	return idx
}

// ControlIndex is the set of verbs the room gates, derived from the catalogue
// the same way the risk maps are. It replaced a switch statement for the same
// reason the risk maps went: a list of names kept apart from the verbs it
// describes is a list that stops describing them.
type ControlIndex map[string]bool

func BuildControlIndex(defs []Def) ControlIndex {
	idx := make(ControlIndex)
	for _, t := range defs {
		if t.RequiresControl {
			idx[t.Name] = true
		}
	}
	return idx
}

// NameIndex is the set of verb names the catalogue defines, so that a call for
// something that does not exist can be told apart from one that was refused.
type NameIndex map[string]bool

func BuildNameIndex(defs []Def) NameIndex {
	idx := make(NameIndex, len(defs))
	for _, t := range defs {
		idx[t.Name] = true
	}
	return idx
}

// ArgIndex is the set of argument names each verb declares.
type ArgIndex map[string]map[string]bool

// BuildArgIndex reads the property names out of every verb's input schema.
//
// The schemas were already being published to clients and then never consulted
// again, which is how an argument no verb has could be accepted by all of
// them. Indexing at startup means the check costs a map lookup per call, and
// means a verb cannot forget it — the same reasoning that moved risk and the
// room gate onto the definition.
func BuildArgIndex(defs []Def) ArgIndex {
	idx := make(ArgIndex, len(defs))
	for _, t := range defs {
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		// A verb with no properties gets an empty set, which correctly refuses
		// every argument rather than accepting any.
		_ = json.Unmarshal(t.InputSchema, &schema)
		names := make(map[string]bool, len(schema.Properties))
		for name := range schema.Properties {
			names[name] = true
		}
		idx[t.Name] = names
	}
	return idx
}

// UnknownArgs returns the argument names this verb does not declare, sorted so
// the message is the same every time it is produced.
func (idx ArgIndex) UnknownArgs(tool string, args map[string]any) []string {
	known, ok := idx[tool]
	if !ok {
		return nil
	}
	var bad []string
	for name := range args {
		// _meta is the MCP specification's own extension slot and belongs to
		// the protocol rather than to the verb, so it is never a verb's
		// argument and never a mistake.
		if name == "_meta" || known[name] {
			continue
		}
		bad = append(bad, name)
	}
	sort.Strings(bad)
	return bad
}

// Declared lists the argument names a verb does take, for the refusal message.
func (idx ArgIndex) Declared(tool string) []string {
	names := make([]string, 0, len(idx[tool]))
	for name := range idx[tool] {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// --- the catalogue, bound ---------------------------------------------------------

// Catalogue is the menu with its indexes built: one handle a consumer that is
// not the MCP server — the Room, a future adapter — can hold and ask.
type Catalogue struct {
	defs    []Def
	risk    RiskIndex
	control ControlIndex
	known   NameIndex
	args    ArgIndex
}

// NewCatalogue binds a set of definitions. It does not validate — that is the
// owner's startup decision, kept separate so a test can build a deliberately
// broken catalogue and watch Validate refuse it.
func NewCatalogue(defs []Def) *Catalogue {
	return &Catalogue{
		defs:    defs,
		risk:    BuildRiskIndex(defs),
		control: BuildControlIndex(defs),
		known:   BuildNameIndex(defs),
		args:    BuildArgIndex(defs),
	}
}

// Known reports whether the menu lists this verb at all.
func (c *Catalogue) Known(name string) bool { return c.known[name] }

// RiskOf answers the verb's classification, RiskUnset for a verb not listed.
func (c *Catalogue) RiskOf(name string) Risk { return c.risk[name] }

// RequiresControl is the shared control-lease gate: whether this verb must
// hold the room's controls before it runs. Every adapter asks HERE, which is
// what makes the answer the same on every wire.
func (c *Catalogue) RequiresControl(name string) bool { return c.control[name] }

// UnknownArgs returns the argument names this verb does not declare.
func (c *Catalogue) UnknownArgs(name string, args map[string]any) []string {
	return c.args.UnknownArgs(name, args)
}

// DeclaredArgs lists the argument names a verb does take.
func (c *Catalogue) DeclaredArgs(name string) []string { return c.args.Declared(name) }

// Defs hands back the definitions, for a consumer that lists rather than asks.
func (c *Catalogue) Defs() []Def { return c.defs }

// Len is how many verbs the menu carries.
func (c *Catalogue) Len() int { return len(c.defs) }
