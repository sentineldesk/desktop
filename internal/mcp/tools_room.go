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

// Sharing the desktop with the people who are also using it.
//
// The agent used to be invisible and unaccountable in equal measure: it injected
// straight into X, so a person watching saw the pointer move on its own with no
// way to tell whether a colleague or the model was driving, and the agent had no
// way to find out that anybody was there at all.
//
// These tools close both halves. The agent joins the room as an ordinary
// participant — a name in the list, a marker on screen, a turn in the control
// rotation — and can read the room before acting.
//
// The rule that makes it work is one line:
//
//	Control is claimed, never assumed.
//
// That holds whether or not anybody is watching. An empty room used to be a
// free pass, on the reasoning that asking permission from nobody is theatre —
// but it left the room unable to say who had the desktop, and it meant the
// agent behaved differently depending on who happened to be connected.
// request_control answers instantly when the controls are free, so asking costs
// one call, and the room always knows the answer.
//
// Releasing hands the controls to nobody. Control goes FREE and stays there
// until somebody claims it, so "I have finished" never reads as "you are up".

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sentineldesk/desktop/internal/media"
	"github.com/sentineldesk/desktop/internal/stream"
)

// Rooms is what the MCP server needs from the room. An interface rather than
// the concrete type because a bridge process has no room at all, and the tools
// have to degrade politely instead of panicking.
type Rooms interface {
	JoinAgent(name string) string
	LeaveAgent()
	AskForControl(timeout time.Duration) (bool, string)
	Members() []stream.MemberInfo
	HumansPresent() bool
	Controller() (string, string)
	IsController(id string) bool
	TakeControl(id string) bool
	ReleaseControl(id string)
	UpdatePointer(id string, x, y int)

	// Presence changes, so the agent can be told the controls moved instead of
	// finding out when its next injection is refused. See events.go.
	WatchPresence(fn func()) func()

	// Putting a question to the people watching, and waiting for an answer.
	AskHuman(text string, options []string, timeout time.Duration) (string, error)

	// Telling them something without waiting. Used for the credential warning,
	// which reports what has already happened and must not hold the agent
	// hostage to somebody acknowledging it.
	Notice(kind string, items []string)

	// Putting the agent's conversation with a person on the record, without
	// asking the room to do anything about it. This is how a question answered
	// somewhere else — the terminal the person typed the goal into — still
	// reaches a chat panel here. Fire and forget, like Notice.
	AgentChat(turn stream.ChatTurn)

	// Asking for a value the answer to which must not be echoed on screen and
	// must not come back to the agent. See secrets.go.
	AskSecret(text string, timeout time.Duration) (string, error)

	// The live capture can be forwarded to an external destination without
	// encoding it a second time, so the agent uses the same path the toolbar
	// does rather than raising a capture of its own.
	StartRestream(t media.RestreamTarget) error
	StopRestream(id string) error
	Restreams() []media.RestreamInfo
	CanRestream() bool
}

// AgentID is the room identity of the MCP plane.
const AgentID = "agent"

func (s *Server) roomTools() []toolDef {
	return []toolDef{
		{
			Name: "room_state",
			Risk: riskRead,
			Description: "Who else is on this desktop right now: participants, " +
				"who holds control, and whether you may inject input. Call this " +
				"before acting when you might be sharing the session with a person.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:       "ask_human",
			Visibility: visVisible,
			Risk:       riskWrite,
			Description: "Ask the people watching a question and wait for the answer. " +
				"A prompt appears on their screen; pass `options` to give them buttons, " +
				"or leave it out for free text. Use this when the answer is theirs to " +
				"give rather than yours to guess — which file did they mean, is this the " +
				"right account, do they want to watch you do this or have it run in the " +
				"background. No answer is not an answer: a timeout is reported as one, " +
				"never as a default. Fails when nobody is here, so ask room_state first " +
				"if you might be alone.",
			InputSchema: schema(map[string]any{
				"question":   pStr("what to ask, in one sentence"),
				"options":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "optional: the answers to offer as buttons"},
				"timeout_ms": pIntDef("how long to wait (default 120000)", 120000),
			}, "question"),
		},
		{
			Name:       "request_control",
			Visibility: visVisible,
			Risk:       riskWrite,
			Description: "Ask the people watching for control of the desktop so you " +
				"can move the mouse and type. THEY DECIDE: a prompt appears on their " +
				"screen and this waits for the answer. No answer means no. With the " +
				"controls free — nobody driving, or nobody here at all — it is " +
				"granted immediately. Ask every time: control is never automatic.",
			InputSchema: schema(map[string]any{
				"timeout_ms": pIntDef("how long to wait for an answer (default 45000)", 45000),
			}),
		},
		{
			Name:       "release_control",
			Visibility: visVisible,
			Risk:       riskWrite,
			Description: "Hand control back to the people watching. Do this when you " +
				"finish a task, so a person does not have to take it from you.",
			InputSchema: schema(map[string]any{}),
		},
	}
}

func (s *Server) callRoom(ctx context.Context, name string, args map[string]any) (any, bool, bool) {
	// Claim the three room tools and nothing else.
	//
	// The nil check used to come first and report handled for EVERY name, so a
	// Server built without SetRoom answered "this build has no room attached"
	// to screenshot, run_command and all the rest — dispatch tries this
	// dispatcher early and stops at the first one that claims the call. The
	// whole catalogue was dead, and the message blamed the room.
	//
	// It never fired in the daemon, which always calls SetRoom, which is why it
	// went unnoticed. It fires immediately anywhere else the server is
	// embedded, and it contradicts the rule the rest of this project follows:
	// an optional capability degrades, it does not take everything with it. No
	// room means unarbitrated, not unusable.
	switch name {
	case "room_state", "request_control", "release_control", "ask_human":
	default:
		return nil, false, false
	}
	if s.room == nil {
		return textContent("this build has no room attached; input is unarbitrated"), true, true
	}

	switch name {
	case "room_state":
		s.room.JoinAgent(s.agentName)
		humans := s.room.HumansPresent()
		ctlID, ctlName := s.room.Controller()
		members := s.room.Members()

		people := make([]map[string]any, 0, len(members))
		for _, m := range members {
			people = append(people, map[string]any{
				"id": m.ID, "name": m.Name,
				"controller": m.Controller, "agent": m.Agent,
				"seconds": m.Seconds,
			})
		}
		return map[string]any{
			"participants":     people,
			"humans_present":   humans,
			"controller":       ctlName,
			"controller_id":    ctlID,
			"you_have_control": s.room.IsController(AgentID),
			// Spelled out rather than left for the model to infer: the whole
			// point is that it should not have to guess whether it may act.
			"may_inject": s.room.IsController(AgentID),
			"note": "Control is always claimed, never assumed — even with nobody " +
				"else here. call request_control (immediate when the controls " +
				"are free) and release_control when the task is done.",
		}, false, true

	case "ask_human":
		return s.toolAskHuman(ctx, args)
	case "request_control":
		s.room.JoinAgent(s.agentName)
		// With nobody watching there is nobody to ask, and blocking on an empty
		// room would stall every unattended run.
		if !s.room.HumansPresent() {
			if !s.room.TakeControl(AgentID) {
				return textContent("could not take control"), true, true
			}
			return textContent("control taken (nobody else is here)"), false, true
		}
		timeout := time.Duration(argInt(args, "timeout_ms")) * time.Millisecond
		if timeout <= 0 {
			timeout = 45 * time.Second
		}
		granted, why := s.room.AskForControl(timeout)
		if !granted {
			return map[string]any{
				"granted": false, "reason": why,
				"hint": "a person has to allow it from their toolbar. You can keep " +
					"working on anything that does not need the mouse or keyboard, " +
					"or ask again later.",
			}, true, true
		}
		return map[string]any{"granted": true, "reason": why}, false, true

	case "release_control":
		if !s.room.IsController(AgentID) {
			return textContent("you did not hold control"), false, true
		}
		s.room.ReleaseControl(AgentID)
		return textContent("control released — the controls are free for whoever " +
			"claims them next"), false, true
	}
	return nil, false, false
}

// reportPointer tells the room where the agent's pointer is.
//
// While the agent holds control its pointer IS the X pointer, so nothing extra
// is drawn — the same rule that applies to a human controller. Keeping the room
// informed anyway means the marker is already correct the moment control moves
// somewhere else.
func (s *Server) reportPointer(x, y int) {
	if s.room != nil {
		s.room.UpdatePointer(AgentID, x, y)
	}
}

// mayInject decides whether an input tool is allowed to run.
//
// This is the one place the arbitration is enforced, so it cannot be forgotten
// on a new tool: everything that reaches XTEST goes through here.
func (s *Server) mayInject() error {
	if s.room == nil {
		return nil // no room: nothing to arbitrate with
	}
	s.room.JoinAgent(s.agentName)
	if s.room.IsController(AgentID) {
		return nil
	}
	// An empty room used to be a free pass. It is not one any more: control is
	// held or it is not, and whether anybody happens to be watching does not
	// change that. The agent alone still asks — request_control answers
	// immediately when nothing is driving, so it costs one call and buys a room
	// whose state always says who has the desktop.

	// Never taken implicitly, not even when the controls are free. An input
	// tool that quietly seizes control makes the acquisition invisible: the
	// agent ends up holding the desktop without ever having said it wanted it,
	// and keeps holding it, because nothing gives back what nothing asked for.
	//
	// request_control is the one door, and it is cheap — it grants at once when
	// nothing is driving. What it buys is that every handover is deliberate and
	// visible in the room.
	if id, _ := s.room.Controller(); id == "" {
		return fmt.Errorf(
			"nobody is driving, but control is not taken automatically — " +
				"call request_control (it is granted immediately when the " +
				"controls are free), and release_control when you are done")
	}
	_, who := s.room.Controller()
	if who == "" {
		who = "somebody else"
	}
	return fmt.Errorf(
		"a person is using this desktop and %s holds control — "+
			"call request_control first, or room_state to see who is here", who)
}

// chatTurnNo numbers the exchanges this process starts, so a question and its
// answer arrive at a chat panel carrying the same id.
//
// Process-wide rather than per connection because the id only has to be unique
// among the records a panel is holding at once, and per-connection counters
// would hand two agents the same "1" — which is worse than a number that
// restarts on daemon restart, because two live exchanges would merge into one.
var chatTurnNo atomic.Uint64

// RecordAgentChat publishes one turn of the agent's conversation to the room.
//
// Exported because the console path does not come through here: when the agent
// runtime has a terminal attached it answers the person there, and the daemon's
// only involvement is putting what was said on the record. That caller needs a
// door, and this is it — the same door toolAskHuman uses below, so the two
// routes cannot drift into producing different records for the same event.
//
// Silent when there is no room. A build without one has nowhere to put a
// transcript, and failing the caller over a record nobody can read would turn
// an optional capability into a required one.
func (s *Server) RecordAgentChat(turn stream.ChatTurn) {
	if s.room == nil {
		return
	}
	s.room.AgentChat(turn)
}

// NextChatTurnID mints an id for an exchange, for callers that start one.
func NextChatTurnID() string {
	return fmt.Sprintf("ask-%d", chatTurnNo.Add(1))
}

// approveDanger asks the people in the room to allow one dangerous call, and
// reports whether they did. This is MCP_POLICY=approve's gate — see
// handleToolCall for where it sits among the others.
//
// Built on the same room prompt as ask_human, with the one difference that
// matters: the TEXT IS THE SERVER'S. request_control keeps its prompt out of
// the agent's hands because a prompt that hands over the desktop must not say
// whatever an agent would like; a prompt that unlocks run_command deserves
// exactly the same suspicion, so what the person reads is the tool's name and
// its arguments as the server saw them, composed here.
//
// Every way this can fail is a no with its own sentence: no room to ask, an
// empty room, a question already pending, a timeout, a person declining. None
// of them defaults to yes, because the entire point of the level is that
// nobody being around to say yes MEANS no — an approval gate that waves calls
// through when the room empties is the `full` policy with extra steps.
func (s *Server) approveDanger(ctx context.Context, tool, argsSummary string) (bool, string) {
	if s.room == nil {
		return false, fmt.Sprintf(
			"MCP_POLICY=approve: %s is a dangerous tool and needs a person's approval, "+
				"and this build has no room to ask anybody in", tool)
	}
	s.room.JoinAgent(s.agentName)
	if !s.room.HumansPresent() {
		return false, fmt.Sprintf(
			"MCP_POLICY=approve: %s needs a person in the room to allow it, and nobody "+
				"is here right now. This is not a policy refusal — ask again when "+
				"somebody is present, or continue with work that does not need approval", tool)
	}

	text := fmt.Sprintf("The agent wants to run %s — allow it this once?", tool)
	if argsSummary != "" {
		text = fmt.Sprintf("The agent wants to run %s with %s — allow it this once?",
			tool, argsSummary)
	}

	// Long enough to read a command line, short enough that an agent blocked on
	// an absent person fails while its caller is still listening. Fixed rather
	// than an argument: the agent does not get to decide how long a person has
	// to notice what it is asking for.
	const window = 60 * time.Second

	// Answered on another goroutine so a cancelled call stops waiting at once;
	// the room's own timer still clears the prompt from everyone's screen.
	type result struct {
		answer string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		answer, err := s.room.AskHuman(text, []string{"Allow", "Deny"}, window)
		done <- result{answer, err}
	}()

	select {
	case <-ctx.Done():
		return false, "cancelled while waiting for a person to approve " + tool
	case r := <-done:
		if r.err != nil {
			return false, fmt.Sprintf(
				"MCP_POLICY=approve: %s was not approved (%s). No answer is a no — "+
					"nobody choosing is not somebody agreeing", tool, r.err)
		}
		if strings.EqualFold(strings.TrimSpace(r.answer), "allow") {
			return true, ""
		}
		return false, fmt.Sprintf(
			"MCP_POLICY=approve: a person declined %s. Do not retry the same call — "+
				"ask them what they want instead, with ask_human or in the chat", tool)
	}
}

// toolAskHuman puts a question to the room and waits.
//
// The general form of what request_control does in one special case. That one
// is kept separate on purpose: its text is not the agent's to write, because a
// prompt that hands over the desktop should not be able to say whatever an
// agent would like it to say.
//
// A timeout is reported as a timeout. Returning a default would make "nobody
// was looking" indistinguishable from "somebody chose this", and the whole
// reason to ask is that the answer is not the agent's to assume.
//
// This is the UNATTENDED route, and it is unchanged: the agent has no console
// to be answered at, so it asks the room and waits exactly as before. What is
// new is that the exchange is also put on the record through RecordAgentChat,
// so a chat panel shows the same conversation whichever route it took. The
// record is fire and forget — it cannot fail the call, cannot delay it, and
// cannot turn a question nobody answered into an answer.
func (s *Server) toolAskHuman(ctx context.Context, args map[string]any) (any, bool, bool) {
	question := strings.TrimSpace(argStr(args, "question"))
	if question == "" {
		return textContent("`question` is missing: what do you want to ask?"), true, true
	}
	if s.room == nil {
		return textContent("this build has no room, so there is nobody to ask"), true, true
	}

	var options []string
	if raw, ok := args["options"]; ok && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return textContent("`options` must be an array of strings"), true, true
		}
		for _, item := range list {
			if str, _ := item.(string); strings.TrimSpace(str) != "" {
				options = append(options, strings.TrimSpace(str))
			}
		}
	}

	timeout := time.Duration(argInt(args, "timeout_ms")) * time.Millisecond
	if timeout <= 0 {
		timeout = 120 * time.Second
	}

	// One id for the whole exchange, so the answer lands under the question in a
	// panel instead of beside it.
	turnID := NextChatTurnID()
	s.RecordAgentChat(stream.ChatTurn{
		ID: turnID, Role: "agent", Via: "room",
		Text: question, Options: options,
	})

	// Answered on another goroutine so a cancelled call does not sit here until
	// the timeout. The room's own timer still clears the prompt from everyone's
	// screen, which is what must not be skipped.
	type result struct {
		answer string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		answer, err := s.room.AskHuman(question, options, timeout)
		done <- result{answer, err}

		// Recorded HERE rather than in the select below, and that is not
		// tidiness: the select can leave through ctx.Done while this call is
		// still outstanding, and a transcript that stopped at the question would
		// leave a reader waiting for an answer that had already been given — or
		// already failed to arrive. The record is about what the room did, not
		// about whether the agent was still listening.
		turn := stream.ChatTurn{ID: turnID, Via: "room", Role: "human", Text: answer}
		if err != nil {
			// "system", because nobody said this. A timeout rendered as a human
			// turn is the same lie the tool refuses to tell its caller.
			turn.Role, turn.Text = "system", err.Error()
		}
		s.RecordAgentChat(turn)
	}()

	select {
	case <-ctx.Done():
		return textContent("cancelled while waiting for an answer"), true, true
	case r := <-done:
		if r.err != nil {
			return map[string]any{
				"answered": false,
				"reason":   r.err.Error(),
				"hint": "nobody answering is not the same as agreeing. Decide what to " +
					"do without them, or stop and say you could not reach anyone.",
			}, true, true
		}
		return map[string]any{"answered": true, "answer": r.answer}, false, true
	}
}
