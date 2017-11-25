// Copyright (C) 2016 Librato, Inc. All rights reserved.
// test usage example from doc.go

package tv_test

import (
	"errors"
	"testing"

	"github.com/librato/go-traceview/v1/tv"
	g "github.com/librato/go-traceview/v1/tv/internal/graphtest"
	"github.com/librato/go-traceview/v1/tv/internal/traceview"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/context"
)

func testDocLayerExample() {
	// create trace and bind to new context
	ctx := tv.NewContext(context.Background(), tv.NewTrace("myApp"))
	// create new layer span for this trace
	l, ctxL := tv.BeginLayer(ctx, "myLayer")

	// profile a slow part of a layer
	p := tv.BeginProfile(ctxL, "slowFunc")
	// slowFunc(x, y)
	p.End()

	// Start a new span, given a parent layer
	db1L := l.BeginLayer("myDB1", "Query", "SELECT * FROM tbl1")
	// perform a query
	db1L.End()

	// Start a new span, given a context.Context
	db2L, _ := tv.BeginLayer(ctxL, "myDB2", "Query", "SELECT * FROM tbl2")
	// perform a query
	db2L.End()

	l.End()
	tv.Err(ctx, errors.New("Got bad error!"))
	tv.EndTrace(ctx)
}

func testDocLayerExampleCtx() {
	// create trace and bind to new context
	ctx := tv.NewContext(context.Background(), tv.NewTrace("myApp"))
	// create new layer span for this trace
	_, ctxL := tv.BeginLayer(ctx, "myLayer")

	// profile a nested block or function call
	slowFunc := func() {
		defer tv.BeginProfile(ctxL, "slowFunc").End()
		// ... do something slow
	}
	slowFunc()

	// Start a new span, given a parent layer
	_, ctxQ1 := tv.BeginLayer(ctxL, "myDB1", "Query", "SELECT * FROM tbl1")
	// perform a query
	tv.End(ctxQ1)

	// Start a new span, given a context.Context
	_, ctxQ2 := tv.BeginLayer(ctxL, "myDB2", "Query", "SELECT * FROM tbl2")
	// perform a query
	tv.End(ctxQ2)

	tv.End(ctxL)
	tv.Err(ctx, errors.New("Got bad error!"))
	tv.EndTrace(ctx)
}

func TestDocLayerExample(t *testing.T) {
	r := traceview.SetTestReporter(true)
	testDocLayerExample()
	r.Close(11)
	assertDocLayerExample(t, r.Bufs)
}
func TestDocLayerExampleCtx(t *testing.T) {
	r := traceview.SetTestReporter(true)
	testDocLayerExampleCtx()
	r.Close(11)
	assertDocLayerExample(t, r.Bufs)
}
func assertDocLayerExample(t *testing.T, bufs [][]byte) {
	g.AssertGraph(t, bufs, 11, g.AssertNodeMap{
		{"myApp", "entry"}:   {},
		{"myLayer", "entry"}: {Edges: g.Edges{{"myApp", "entry"}}},
		{"", "profile_entry"}: {Edges: g.Edges{{"myLayer", "entry"}}, Callback: func(n g.Node) {
			assert.Equal(t, n.Map["ProfileName"], "slowFunc")
		}},
		{"", "profile_exit"}: {Edges: g.Edges{{"", "profile_entry"}}},
		{"myDB1", "entry"}: {Edges: g.Edges{{"myLayer", "entry"}}, Callback: func(n g.Node) {
			assert.Equal(t, "SELECT * FROM tbl1", n.Map["Query"])
		}},
		{"myDB1", "exit"}: {Edges: g.Edges{{"myDB1", "entry"}}},
		{"myDB2", "entry"}: {Edges: g.Edges{{"myLayer", "entry"}}, Callback: func(n g.Node) {
			assert.Equal(t, "SELECT * FROM tbl2", n.Map["Query"])
		}},
		{"myDB2", "exit"}:   {Edges: g.Edges{{"myDB2", "entry"}}},
		{"myLayer", "exit"}: {Edges: g.Edges{{"", "profile_exit"}, {"myDB1", "exit"}, {"myDB2", "exit"}, {"myLayer", "entry"}}},
		{"myApp", "error"}: {Edges: g.Edges{{"myApp", "entry"}}, Callback: func(n g.Node) {
			assert.Equal(t, "error", n.Map["ErrorClass"])
			assert.Equal(t, "Got bad error!", n.Map["ErrorMsg"])
		}},
		{"myApp", "exit"}: {Edges: g.Edges{{"myLayer", "exit"}, {"myApp", "error"}}},
	})
}
