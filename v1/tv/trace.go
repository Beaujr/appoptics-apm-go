// Copyright (C) 2016 AppNeta, Inc. All rights reserved.

package tv

import "github.com/appneta/go-appneta/v1/tv/internal/traceview"

// Trace represents a distributed trace for this request that reports
// events to AppNeta TraceView.
type Trace interface {
	// Inherited from the Layer interface
	//  BeginLayer(layerName string, args ...interface{}) Layer
	//  BeginProfile(profileName string, args ...interface{}) Profile
	//	End(args ...interface{})
	//	Info(args ...interface{})
	//  Error(class, msg string)
	//  Err(error)
	//  IsTracing() bool
	Layer

	// End a trace, and include KV pairs returned by func f. Useful alternative to End() when used
	// with defer to delay evaluation of KVs until the end of the trace (since a deferred function's
	// arguments are evaluated when the defer statement is evaluated). Func f will not be called at
	// all if this span is not tracing.
	EndCallback(f func() KVMap)

	// Add additional KV pairs that will be serialized (and dereferenced, for pointer
	// values) at the end of this trace's span.
	AddEndArgs(args ...interface{})

	// ExitMetadata returns a hex string that propagates the end of this span back to a remote
	// client. It is typically used in an response header (e.g. the HTTP Header "X-Trace"). Call
	// this method to set a response header in advance of calling End().
	ExitMetadata() string
}

// KVMap is a map of additional key-value pairs to report along with the event data provided
// to TraceView. Certain key names (such as "Query" or "RemoteHost") are used by AppNeta to
// provide details about program activity and distinguish between different types of layers.
// Please visit http://docs.appneta.com/traceview-instrumentation#special-interpretation for
// details on the key names that TraceView looks for.
type KVMap map[string]interface{}

type tvTrace struct {
	layerSpan
	exitEvent traceview.Event
	endArgs   []interface{}
}

func (t *tvTrace) tvContext() traceview.Context { return t.tvCtx }

// NewTrace creates a new trace for reporting to TraceView and immediately records
// the beginning of the layer layerName. If this trace is sampled, it may report
// event data to AppNeta; otherwise event reporting will be a no-op.
func NewTrace(layerName string) Trace {
	ctx := traceview.NewContext(layerName, "", true, nil)
	return &tvTrace{
		layerSpan: layerSpan{span: span{tvCtx: ctx, labeler: layerLabeler{layerName}}},
	}
}

// NewTraceFromID creates a new trace for reporting to TraceView, provided an
// incoming trace ID (e.g. from a incoming RPC or service call's "X-Trace" header).
// If callback is provided & trace is sampled, cb will be called for entry event KVs
func NewTraceFromID(layerName, mdstr string, cb func() KVMap) Trace {
	ctx := traceview.NewContext(layerName, mdstr, true, func() map[string]interface{} {
		if cb != nil {
			return cb()
		}
		return nil
	})
	return &tvTrace{
		layerSpan: layerSpan{span: span{tvCtx: ctx, labeler: layerLabeler{layerName}}},
	}
}

// EndTrace reports the exit event for the layer name that was used when calling NewTrace().
// No more events should be reported from this trace.
func (t *tvTrace) End(args ...interface{}) {
	if t.ok() {
		t.endArgs = append(t.endArgs, args...)
		t.reportExit()
	}
}

// Add KV pairs as variadic args that will be serialized (and dereferenced, for pointer
// values) at the end of this trace's span.
func (t *tvTrace) AddEndArgs(args ...interface{}) {
	if t.ok() {
		// ensure even number of args added
		if len(args)%2 == 1 {
			args = args[0 : len(args)-1]
		}
		t.endArgs = append(t.endArgs, args...)
	}
}

// EndCallback ends a trace, reporting additional KV pairs returned by calling cb
func (t *tvTrace) EndCallback(cb func() KVMap) {
	if t.ok() {
		if cb != nil {
			for k, v := range cb() {
				t.endArgs = append(t.endArgs, k, v)
			}
		}
		t.reportExit()
	}
}

func (t *tvTrace) reportExit() {
	if t.ok() {
		for _, edge := range t.childEdges { // add Edge KV for each joined child
			t.endArgs = append(t.endArgs, "Edge", edge)
		}
		if t.exitEvent != nil { // use exit event, if one was provided
			_ = t.exitEvent.ReportContext(t.tvCtx, true, t.endArgs...)
		} else {
			_ = t.tvCtx.ReportEvent(traceview.LabelExit, t.layerName(), t.endArgs...)
		}
		t.childEdges = nil // clear child edge list
		t.endArgs = nil
		t.ended = true
	}
}

func (t *tvTrace) IsTracing() bool { return t.tvCtx.IsTracing() }

// ExitMetadata reports the X-Trace metadata string that will be used by the exit event.
// This is useful for setting response headers before reporting the end of the span.
func (t *tvTrace) ExitMetadata() string {
	if t.IsTracing() {
		if t.exitEvent == nil {
			t.exitEvent = t.tvCtx.NewEvent(traceview.LabelExit, t.layerName(), false)
		}
		if t.exitEvent != nil {
			return t.exitEvent.MetadataString()
		}
	}
	return ""
}
