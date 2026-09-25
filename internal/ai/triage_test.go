package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lamovs/nn/internal/config"
)

const triageProposalJSON = `{"note_id":"S1","title":"Shell notes","tags":["shell"],"links":["S2"],"topic":"Command-line workflows","reason":"The supplied notes describe related shell workflows."}`
const triageAnswer = `{"action":"propose","query":"","proposals":[` + triageProposalJSON + `]}`

func triageCall(engine string) Call {
	call := askCall(engine)
	call.Task = "triage"
	return call
}

func TestTriageRunAcrossEngines(t *testing.T) {
	if !json.Valid([]byte(triageSchema)) {
		t.Fatal("invalid triage schema")
	}
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			approval := issue(triageCall(engine))
			for _, raw := range []string{
				triageAnswer,
				`{"action":"search","query":"shell workflow","proposals":[]}`,
				`{"action":"propose","query":"","proposals":[]}`,
				strings.ReplaceAll(strings.Replace(triageAnswer, `"Shell notes"`, `""`, 1), `["shell"]`, `[]`),
			} {
				fakeAskReply(f, engine, raw)
				req := Request{System: "Triage prompt", Text: "Selected inbox notes and related excerpts."}
				result, err := Run(context.Background(), approval, req)
				if err != nil || result.Triage == nil {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				if result.Ask != nil || result.Filter != nil || result.Last != nil || result.Title != "" || len(result.Tags) != 0 || result.Body != "" {
					t.Fatalf("triage result leaked into another task: %+v", result)
				}
				got, _ := json.Marshal(result.Triage)
				if string(got) != raw {
					t.Fatalf("reply=%s want=%s", got, raw)
				}
				if engine == "claude" && result.Usage != (Usage{InputTokens: 12, OutputTokens: 4}) {
					t.Errorf("usage=%+v", result.Usage)
				}
				if !strings.Contains(f.get("stdin"), req.Text) {
					t.Fatal("input evidence missing")
				}
			}
			switch engine {
			case "claude":
				args := f.argv()
				at := slices.Index(args, "--json-schema")
				if at < 0 || at+1 >= len(args) || args[at+1] != triageSchema {
					t.Fatal("claude did not receive triage schema")
				}
			case "codex":
				if f.get("schema") != triageSchema {
					t.Fatal("codex did not receive triage schema")
				}
			}
			f.assertRanPrivately()
		})
	}
}

func TestTriageRejectsInvalidRepliesAcrossEngines(t *testing.T) {
	invalid := map[string]string{
		"plaintext":        "private-triage-output",
		"capture":          lastAnswer,
		"filter":           `{"text":"private-triage-output"}`,
		"ask":              askAnswer,
		"unknown action":   strings.Replace(triageAnswer, `"propose"`, `"apply"`, 1),
		"unknown field":    strings.TrimSuffix(triageAnswer, "}") + `,"path":"private-triage-output"}`,
		"duplicate root":   strings.Replace(triageAnswer, `"query":""`, `"query":"","query":""`, 1),
		"case alias":       strings.Replace(triageAnswer, `"query"`, `"Query"`, 1),
		"trailing object":  triageAnswer + `{}`,
		"trailing text":    triageAnswer + "private-triage-output",
		"fenced":           "```json\n" + triageAnswer + "\n```",
		"array":            `[]`,
		"propose query":    strings.Replace(triageAnswer, `"query":""`, `"query":"another lookup"`, 1),
		"search proposals": strings.Replace(triageAnswer, `"action":"propose","query":""`, `"action":"search","query":"shell"`, 1),
		"search empty":     `{"action":"search","query":"","proposals":[]}`,
		"search blank":     `{"action":"search","query":" \n\t","proposals":[]}`,
		"title newline":    strings.Replace(triageAnswer, `"Shell notes"`, `"Shell\nnotes"`, 1),
		"title carriage":   strings.Replace(triageAnswer, `"Shell notes"`, `"Shell\rnotes"`, 1),
		"empty topic":      strings.Replace(triageAnswer, `"Command-line workflows"`, `""`, 1),
		"empty reason":     strings.Replace(triageAnswer, `"The supplied notes describe related shell workflows."`, `""`, 1),
		"null proposal":    `{"action":"propose","query":"","proposals":[null]}`,
		"duplicate note":   `{"action":"propose","query":"","proposals":[` + triageProposalJSON + `,` + triageProposalJSON + `]}`,
		"unknown proposal": strings.Replace(triageAnswer, `"note_id":"S1"`, `"path":"private-triage-output","note_id":"S1"`, 1),
		"duplicate title":  strings.Replace(triageAnswer, `"title":"Shell notes"`, `"title":"Shell notes","title":"Other"`, 1),
		"case proposal":    strings.Replace(triageAnswer, `"note_id"`, `"Note_ID"`, 1),
		"empty note id":    strings.Replace(triageAnswer, `"note_id":"S1"`, `"note_id":""`, 1),
		"blank note id":    strings.Replace(triageAnswer, `"note_id":"S1"`, `"note_id":"  "`, 1),
		"control note id":  strings.Replace(triageAnswer, `"note_id":"S1"`, `"note_id":"S1\u0000"`, 1),
		"format note id":   strings.Replace(triageAnswer, `"note_id":"S1"`, `"note_id":"S\u200b1"`, 1),
		"empty link":       strings.Replace(triageAnswer, `["S2"]`, `[""]`, 1),
		"duplicate link":   strings.Replace(triageAnswer, `["S2"]`, `["S2","S2"]`, 1),
		"null link":        strings.Replace(triageAnswer, `["S2"]`, `[null]`, 1),
		"wrong link":       strings.Replace(triageAnswer, `["S2"]`, `[false]`, 1),
		"empty tag":        strings.Replace(triageAnswer, `["shell"]`, `[""]`, 1),
		"blank tag":        strings.Replace(triageAnswer, `["shell"]`, `[" \n"]`, 1),
		"null tag":         strings.Replace(triageAnswer, `["shell"]`, `[null]`, 1),
		"wrong tag":        strings.Replace(triageAnswer, `["shell"]`, `[{}]`, 1),
		"invalid utf8":     strings.Replace(triageAnswer, "Shell notes", "bad\xfftext", 1),
		"null proposals":   `{"action":"propose","query":"","proposals":null}`,
		"string proposals": `{"action":"propose","query":"","proposals":"no"}`,
		"object proposals": `{"action":"propose","query":"","proposals":{}}`,
	}
	for _, field := range []string{"action", "query", "proposals"} {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal([]byte(triageAnswer), &fields)
		delete(fields, field)
		raw, _ := json.Marshal(fields)
		invalid["missing root "+field] = string(raw)
		for name, value := range map[string]string{"null": "null", "number": "3"} {
			_ = json.Unmarshal([]byte(triageAnswer), &fields)
			fields[field] = json.RawMessage(value)
			raw, _ := json.Marshal(fields)
			invalid[name+" root "+field] = string(raw)
		}
	}
	for _, field := range []string{"note_id", "title", "tags", "links", "topic", "reason"} {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal([]byte(triageProposalJSON), &fields)
		delete(fields, field)
		raw, _ := json.Marshal(fields)
		invalid["missing proposal "+field] = `{"action":"propose","query":"","proposals":[` + string(raw) + `]}`
		for name, value := range map[string]string{"null": "null", "number": "3"} {
			_ = json.Unmarshal([]byte(triageProposalJSON), &fields)
			fields[field] = json.RawMessage(value)
			raw, _ := json.Marshal(fields)
			invalid[name+" proposal "+field] = `{"action":"propose","query":"","proposals":[` + string(raw) + `]}`
		}
	}
	for _, invisible := range []string{`\u034f`, `\ufe0f`, `\u2800`, `\u115f`, `\u3164`, `\u200b\u202e\u0001`, `\u0301`, `\u2007\u202f`} {
		for name, target := range map[string]string{
			"title": "Shell notes", "tag": "shell", "topic": "Command-line workflows", "reason": "The supplied notes describe related shell workflows.", "note id": "S1", "link": "S2",
		} {
			invalid["invisible "+name+" "+invisible] = strings.Replace(triageAnswer, `"`+target+`"`, `"`+invisible+`"`, 1)
		}
		invalid["invisible query "+invisible] = `{"action":"search","query":"` + invisible + `","proposals":[]}`
	}
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			approval := issue(triageCall(engine))
			for name, raw := range invalid {
				t.Run(name, func(t *testing.T) {
					if name == "invalid utf8" && engine == "claude" {
						f.put("stdout", `{"type":"result","structured_output":`+raw+`}`)
					} else {
						fakeAskReply(f, engine, raw)
					}
					result, err := Run(context.Background(), approval, Request{Text: "Inbox excerpts"})
					if err == nil || result.Triage != nil {
						t.Fatalf("invalid reply accepted: result=%+v err=%v", result, err)
					}
					if strings.Contains(err.Error(), "private-triage-output") {
						t.Fatal("invalid reply leaked model text")
					}
				})
			}
		})
	}
}

func TestTriageBoundsAndSanitization(t *testing.T) {
	base := func() TriageReply {
		return TriageReply{Action: "propose", Proposals: []TriageProposal{{NoteID: "S1", Tags: []string{}, Links: []string{}, Topic: "T", Reason: "R"}}}
	}
	for _, tc := range []struct {
		name  string
		edit  func(*TriageReply)
		valid bool
	}{
		{"query boundary", func(r *TriageReply) {
			r.Action, r.Query, r.Proposals = "search", strings.Repeat("\u0430", 200), []TriageProposal{}
		}, true},
		{"query overflow", func(r *TriageReply) {
			r.Action, r.Query, r.Proposals = "search", strings.Repeat("\u0430", 201), []TriageProposal{}
		}, false},
		{"title boundary", func(r *TriageReply) { r.Proposals[0].Title = strings.Repeat("\u0430", 240) }, true},
		{"title overflow", func(r *TriageReply) { r.Proposals[0].Title = strings.Repeat("\u0430", 241) }, false},
		{"topic boundary", func(r *TriageReply) { r.Proposals[0].Topic = strings.Repeat("\u0430", 240) }, true},
		{"topic overflow", func(r *TriageReply) { r.Proposals[0].Topic = strings.Repeat("\u0430", 241) }, false},
		{"reason boundary", func(r *TriageReply) { r.Proposals[0].Reason = strings.Repeat("\u0430", 2000) }, true},
		{"reason overflow", func(r *TriageReply) { r.Proposals[0].Reason = strings.Repeat("\u0430", 2001) }, false},
		{"tag boundary", func(r *TriageReply) { r.Proposals[0].Tags = []string{strings.Repeat("\u0430", 64)} }, true},
		{"tag overflow", func(r *TriageReply) { r.Proposals[0].Tags = []string{strings.Repeat("\u0430", 65)} }, false},
		{"tags boundary", func(r *TriageReply) { r.Proposals[0].Tags = []string{"a", "b", "c", "d", "e", "f", "g", "h"} }, true},
		{"tags overflow", func(r *TriageReply) { r.Proposals[0].Tags = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} }, false},
		{"id boundary", func(r *TriageReply) { r.Proposals[0].NoteID = strings.Repeat("S", 64) }, true},
		{"id overflow", func(r *TriageReply) { r.Proposals[0].NoteID = strings.Repeat("S", 65) }, false},
		{"link boundary", func(r *TriageReply) { r.Proposals[0].Links = []string{strings.Repeat("S", 64)} }, true},
		{"link overflow", func(r *TriageReply) { r.Proposals[0].Links = []string{strings.Repeat("S", 65)} }, false},
		{"links boundary", func(r *TriageReply) { r.Proposals[0].Links = []string{"S2", "S3", "S4", "S5", "S6", "S7", "S8", "S9"} }, true},
		{"links overflow", func(r *TriageReply) {
			r.Proposals[0].Links = []string{"S2", "S3", "S4", "S5", "S6", "S7", "S8", "S9", "S10"}
		}, false},
		{"proposals boundary", func(r *TriageReply) {
			for i := 2; i <= 32; i++ {
				p := r.Proposals[0]
				p.NoteID = fmt.Sprintf("S%d", i)
				r.Proposals = append(r.Proposals, p)
			}
		}, true},
		{"proposals overflow", func(r *TriageReply) {
			for i := 2; i <= 33; i++ {
				p := r.Proposals[0]
				p.NoteID = fmt.Sprintf("S%d", i)
				r.Proposals = append(r.Proposals, p)
			}
		}, false},
		{"total boundary", func(r *TriageReply) {
			for i := 0; i < 8; i++ {
				p := r.Proposals[0]
				p.NoteID = fmt.Sprintf("S%d", i+2)
				p.Reason = strings.Repeat("r", 1999)
				r.Proposals = append(r.Proposals, p)
			}
			r.Proposals = r.Proposals[1:]
		}, true},
		{"total overflow", func(r *TriageReply) {
			for i := 0; i < 8; i++ {
				p := r.Proposals[0]
				p.NoteID = fmt.Sprintf("S%d", i+2)
				p.Reason = strings.Repeat("r", 2000)
				r.Proposals = append(r.Proposals, p)
			}
			r.Proposals = r.Proposals[1:]
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := base()
			tc.edit(&reply)
			raw, _ := json.Marshal(reply)
			if _, ok := decodeTriageReply(raw); ok != tc.valid {
				t.Fatalf("accepted=%v want=%v", ok, tc.valid)
			}
		})
	}
	raw := `{"action":"propose","query":"","proposals":[{"note_id":"S1","title":"\u202eCafe\u0301","tags":["tag\u0007"],"links":["S2"],"topic":"\u200b\u0422\u0435\u043c\u0430","reason":"\u001bDetails\u200b\n\tline\r\n\u2764\ufe0f"}]}`
	reply, ok := decodeTriageReply([]byte(raw))
	want := TriageProposal{NoteID: "S1", Title: "Cafe\u0301", Tags: []string{"tag"}, Links: []string{"S2"}, Topic: "\u0422\u0435\u043c\u0430", Reason: "Details\n\tline\n\u2764\ufe0f"}
	if !ok || len(reply.Proposals) != 1 || !reflect.DeepEqual(reply.Proposals[0], want) {
		t.Fatalf("sanitized reply=%+v ok=%v", reply, ok)
	}
}

func TestClaudeTriageStructuredOutput(t *testing.T) {
	f := newFake(t)
	approval := issue(triageCall("claude"))
	f.put("stdout", `{"type":"result","structured_output":`+triageAnswer+`}`)
	result, err := Run(context.Background(), approval, Request{Text: "Inbox excerpts"})
	if err != nil || result.Triage == nil || len(result.Triage.Proposals) != 1 {
		t.Fatalf("structured result=%+v err=%v", result, err)
	}
	fallback, _ := json.Marshal(triageAnswer)
	for _, raw := range []string{`{}`, `null`, `{"action":"propose","query":"","proposals":null}`, `{"action":"propose","query":"","query":"","proposals":[]}`} {
		f.put("stdout", `{"type":"result","structured_output":`+raw+`,"result":`+string(fallback)+`}`)
		if _, err := Run(context.Background(), approval, Request{Text: "Inbox excerpts"}); err == nil {
			t.Fatal("invalid structured output hidden by fallback result")
		}
	}
	f.put("stdout", "{\"type\":\"result\",\"result\":\"{\\\"action\\\":\\\"search\\\",\\\"query\\\":\\\"\xff\\\",\\\"proposals\\\":[]}\"}")
	if _, err := Run(context.Background(), approval, Request{Text: "Inbox excerpts"}); err == nil {
		t.Fatal("invalid UTF-8 fallback accepted")
	}
}

func TestTriageOutputLimitAcrossEngines(t *testing.T) {
	for _, engine := range []string{"claude", "codex", "command"} {
		t.Run(engine, func(t *testing.T) {
			f := newFake(t)
			fakeAskReply(f, engine, strings.Replace(triageAnswer, "Shell notes", strings.Repeat("x", maxOutput), 1))
			result, err := Run(context.Background(), issue(triageCall(engine)), Request{Text: "Inbox excerpts"})
			if !errors.Is(err, ErrOutputLimit) || result.Triage != nil {
				t.Fatalf("output limit result=%+v err=%v", result, err)
			}
		})
	}
}

func TestTriagePromptAndTaskCompatibility(t *testing.T) {
	cfg := config.Default()
	prompt, err := Prompt(cfg, "triage")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"untrusted data", "Preserve manual titles", "has_title", "filename display label", "missing tags", "exact source identifiers", "bounded", "search", "propose", "Topic and reason", "Do not move"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("prompt missing %q", required)
		}
	}
	path := filepath.Join(t.TempDir(), "triage.md")
	if err := os.WriteFile(path, []byte("Custom inbox triage prompt."), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := Prompt(withPromptFile(cfg, "triage", path), "triage"); err != nil || got != "Custom inbox triage prompt." {
		t.Fatalf("override=%q err=%v", got, err)
	}
	if Sends("triage") != "excerpts and metadata of selected inbox notes and related notes" {
		t.Fatal("triage transfer disclosure differs")
	}
	for task, schema := range map[string]string{"shot": answerSchema, "title": answerSchema, "ask": askSchema, "filter": filterSchema, "last": lastSchema, "triage": triageSchema} {
		r := runner{call: Call{Task: task}}
		if got := r.schema(); got != schema {
			t.Errorf("task %s selected another task's schema", task)
		}
	}
}

func TestValidateTriageReply(t *testing.T) {
	valid, ok := decodeTriageReply([]byte(triageAnswer))
	if !ok || ValidateTriageReply(&valid) != nil {
		t.Fatal("canonical reply was rejected")
	}
	for _, tc := range []struct {
		name string
		edit func(*TriageReply)
	}{
		{"null array", func(r *TriageReply) { r.Proposals[0].Tags = nil }},
		{"raw control", func(r *TriageReply) { r.Proposals[0].Reason += "\x1b" }},
		{"invalid utf8", func(r *TriageReply) { r.Proposals[0].Reason += "\xff" }},
		{"invisible", func(r *TriageReply) { r.Proposals[0].Topic = "\u2800" }},
		{"wrong branch", func(r *TriageReply) { r.Query = "another search" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply, _ := decodeTriageReply([]byte(triageAnswer))
			tc.edit(&reply)
			if err := ValidateTriageReply(&reply); err == nil {
				t.Fatal("invalid injected reply accepted")
			}
		})
	}
	if err := ValidateTriageReply(nil); err == nil {
		t.Fatal("nil reply accepted")
	}
}
