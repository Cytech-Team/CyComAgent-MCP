package main

import (
	"testing"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/audit"
)

func TestGroupTraceEventsAndFilter(t *testing.T) {
	now := time.Now().UTC()
	events := []audit.Event{
		{TraceID: "trace-a", CallID: "call-2", Time: now.Add(time.Second), Tool: "desktop_click", DurationMS: 22, OK: false, Error: "must not leak"},
		{TraceID: "trace-b", CallID: "call-3", Time: now, Tool: "process_list", OK: true},
		{TraceID: "trace-a", CallID: "call-1", Time: now, Tool: "browser_open", DurationMS: 10, OK: true},
		{CallID: "legacy-call", Time: now, Tool: "legacy_tool", OK: true},
	}
	groups := groupTraceEvents(events, "", "")
	if len(groups) != 3 {
		t.Fatalf("got %d groups", len(groups))
	}
	var a *traceGroup
	for i := range groups {
		if groups[i].TraceID == "trace-a" {
			a = &groups[i]
		}
	}
	if a == nil || a.TotalCalls != 2 || a.Succeeded != 1 || a.Failed != 1 {
		t.Fatalf("unexpected trace aggregation: %+v", a)
	}
	if a.Calls[0].Tool != "browser_open" || a.Calls[1].Tool != "desktop_click" {
		t.Fatalf("calls not chronological: %+v", a.Calls)
	}
	filtered := groupTraceEvents(events, "browser_open", "")
	if len(filtered) != 1 || filtered[0].TraceID != "trace-a" || filtered[0].TotalCalls != 1 {
		t.Fatalf("unexpected tool filter: %+v", filtered)
	}
	byID := groupTraceEvents(events, "", "trace-b")
	if len(byID) != 1 || byID[0].TraceID != "trace-b" {
		t.Fatalf("unexpected trace ID filter: %+v", byID)
	}
	legacy := groupTraceEvents(events, "legacy_tool", "")
	if len(legacy) != 1 || legacy[0].TraceID != "legacy:legacy-call" {
		t.Fatalf("legacy event not isolated: %+v", legacy)
	}
}

func TestGroupTraceEventsPreservesSessionLink(t *testing.T) {
	now := time.Now().UTC()
	events := []audit.Event{{TraceID: "trace-session", CallID: "call-session", SessionID: "ses_abc", Time: now, Tool: "process_spawn", OK: true}}
	groups := groupTraceEvents(events, "ses_abc", "")
	if len(groups) != 1 || len(groups[0].Calls) != 1 || groups[0].Calls[0].SessionID != "ses_abc" {
		t.Fatalf("session link missing from trace projection: %+v", groups)
	}
}
