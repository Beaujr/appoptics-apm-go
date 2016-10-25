// Copyright (C) 2016 Librato, Inc. All rights reserved.
// TraceView HTTP instrumentation for Go

package tv

import "golang.org/x/net/context"

// BeginQueryLayer returns a Layer that reports metadata used by TraceView to filter
// query latency heatmaps and charts by layer name, query statement, DB host and table.
// Parameter "flavor" specifies the flavor of the query statement, such as "mysql", "postgresql", or "mongodb".
// Call or defer the returned Layer's End() to time the query's client-side latency.
func BeginQueryLayer(ctx context.Context, layerName, query, flavor, remoteHost string) Layer {
	l, _ := BeginLayer(ctx, layerName, "Query", query, "Flavor", flavor, "RemoteHost", remoteHost)
	return l
}

// BeginCacheLayer returns a Layer that reports metadata used by TraceView to filter cache/KV server
// request latency heatmaps and charts by layer name, cache operation and hostname.
// Required parameter "op" is meant to report a Redis or Memcached command e.g. "HGET" or "set".
// Filterable hit/miss ratios charts will be available if "hit" is used.
// Optional parameter "key" will display in the trace's details, but will not be indexed.
// Call or defer the returned Layer's End() to time the request's client-side latency.
func BeginCacheLayer(ctx context.Context, layerName, op, key, remoteHost string, hit bool) Layer {
	l, _ := BeginLayer(ctx, layerName, "KVOp", op, "KVKey", key, "KVHit", hit, "RemoteHost", remoteHost)
	return l
}

// BeginRemoteURLLayer returns a Layer that reports metadata used by TraceView to filter RPC call
// latency heatmaps and charts by layer name and URL endpoint. For requests using the "net/http"
// package, BeginHTTPClientLayer also reports this metadata, while also propagating trace context
// metadata headers via http.Request and http.Response.
// Call or defer the returned Layer's End() to time the call's client-side latency.
func BeginRemoteURLLayer(ctx context.Context, layerName, remoteURL string) Layer {
	l, _ := BeginLayer(ctx, layerName, "IsService", true, "RemoteURL", remoteURL)
	return l
}

// BeginRPCLayer returns a Layer that reports metadata used by TraceView to filter RPC call
// latency heatmaps and charts by layer name, protocol, controller, and remote host.
// Call or defer the returned Layer's End() to time the call's client-side latency.
func BeginRPCLayer(ctx context.Context, layerName, protocol, controller, remoteHost string) Layer {
	l, _ := BeginLayer(ctx, layerName, "IsService", true,
		"RemoteProtocol", protocol, "RemoteHost", remoteHost, "RemoteController", controller)
	return l
}
