// Copyright (C) 2016 AppNeta, Inc. All rights reserved.

package traceview

import (
	"bytes"
	"errors"
	"log"
	"net"
	"os"
	"time"
)

type reporter interface {
	WritePacket([]byte) (int, error)
	IsOpen() bool
}

func newReporter() reporter {
	var conn *net.UDPConn
	if reportingDisabled {
		return &nullReporter{}
	}
	serverAddr, err := net.ResolveUDPAddr("udp4", reporterAddr)
	if err == nil {
		conn, err = net.DialUDP("udp4", nil, serverAddr)
	}
	if err != nil {
		log.Printf("TraceView failed to initialize UDP reporter: %v", err)
		return &nullReporter{}
	}
	return &udpReporter{conn: conn}
}

type nullReporter struct{}

func (r *nullReporter) IsOpen() bool                        { return false }
func (r *nullReporter) WritePacket(buf []byte) (int, error) { return len(buf), nil }

type udpReporter struct {
	conn *net.UDPConn
}

func (r *udpReporter) IsOpen() bool                        { return r.conn != nil }
func (r *udpReporter) WritePacket(buf []byte) (int, error) { return r.conn.Write(buf) }

var reporterAddr = "127.0.0.1:7831"
var globalReporter reporter = &nullReporter{}
var reportingDisabled bool
var usingTestReporter bool
var cachedHostname string

type hostnamer interface {
	Hostname() (name string, err error)
}
type osHostnamer struct{}

func (h osHostnamer) Hostname() (string, error) { return os.Hostname() }

func init() {
	cacheHostname(osHostnamer{})
}
func cacheHostname(hn hostnamer) {
	h, err := hn.Hostname()
	if err != nil {
		log.Printf("Unable to get hostname, TraceView tracing disabled: %v", err)
		globalReporter = &nullReporter{} // disable reporting
		reportingDisabled = true
	}
	cachedHostname = h
}

var cachedPid = os.Getpid()

func reportEvent(r reporter, ctx *context, e *event) error {
	if !r.IsOpen() {
		// Reporter didn't initialize, nothing to do...
		return nil
	}
	if ctx == nil || e == nil {
		return errors.New("Invalid context, event")
	}

	// The context metadata must have the same task_id as the event.
	if bytes.Compare(ctx.metadata.ids.taskID, e.metadata.ids.taskID) != 0 {
		return errors.New("Invalid event, different task_id from context")
	}

	// The context metadata must have a different op_id than the event.
	if bytes.Compare(ctx.metadata.ids.opID, e.metadata.ids.opID) == 0 {
		return errors.New("Invalid event, same as context")
	}

	us := time.Now().UnixNano() / 1000
	e.AddInt64("Timestamp_u", us)

	// Add cached syscalls for Hostname & PID
	e.AddString("Hostname", cachedHostname)
	e.AddInt("PID", cachedPid)

	// Update the context's op_id to that of the event
	ctx.metadata.ids.setOpID(e.metadata.ids.opID)

	// Send BSON:
	bsonBufferFinish(&e.bbuf)
	_, err := r.WritePacket(e.bbuf.buf)
	return err
}

// Determines if request should be traced, based on sample rate settings:
// This is our only dependency on the liboboe C library.
func shouldTraceRequest(layer, xtraceHeader string) (sampled bool, sampleRate, sampleSource int) {
	return oboeSampleRequest(layer, xtraceHeader)
}

// SetTestReporter sets and returns a test reporter that captures raw event bytes
func SetTestReporter() *TestReporter {
	r := &TestReporter{ShouldTrace: true}
	globalReporter = r
	usingTestReporter = true
	return r
}

// TestReporter appends reported events to Bufs if ShouldTrace is true.
type TestReporter struct {
	Bufs        [][]byte
	ShouldTrace bool
}

// WritePacket appends buf to Bufs.
func (r *TestReporter) WritePacket(buf []byte) (int, error) {
	r.Bufs = append(r.Bufs, buf)
	return len(buf), nil
}

// IsOpen is always true.
func (r *TestReporter) IsOpen() bool { return true }
