// Copyright (C) 2016 AppNeta, Inc. All rights reserved.

package traceview

import "bytes"

type event struct {
	metadata oboeMetadata
	bbuf     bsonBuffer
}

// Label is a required event attribute.
type Label string

// Labels used for reporting events for Layer and Profile spans.
const (
	LabelEntry        = "entry"
	LabelExit         = "exit"
	LabelInfo         = "info"
	LabelError        = "error"
	LabelProfileEntry = "profile_entry"
	LabelProfileExit  = "profile_exit"
)

const (
	eventHeader = "1"
)

func oboeEventInit(evt *event, md *oboeMetadata) int {
	if evt == nil || md == nil {
		return -1
	}

	// Metadata initialization
	result := oboeMetadataInit(&evt.metadata)
	if result < 0 {
		return result
	}

	evt.metadata.taskLen = md.taskLen
	evt.metadata.opLen = md.opLen

	copy(evt.metadata.ids.taskID, md.ids.taskID)
	oboeRandomOpID(&evt.metadata)

	// Buffer initialization

	bsonBufferInit(&evt.bbuf)

	// Copy header to buffer
	// TODO errors?
	bsonAppendString(&evt.bbuf, "_V", eventHeader)

	// Pack metadata
	mdStr, err := oboeMetadataToString(&evt.metadata)
	if err == nil {
		bsonAppendString(&evt.bbuf, "X-Trace", mdStr)
	}

	return 0
}

func newEvent(md *oboeMetadata, label Label, layer string) *event {
	e := &event{}
	oboeEventInit(e, md)
	e.addLabelLayer(label, layer)
	return e
}

func (e *event) addLabelLayer(label Label, layer string) {
	e.AddString("Label", string(label))
	if layer != "" {
		e.AddString("Layer", layer)
	}
}

// Adds string key/value to event. BSON strings are assumed to be Unicode.
func (e *event) AddString(key, value string) { bsonAppendString(&e.bbuf, key, value) }

// Adds a binary buffer as a key/value to this event. This uses a binary-safe BSON buffer type.
func (e *event) AddBinary(key string, value []byte) { bsonAppendBinary(&e.bbuf, key, value) }

// Adds int key/value to event
func (e *event) AddInt(key string, value int) { bsonAppendInt(&e.bbuf, key, value) }

// Adds int64 key/value to event
func (e *event) AddInt64(key string, value int64) { bsonAppendInt64(&e.bbuf, key, value) }

// Adds int32 key/value to event
func (e *event) AddInt32(key string, value int32) { bsonAppendInt32(&e.bbuf, key, value) }

// Adds float32 key/value to event
func (e *event) AddFloat32(key string, value float32) { bsonAppendFloat64(&e.bbuf, key, float64(value)) }

// Adds float64 key/value to event
func (e *event) AddFloat64(key string, value float64) { bsonAppendFloat64(&e.bbuf, key, value) }

// Adds float key/value to event
func (e *event) AddBool(key string, value bool) { bsonAppendBool(&e.bbuf, key, value) }

// Adds edge (reference to previous event) to event
func (e *event) AddEdge(ctx *context) { bsonAppendString(&e.bbuf, "Edge", ctx.metadata.opString()) }

func (e *event) AddEdgeFromMetadataString(mdstr string) {
	var md oboeMetadata
	oboeMetadataInit(&md)
	oboeMetadataFromString(&md, mdstr)
	// only add Edge if metadata references same trace as ours
	if bytes.Compare(e.metadata.ids.taskID, md.ids.taskID) == 0 {
		bsonAppendString(&e.bbuf, "Edge", md.opString())
	}
}

// Reports event using specified Reporter
func (e *event) ReportUsing(c *context, r reporter) error { return reportEvent(r, c, e) }

// Reports event using default (UDP) Reporter
func (e *event) Report(c *context) error { return e.ReportUsing(c, globalReporter) }

// Report event using SampledContext interface
func (e *event) ReportContext(c SampledContext, addCtxEdge bool, args ...interface{}) {
	if ctx, ok := c.(*context); ok {
		ctx.report(e, addCtxEdge, args...)
	}
}

// Returns Metadata string (X-Trace header)
func (e *event) MetadataString() string { return e.metadata.String() }
