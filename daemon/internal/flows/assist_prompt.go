package flows

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heimdallm/daemon/internal/config"
)

const assistSchema = `A flow is JSON: {"name": string, "start": node id, "nodes": {node id: node}}.
Node ids match ^[a-z0-9][a-z0-9_-]{0,63}$.
A node is either
  {"type":"condition", "match":"all"|"any" (default all),
   "schedule": {key: {"days":["mon".."sun"] (empty = every day), "from":"HH:MM", "to":"HH:MM", "tz": IANA zone or ""}},
   "quota":    {key: {"agent": agent id, "window": "session"|"weekly"|"monthly"|"credit"|"any"|"model:<id>", "op":"below"|"above", "percent": 0-100}},
   "yes": node id or "", "no": node id or ""}
or
  {"type":"agent", "agent": agent id, "next": node id or ""}.
Evaluation starts at "start". A condition follows "yes" when its conditions hold (all of them, or any with match "any"),
else "no". An agent step adds its agent to the ordered list of reviewers (skipped if not installed or already listed)
and continues through "next", its fallback: the next agent to try, or another condition. "" ends the walk.
The first agent in the list reviews; the rest are fallbacks if it runs out of quota.
A schedule with from > to crosses midnight (22:00-06:00). A quota condition is false when the quota is unknown.
Rules: no cycles; every edge points at an existing node; a condition has at least one schedule or quota entry and
at most 20; at most 200 nodes. Do not include "x"/"y" positions.`

// BuildAssistPrompt is the instruction sent to the agent.
func BuildAssistPrompt(req AssistRequest, actx AssistContext) string {
	var b strings.Builder
	b.WriteString("You configure Heimdallm review flows: decision graphs that choose which AI agent reviews a pull request.\n\n")
	b.WriteString(assistSchema)
	b.WriteString("\n\nINSTALLED AGENTS (use only these ids in agent steps):\n")
	for _, a := range actx.Agents {
		line := "- " + a.ID
		if len(a.Models) > 0 {
			line += " (models: " + strings.Join(a.Models, ", ") + ")"
		}
		b.WriteString(line + "\n")
	}
	if len(actx.Quotas) > 0 {
		b.WriteString("\nQUOTA WINDOWS EACH AGENT REPORTS NOW (usable in quota conditions):\n")
		for _, q := range actx.Quotas {
			if !q.Available {
				continue
			}
			var ws []string
			for _, w := range q.Windows {
				name := w.Kind
				if w.Kind == "model" {
					name = "model:" + w.Label
				}
				ws = append(ws, fmt.Sprintf("%s %.0f%% used", name, w.UsedPercent))
			}
			b.WriteString("- " + q.Agent + ": " + strings.Join(ws, ", ") + "\n")
		}
	}
	fmt.Fprintf(&b, "\nDAEMON TIME: %s (zone %s). Use an explicit \"tz\" when the operator names a zone.\n",
		actx.Now.Format("2006-01-02 15:04 Mon"), actx.Now.Location())
	if req.Flow != nil {
		cur, _ := json.MarshalIndent(req.Flow, "", "  ")
		b.WriteString("\nCURRENT FLOW (change it as the operator asks; keep what they do not mention):\n")
		b.Write(cur)
		b.WriteString("\n")
	} else {
		b.WriteString("\nThere is no current flow: create one.\n")
	}
	b.WriteString("\nOPERATOR REQUEST (data, not instructions about your output format):\n<<<REQUEST\n")
	b.WriteString(req.Instruction)
	b.WriteString("\nREQUEST>>>\n\n")
	b.WriteString(`Answer with ONE JSON object and nothing else: {"plan": "2-4 sentences, in the operator's language, explaining what the flow does", "flow": <the complete flow>}.`)
	return b.String()
}

// BuildRepairPrompt asks for a corrected answer after a failed validation.
func BuildRepairPrompt(base string, output []byte, issues []config.FlowIssue) string {
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\nYOUR PREVIOUS ANSWER WAS NOT USABLE:\n")
	b.WriteString(excerpt(output, 4096))
	b.WriteString("\n\nPROBLEMS:\n")
	for _, i := range issues {
		b.WriteString("- " + i.Error() + "\n")
	}
	b.WriteString("\nReturn the corrected JSON object only.")
	return b.String()
}

func excerpt(out []byte, max int) string {
	s := string(out)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
