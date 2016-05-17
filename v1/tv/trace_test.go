// Copyright (C) 2016 AppNeta, Inc. All rights reserved.

package tv_test

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/net/context"

	"github.com/stretchr/testify/assert"
	"github.com/appneta/go-traceview/v1/tv"
	g "github.com/appneta/go-traceview/v1/tv/internal/graphtest"
	"github.com/appneta/go-traceview/v1/tv/internal/traceview"
)

func TestTraceMetadata(t *testing.T) {
	r := traceview.SetTestReporter()

	tr := tv.NewTrace("test")
	md := tr.ExitMetadata()
	tr.End()

	g.AssertGraph(t, r.Bufs, 2, map[g.MatchNode]g.AssertNode{
		// entry event should have no edges
		{"test", "entry"}: {},
		{"test", "exit"}: {g.OutEdges{{"test", "entry"}}, func(n g.Node) {
			// exit event should match ExitMetadata
			assert.Equal(t, md, n.Map["X-Trace"])
		}},
	})
}
func TestNoTraceMetadata(t *testing.T) {
	r := traceview.SetTestReporter()
	r.ShouldTrace = false

	// if trace is not sampled, metadata should be empty
	tr := tv.NewTrace("test")
	md := tr.ExitMetadata()
	tr.End()

	assert.Equal(t, md, "")
	assert.Len(t, r.Bufs, 0)
}

// ensure two different traces have different trace IDs
func TestTraceMetadataDiff(t *testing.T) {
	r := traceview.SetTestReporter()

	t1 := tv.NewTrace("test1")
	md1 := t1.ExitMetadata()
	assert.Len(t, md1, 58)
	t1.End()
	assert.Len(t, r.Bufs, 2)

	t2 := tv.NewTrace("test1")
	md2 := t2.ExitMetadata()
	assert.Len(t, md2, 58)
	t2.End()
	assert.Len(t, r.Bufs, 4)

	assert.NotEqual(t, md1, md2)
	assert.NotEqual(t, md1[2:42], md2[2:42])
}

// example trace
func traceExample(ctx context.Context) {
	// do some work
	f0(ctx)

	// instrument a DB query
	q := []byte("SELECT * FROM tbl")
	l, _ := tv.BeginLayer(ctx, "DBx", "Query", q)
	// db.Query(q)
	time.Sleep(20 * time.Millisecond)
	l.Error("QueryError", "Error running query!")
	l.End()

	// tv.Info and tv.Error report on the root span
	tv.Info(ctx, "HTTP-Status", 500)
	tv.Error(ctx, "TimeoutError", "response timeout")

	// end the trace
	tv.EndTrace(ctx)
}

// example trace
func traceExampleCtx(ctx context.Context) {
	// do some work
	f0(ctx)

	// instrument a DB query
	q := []byte("SELECT * FROM tbl")
	_, ctxQ := tv.BeginLayer(ctx, "DBx", "Query", q)
	// db.Query(q)
	time.Sleep(20 * time.Millisecond)
	tv.Error(ctxQ, "QueryError", "Error running query!")
	tv.End(ctxQ)

	// tv.Info and tv.Error report on the root span
	tv.Info(ctx, "HTTP-Status", 500)
	tv.Error(ctx, "TimeoutError", "response timeout")

	// end the trace
	tv.EndTrace(ctx)
}

// example work function
func f0(ctx context.Context) {
	defer tv.BeginProfile(ctx, "f0").End()

	l, _ := tv.BeginLayer(ctx, "http.Get", "URL", "http://a.b")
	time.Sleep(5 * time.Millisecond)
	// _, _ = http.Get("http://a.b")

	// test reporting a variety of value types
	l.Info("floatV", 3.5, "boolT", true, "boolF", false, "bigV", 5000000000,
		"int64V", int64(5000000001), "int32V", int32(100), "float32V", float32(0.1),
		// test reporting an unsupported type -- currently will be silently ignored
		"weirdType", func() {},
	)
	// test reporting a non-string key: should not work, won't report any events
	l.Info(3, "3")

	time.Sleep(5 * time.Millisecond)
	l.Err(errors.New("test error!"))
	l.End()
}

func TestTraceExample(t *testing.T) {
	r := traceview.SetTestReporter() // enable test reporter
	// create a new trace, and a context to carry it around
	ctx := tv.NewContext(context.Background(), tv.NewTrace("myExample"))
	traceExample(ctx) // generate events
	assertTraceExample(t, r.Bufs)
}

func TestTraceExampleCtx(t *testing.T) {
	r := traceview.SetTestReporter() // enable test reporter
	// create a new trace, and a context to carry it around
	ctx := tv.NewContext(context.Background(), tv.NewTrace("myExample"))
	traceExampleCtx(ctx) // generate events
	assertTraceExample(t, r.Bufs)
}

func assertTraceExample(t *testing.T, bufs [][]byte) {
	g.AssertGraph(t, bufs, 13, map[g.MatchNode]g.AssertNode{
		// entry event should have no edges
		{"myExample", "entry"}: {},
		// first profile event should link to entry event
		{"", "profile_entry"}: {g.OutEdges{{"myExample", "entry"}}, func(n g.Node) {
			assert.Equal(t, n.Map["Language"], "go")
			assert.Equal(t, n.Map["ProfileName"], "f0")
			assert.Equal(t, n.Map["FunctionName"], "github.com/appneta/go-traceview/v1/tv_test.f0")
		}},
		{"", "profile_exit"}: {g.OutEdges{{"", "profile_entry"}}, nil},
		// nested layer in http.Get profile points to trace entry
		// XXX should it point to f0 entry?
		{"http.Get", "entry"}: {g.OutEdges{{"myExample", "entry"}}, func(n g.Node) {
			assert.Equal(t, n.Map["URL"], "http://a.b")
		}},
		// http.Get info points to entry
		{"http.Get", "info"}: {g.OutEdges{{"http.Get", "entry"}}, func(n g.Node) {
			assert.Equal(t, n.Map["floatV"], 3.5)
			assert.Equal(t, n.Map["boolT"], true)
			assert.Equal(t, n.Map["boolF"], false)
			assert.EqualValues(t, n.Map["bigV"], 5000000000)
			assert.EqualValues(t, n.Map["int64V"], 5000000001)
			assert.EqualValues(t, n.Map["int32V"], 100)
			assert.EqualValues(t, n.Map["float32V"], float32(0.1))
		}},
		// http.Get error points to info
		{"http.Get", "error"}: {g.OutEdges{{"http.Get", "info"}}, func(n g.Node) {
			assert.Equal(t, "error", n.Map["ErrorClass"])
			assert.Equal(t, "test error!", n.Map["ErrorMsg"])
		}},
		// end of nested layer should link to last layer event (error)
		{"http.Get", "exit"}: {g.OutEdges{{"http.Get", "error"}}, nil},
		// first query after call to f0 should link to ...?
		{"DBx", "entry"}: {g.OutEdges{{"myExample", "entry"}}, func(n g.Node) {
			assert.EqualValues(t, n.Map["Query"], "SELECT * FROM tbl")
		}},
		// error in nested layer should link to layer entry
		{"DBx", "error"}: {g.OutEdges{{"DBx", "entry"}}, func(n g.Node) {
			assert.Equal(t, "QueryError", n.Map["ErrorClass"])
			assert.Equal(t, "Error running query!", n.Map["ErrorMsg"])
		}},
		// end of nested layer should link to layer entry
		{"DBx", "exit"}: {g.OutEdges{{"DBx", "error"}}, nil},

		{"myExample", "info"}: {g.OutEdges{{"myExample", "entry"}}, func(n g.Node) {
			assert.Equal(t, 500, n.Map["HTTP-Status"])
		}},
		{"myExample", "error"}: {g.OutEdges{{"myExample", "info"}}, func(n g.Node) {
			assert.Equal(t, "TimeoutError", n.Map["ErrorClass"])
			assert.Equal(t, "response timeout", n.Map["ErrorMsg"])
		}},
		{"myExample", "exit"}: {g.OutEdges{
			{"http.Get", "exit"}, {"", "profile_exit"}, {"DBx", "exit"}, {"myExample", "error"},
		}, nil},
	})
}
func TestNoTraceExample(t *testing.T) {
	r := traceview.SetTestReporter()
	ctx := context.Background()
	traceExample(ctx)
	assert.Len(t, r.Bufs, 0)
}

func TestTraceFromMetadata(t *testing.T) {
	r := traceview.SetTestReporter()

	// emulate incoming request with X-Trace header
	incomingID := "1BF4CAA9299299E3D38A58A9821BD34F6268E576CFAB2198D447EA2203"
	tr := tv.NewTraceFromID("test", incomingID, nil)
	tr.End()

	g.AssertGraph(t, r.Bufs, 2, map[g.MatchNode]g.AssertNode{
		// entry event should have no edges
		{"test", "entry"}: {g.OutEdges{}, func(n g.Node) {
			// trace ID should match incoming ID
			assert.Equal(t, incomingID[2:42], n.Map["X-Trace"].(string)[2:42])
		}},
		// exit event links to entry
		{"test", "exit"}: {g.OutEdges{{"test", "entry"}}, func(n g.Node) {
			// trace ID should match incoming ID
			assert.Equal(t, incomingID[2:42], n.Map["X-Trace"].(string)[2:42])
		}},
	})
}
func TestNoTraceFromMetadata(t *testing.T) {
	r := traceview.SetTestReporter()
	r.ShouldTrace = false
	tr := tv.NewTraceFromID("test", "", nil)
	md := tr.ExitMetadata()
	tr.End()

	assert.Equal(t, md, "")
	assert.Len(t, r.Bufs, 0)
}

func TestTraceJoin(t *testing.T) {
	r := traceview.SetTestReporter()

	tr := tv.NewTrace("test")
	l := tr.BeginLayer("L1")
	l.End()
	tr.End()

	g.AssertGraph(t, r.Bufs, 4, map[g.MatchNode]g.AssertNode{
		// entry event should have no edges
		{"test", "entry"}: {},
		{"L1", "entry"}:   {g.OutEdges{{"test", "entry"}}, nil},
		{"L1", "exit"}:    {g.OutEdges{{"L1", "entry"}}, nil},
		{"test", "exit"}:  {g.OutEdges{{"L1", "exit"}, {"test", "entry"}}, nil},
	})
}
