// Copyright (C) 2016 AppNeta, Inc. All rights reserved.

package tv_test

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/appneta/go-appneta/v1/tv"
	g "github.com/appneta/go-appneta/v1/tv/internal/graphtest"
	"github.com/appneta/go-appneta/v1/tv/internal/traceview"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/context"
)

func handler404(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }
func handler200(w http.ResponseWriter, r *http.Request) {} // do nothing (default should be 200)

func httpTest(f http.HandlerFunc) *httptest.ResponseRecorder {
	h := http.HandlerFunc(tv.HTTPHandler(f))
	// test a single GET request
	req, _ := http.NewRequest("GET", "http://test.com/hello?testq", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestHTTPHandler404(t *testing.T) {
	r := traceview.SetTestReporter() // set up test reporter
	response := httpTest(handler404)
	assert.Len(t, response.HeaderMap["X-Trace"], 1)

	g.AssertGraph(t, r.Bufs, 2, map[g.MatchNode]g.AssertNode{
		// entry event should have no edges
		{"http.HandlerFunc", "entry"}: {g.OutEdges{}, func(n g.Node) {
			assert.Equal(t, "/hello", n.Map["URL"])
			assert.Equal(t, "test.com", n.Map["HTTP-Host"])
			assert.Equal(t, "GET", n.Map["Method"])
			assert.Equal(t, "testq", n.Map["Query-String"])
		}},
		{"http.HandlerFunc", "exit"}: {g.OutEdges{{"http.HandlerFunc", "entry"}}, func(n g.Node) {
			// assert that response X-Trace header matches trace exit event
			assert.Equal(t, response.HeaderMap.Get("X-Trace"), n.Map["X-Trace"])
			assert.EqualValues(t, response.Code, n.Map["Status"])
			assert.EqualValues(t, 404, n.Map["Status"])
			assert.Equal(t, "tv_test", n.Map["Controller"])
			assert.Equal(t, "handler404", n.Map["Action"])
		}},
	})
}

func TestHTTPHandler200(t *testing.T) {
	r := traceview.SetTestReporter() // set up test reporter
	response := httpTest(handler200)

	g.AssertGraph(t, r.Bufs, 2, map[g.MatchNode]g.AssertNode{
		// entry event should have no edges
		{"http.HandlerFunc", "entry"}: {g.OutEdges{}, func(n g.Node) {
			assert.Equal(t, "/hello", n.Map["URL"])
			assert.Equal(t, "test.com", n.Map["HTTP-Host"])
			assert.Equal(t, "GET", n.Map["Method"])
			assert.Equal(t, "testq", n.Map["Query-String"])
		}},
		{"http.HandlerFunc", "exit"}: {g.OutEdges{{"http.HandlerFunc", "entry"}}, func(n g.Node) {
			// assert that response X-Trace header matches trace exit event
			assert.Len(t, response.HeaderMap["X-Trace"], 1)
			assert.Equal(t, response.HeaderMap["X-Trace"][0], n.Map["X-Trace"])
			assert.EqualValues(t, response.Code, n.Map["Status"])
			assert.EqualValues(t, 200, n.Map["Status"])
			assert.Equal(t, "tv_test", n.Map["Controller"])
			assert.Equal(t, "handler200", n.Map["Action"])
		}},
	})
}

func TestHTTPHandlerNoTrace(t *testing.T) {
	r := traceview.SetTestReporter() // set up test reporter
	r.ShouldTrace = false
	httpTest(handler404)

	// tracing disabled, shouldn't report anything
	assert.Len(t, r.Bufs, 0)
}

// testServer tests creating a layer/trace from inside an HTTP handler (using tv.TraceFromHTTPRequest)
func testServer(t *testing.T, list net.Listener) {
	s := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		// create layer from incoming HTTP Request headers, if trace exists
		tr := tv.TraceFromHTTPRequest("myHandler", req)
		w := tv.NewResponseWriter(writer, tr)
		defer tr.End()

		tr.AddEndArgs("NotReported") // odd-length args, should have no effect

		t.Logf("server: got request %v", req)
		l2 := tr.BeginLayer("DBx", "Query", "SELECT *", "RemoteHost", "db.net")
		// Run a query ...
		l2.End()

		w.WriteHeader(403) // return Forbidden
	})}
	assert.NoError(t, s.Serve(list))
}

// same as testServer, but with external tv.HTTPHandler() handler wrapping
func testDoubleWrappedServer(t *testing.T, list net.Listener) {
	s := &http.Server{Handler: http.HandlerFunc(tv.HTTPHandler(func(writer http.ResponseWriter, req *http.Request) {
		// create layer from incoming HTTP Request headers, if trace exists
		tr, w := tv.TraceFromHTTPRequestResponse("myHandler", writer, req)
		defer tr.End()

		t.Logf("server: got request %v", req)
		l2 := tr.BeginLayer("DBx", "Query", "SELECT *", "RemoteHost", "db.net")
		// Run a query ...
		l2.End()

		// XXX important to test with and without header set in handler
		w.WriteHeader(403) // return Forbidden
	}))}
	assert.NoError(t, s.Serve(list))
}

// create an HTTP client span, make an HTTP request, and propagate the trace context
func testClient(ctx context.Context, url string) (*http.Response, error) {
	l, _ := tv.BeginLayer(ctx, "http.Client", "IsService", true, "RemoteURL", url)

	httpClient := &http.Client{}
	httpReq, _ := http.NewRequest("GET", url, nil)
	httpReq.Header.Set("X-Trace", l.MetadataString())

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		l.Err(err)
	}
	defer resp.Body.Close()

	// TODO also test when no X-Trace header in response, or req fails
	l.End("Edge", resp.Header.Get("X-Trace"))

	return resp, err
}

func TestTraceFromHTTPRequest(t *testing.T) {
	list, err := net.Listen("tcp", ":0") // pick an unallocated port
	assert.NoError(t, err)
	port := list.Addr().(*net.TCPAddr).Port
	go testServer(t, list) // start test server

	r := traceview.SetTestReporter() // set up test reporter
	ctx := tv.NewContext(context.Background(), tv.NewTrace("httpTest"))
	url := fmt.Sprintf("http://127.0.0.1:%d/test?qs=1", port)
	resp, err := testClient(ctx, url)
	tv.EndTrace(ctx)

	assert.NoError(t, err)
	assert.Len(t, resp.Header["X-Trace"], 1)
	assert.Equal(t, 403, resp.StatusCode)

	g.AssertGraph(t, r.Bufs, 8, map[g.MatchNode]g.AssertNode{
		{"httpTest", "entry"}: {},
		{"http.Client", "entry"}: {g.OutEdges{{"httpTest", "entry"}}, func(n g.Node) {
			assert.Equal(t, true, n.Map["IsService"])
			assert.Equal(t, url, n.Map["RemoteURL"])
		}},
		{"http.Client", "exit"}: {g.OutEdges{{"myHandler", "exit"}, {"http.Client", "entry"}}, nil},
		{"myHandler", "entry"}: {g.OutEdges{{"http.Client", "entry"}}, func(n g.Node) {
			assert.Equal(t, "/test", n.Map["URL"])
			assert.Equal(t, fmt.Sprintf("127.0.0.1:%d", port), n.Map["HTTP-Host"])
			assert.Equal(t, "qs=1", n.Map["Query-String"])
			assert.Equal(t, "GET", n.Map["Method"])
		}},
		{"myHandler", "exit"}: {g.OutEdges{{"DBx", "exit"}, {"myHandler", "entry"}}, func(n g.Node) {
			assert.Equal(t, 403, n.Map["Status"])
		}},
		{"DBx", "entry"}: {g.OutEdges{{"myHandler", "entry"}}, func(n g.Node) {
			assert.Equal(t, "SELECT *", n.Map["Query"])
			assert.Equal(t, "db.net", n.Map["RemoteHost"])
		}},
		{"DBx", "exit"}:      {g.OutEdges{{"DBx", "entry"}}, nil},
		{"httpTest", "exit"}: {g.OutEdges{{"http.Client", "exit"}, {"httpTest", "entry"}}, nil},
	})
}

func TestDoubleWrappedHTTPRequest(t *testing.T) {
	list, err := net.Listen("tcp", ":0") // pick an unallocated port
	assert.NoError(t, err)
	port := list.Addr().(*net.TCPAddr).Port
	go testDoubleWrappedServer(t, list) // start test server

	r := traceview.SetTestReporter() // set up test reporter
	ctx := tv.NewContext(context.Background(), tv.NewTrace("httpTest"))
	url := fmt.Sprintf("http://127.0.0.1:%d/test?qs=1", port)
	resp, err := testClient(ctx, url)
	t.Logf("response: %v", resp)
	tv.EndTrace(ctx)

	assert.NoError(t, err)
	assert.Len(t, resp.Header["X-Trace"], 1)
	assert.Equal(t, 403, resp.StatusCode)

	g.AssertGraph(t, r.Bufs, 10, map[g.MatchNode]g.AssertNode{
		{"httpTest", "entry"}: {},
		{"http.Client", "entry"}: {g.OutEdges{{"httpTest", "entry"}}, func(n g.Node) {
			assert.Equal(t, true, n.Map["IsService"])
			assert.Equal(t, url, n.Map["RemoteURL"])
		}},
		{"http.Client", "exit"}: {g.OutEdges{{"http.HandlerFunc", "exit"}, {"http.Client", "entry"}}, nil},
		{"http.HandlerFunc", "entry"}: {g.OutEdges{{"http.Client", "entry"}}, func(n g.Node) {
			assert.Equal(t, "/test", n.Map["URL"])
			assert.Equal(t, fmt.Sprintf("127.0.0.1:%d", port), n.Map["HTTP-Host"])
			assert.Equal(t, "qs=1", n.Map["Query-String"])
			assert.Equal(t, "GET", n.Map["Method"])
		}},
		{"http.HandlerFunc", "exit"}: {g.OutEdges{{"myHandler", "exit"}, {"http.HandlerFunc", "entry"}}, func(n g.Node) {
			assert.Equal(t, 403, n.Map["Status"])
			assert.Equal(t, "tv_test", n.Map["Controller"])
			assert.Equal(t, "testDoubleWrappedServer.func1", n.Map["Action"])
		}},
		{"myHandler", "entry"}: {g.OutEdges{{"http.HandlerFunc", "entry"}}, func(n g.Node) {
			assert.Equal(t, "/test", n.Map["URL"])
			assert.Equal(t, fmt.Sprintf("127.0.0.1:%d", port), n.Map["HTTP-Host"])
			assert.Equal(t, "qs=1", n.Map["Query-String"])
			assert.Equal(t, "GET", n.Map["Method"])
		}},
		{"myHandler", "exit"}: {g.OutEdges{{"DBx", "exit"}, {"myHandler", "entry"}}, func(n g.Node) {
			assert.Equal(t, 403, n.Map["Status"])
		}},
		{"DBx", "entry"}: {g.OutEdges{{"myHandler", "entry"}}, func(n g.Node) {
			assert.Equal(t, "SELECT *", n.Map["Query"])
			assert.Equal(t, "db.net", n.Map["RemoteHost"])
		}},
		{"DBx", "exit"}:      {g.OutEdges{{"DBx", "entry"}}, nil},
		{"httpTest", "exit"}: {g.OutEdges{{"http.Client", "exit"}, {"httpTest", "entry"}}, nil},
	})
}
