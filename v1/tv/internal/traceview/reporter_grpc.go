// Copyright (C) 2017 Librato, Inc. All rights reserved.

package traceview

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/appoptics/go-appoptics/v1/tv/internal/traceview/collector"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	grpcReporterVersion = "golang-v2"

	// default collector endpoint address and port,
	// can be overridded via APPOPTICS_COLLECTOR
	grpcAddressDefault = "collector.appoptics.com:443"

	// default certificate used to verify the collector endpoint,
	// can be overridden via APPOPTICS_TRUSTEDPATH
	grpcCertDefault = `-----BEGIN CERTIFICATE-----
MIID8TCCAtmgAwIBAgIJAMoDz7Npas2/MA0GCSqGSIb3DQEBCwUAMIGOMQswCQYD
VQQGEwJVUzETMBEGA1UECAwKQ2FsaWZvcm5pYTEWMBQGA1UEBwwNU2FuIEZyYW5j
aXNjbzEVMBMGA1UECgwMTGlicmF0byBJbmMuMRUwEwYDVQQDDAxBcHBPcHRpY3Mg
Q0ExJDAiBgkqhkiG9w0BCQEWFXN1cHBvcnRAYXBwb3B0aWNzLmNvbTAeFw0xNzA5
MTUyMjAxMzlaFw0yNzA5MTMyMjAxMzlaMIGOMQswCQYDVQQGEwJVUzETMBEGA1UE
CAwKQ2FsaWZvcm5pYTEWMBQGA1UEBwwNU2FuIEZyYW5jaXNjbzEVMBMGA1UECgwM
TGlicmF0byBJbmMuMRUwEwYDVQQDDAxBcHBPcHRpY3MgQ0ExJDAiBgkqhkiG9w0B
CQEWFXN1cHBvcnRAYXBwb3B0aWNzLmNvbTCCASIwDQYJKoZIhvcNAQEBBQADggEP
ADCCAQoCggEBAOxO0wsGba3iI4r3L5BMST0rAO/gGaUhpQre6nRwVTmPCnLw1bmn
GdiFgYv/oRRwU+VieumHSQqoOmyFrg+ajGmvUDp2WqQ0It+XhcbaHFiAp2H7+mLf
cUH6S43/em0WUxZHeRzRupRDyO1bX6Hh2jgxykivlFrn5HCIQD5Hx1/SaZoW9v2n
oATCbgFOiPW6kU/AVs4R0VBujon13HCehVelNKkazrAEBT1i6RvdOB6aQQ32seW+
gLV5yVWSPEJvA9ZJqad/nQ8EQUMSSlVN191WOjp4bGpkJE1svs7NmM+Oja50W56l
qOH5eWermr/8qWjdPlDJ+I0VkgN0UyHVuRECAwEAAaNQME4wHQYDVR0OBBYEFOuL
KDTFhRQXwlBRxhPqhukrNYeRMB8GA1UdIwQYMBaAFOuLKDTFhRQXwlBRxhPqhukr
NYeRMAwGA1UdEwQFMAMBAf8wDQYJKoZIhvcNAQELBQADggEBAJQtH446NZhjusy6
iCyvmnD95ybfNPDpjHmNx5n9Y6w9n+9y1o3732HUJE+WjvbLS3h1o7wujGKMcRJn
7I7eTDd26ZhLvnh5/AitYjdxrtUkQDgyxwLFJKhZu0ik2vXqj0fL961/quJL8Gyp
hNj3Nf7WMohQMSohEmCCX2sHyZGVGYmQHs5omAtkH/NNySqmsWNcpgd3M0aPDRBZ
5VFreOSGKBTJnoLNqods/S9RV0by84hm3j6aQ/tMDIVE9VCJtrE6evzC0MWyVFwR
ftgwcxyEq5SkiR+6BCwdzAMqADV37TzXDHLjwSrMIrgLV5xZM20Kk6chxI5QAr/f
7tsqAxw=
-----END CERTIFICATE-----`

	grpcEventMaxBatchIntervalDefault        = 100 * time.Millisecond
	grpcMetricIntervalDefault               = 30  // default metrics flush interval in seconds
	grpcGetSettingsIntervalDefault          = 30  // default settings retrieval interval in seconds
	grpcSettingsTimeoutCheckIntervalDefault = 10  // default check interval for timed out settings in seconds
	grpcPingIntervalDefault                 = 20  // default interval for keep alive pings in seconds
	grpcRetryDelayInitial                   = 500 // initial connection/send retry delay in milliseconds
	grpcRetryDelayMultiplier                = 1.5 // backoff multiplier for unsuccessful retries
	grpcRetryDelayMax                       = 60  // max connection/send retry delay in seconds
	grpcRedirectMax                         = 20  // max allowed collector redirects
)

// ID of first goroutine that attempts to reconnect a given GRPC client (eventConnection
// or metricConnection). This is to prevent multiple goroutines from messing with the same
// client connection (e.g. sendMetrics() and getSettings() both use the metricConnection)
type reconnectAuthority int

// possible IDs for reconnectAuthority.
// UNSET means no goroutine is attempting a reconnect
const (
	UNSET       reconnectAuthority = iota
	POSTEVENTS                     // eventSender() routine
	POSTSTATUS                     // statusSender() routine
	POSTMETRICS                    // sendMetrics() routine
	GETSETTINGS                    // getSettings() routine
)

type reporterChannel int

const (
	EVENTS reporterChannel = iota
	METRICS
)

// everything needed for a GRPC connection
type grpcConnection struct {
	client             collector.TraceCollectorClient // GRPC client instance
	connection         *grpc.ClientConn               //GRPC connection object
	address            string                         // collector address
	certificate        []byte                         // collector certificate
	serviceKey         string                         // service key
	reconnectAuthority reconnectAuthority             // ID of the goroutine attempting a reconnect on this connection
	pingTicker         *time.Timer                    // timer for keep alive pings in seconds
	pingTickerLock     sync.Mutex                     // lock to ensure sequential access of pingTicker
	lock               sync.RWMutex                   // lock to ensure sequential access (in case of connection loss)
	queueStats         *eventQueueStats               // queue stats (reset on each metrics report cycle)
}

type grpcReporter struct {
	eventConnection              *grpcConnection // used for events only
	metricConnection             *grpcConnection // used for everything else (postMetrics, postStatus, getSettings)
	eventMaxBatchInterval        time.Duration   // max interval between postEvent batches
	collectMetricInterval        int             // metrics flush interval in seconds
	getSettingsInterval          int             // settings retrieval interval in seconds
	settingsTimeoutCheckInterval int             // check interval for timed out settings in seconds
	collectMetricIntervalLock    sync.RWMutex    // lock to ensure sequential access of collectMetricInterval

	eventMessages  chan []byte      // channel for event messages (sent from agent)
	spanMessages   chan SpanMessage // channel for span messages (sent from agent)
	statusMessages chan []byte      // channel for status messages (sent from agent)
	metricMessages chan []byte      // channel for metrics messages (internal to reporter)
	done           chan struct{}    // channel to stop the reporter

	insecureSkipVerify bool // for testing only: if true, skip verifying TLS cert hostname
}

// initializes a new GRPC reporter from scratch (called once on program startup)
//
// returns	GRPC reporter object
func newGRPCReporter() reporter {
	if reportingDisabled {
		return &nullReporter{}
	}

	// service key is required, so bail out if not found
	serviceKey := os.Getenv("APPOPTICS_SERVICE_KEY")
	if serviceKey == "" {
		OboeLog(WARNING, "No service key found, check environment variable APPOPTICS_SERVICE_KEY.")
		return &nullReporter{}
	}

	// see if a hostname alias is configured
	configuredHostname = os.Getenv("APPOPTICS_HOSTNAME_ALIAS")

	// collector address override
	collectorAddress := os.Getenv("APPOPTICS_COLLECTOR")
	if collectorAddress == "" {
		collectorAddress = grpcAddressDefault
	}

	// certificate override
	var cert []byte
	if certPath := os.Getenv("APPOPTICS_TRUSTEDPATH"); certPath != "" {
		var err error
		cert, err = ioutil.ReadFile(certPath)
		if err != nil {
			OboeLog(ERROR, fmt.Sprintf("Error reading cert file %s: %v", certPath, err))
			return &nullReporter{}
		}
	} else {
		cert = []byte(grpcCertDefault)
	}

	var insecureSkipVerify bool
	switch strings.ToLower(os.Getenv("APPOPTICS_INSECURE_SKIP_VERIFY")) {
	case "true", "1", "yes":
		insecureSkipVerify = true
	}

	// create connection object for events client and metrics client
	eventConn, err1 := grpcCreateClientConnection(cert, collectorAddress, insecureSkipVerify)
	metricConn, err2 := grpcCreateClientConnection(cert, collectorAddress, insecureSkipVerify)
	if err1 != nil || err2 != nil {
		OboeLog(ERROR, fmt.Sprintf("Failed to initialize gRPC reporter %v: %v; %v", collectorAddress, err1, err2))
		return &nullReporter{}
	}

	// construct the reporter object which handles two connections
	reporter := &grpcReporter{
		eventConnection: &grpcConnection{
			client:      collector.NewTraceCollectorClient(eventConn),
			connection:  eventConn,
			address:     collectorAddress,
			certificate: cert,
			serviceKey:  serviceKey,
			queueStats:  &eventQueueStats{},
		},
		metricConnection: &grpcConnection{
			client:      collector.NewTraceCollectorClient(metricConn),
			connection:  metricConn,
			address:     collectorAddress,
			certificate: cert,
			serviceKey:  serviceKey,
			queueStats:  &eventQueueStats{},
		},

		eventMaxBatchInterval:        grpcEventMaxBatchIntervalDefault,
		collectMetricInterval:        grpcMetricIntervalDefault,
		getSettingsInterval:          grpcGetSettingsIntervalDefault,
		settingsTimeoutCheckInterval: grpcSettingsTimeoutCheckIntervalDefault,

		eventMessages:  make(chan []byte, 1024),
		spanMessages:   make(chan SpanMessage, 1024),
		statusMessages: make(chan []byte, 1024),
		metricMessages: make(chan []byte, 1024),

		insecureSkipVerify: insecureSkipVerify,
	}

	// send connection init message
	reporter.eventConnection.sendConnectionInit()
	reporter.metricConnection.sendConnectionInit()

	// start up long-running goroutine eventSender() which listens on the events message channel
	// and reports incoming events to the collector using GRPC
	go reporter.eventSender()

	// start up long-running goroutine statusSender() which listens on the status message channel
	// and reports incoming events to the collector using GRPC
	go reporter.statusSender()

	// start up long-running goroutine periodicTasks() which kicks off periodic tasks like
	// collectMetrics() and getSettings()
	if !periodicTasksDisabled {
		go reporter.periodicTasks()
	}

	// start up long-running goroutine spanMessageAggregator() which listens on the span message
	// channel and processes incoming span messages
	go reporter.spanMessageAggregator()

	return reporter
}

// creates a new client connection object which is used by GRPC
// cert		certificate used to verify the collector endpoint
// addr		collector endpoint address and port
//
// returns	client connection object
//			possible error during AppendCertsFromPEM() and Dial()
func grpcCreateClientConnection(cert []byte, addr string, insecureSkipVerify bool) (*grpc.ClientConn, error) {
	certPool := x509.NewCertPool()

	if ok := certPool.AppendCertsFromPEM(cert); !ok {
		return nil, errors.New("Unable to append the certificate to pool.")
	}

	// trim port from server name used for TLS verification
	serverName := addr
	if s := strings.Split(addr, ":"); len(s) > 0 {
		serverName = s[0]
	}

	tlsConfig := &tls.Config{
		ServerName:         serverName,
		RootCAs:            certPool,
		InsecureSkipVerify: insecureSkipVerify,
	}
	// turn off server certificate verification for Go < 1.8
	if !isHigherOrEqualGoVersion("go1.8") {
		tlsConfig.InsecureSkipVerify = true
	}
	creds := credentials.NewTLS(tlsConfig)

	return grpc.Dial(addr, grpc.WithTransportCredentials(creds))
}

// attempts to restore a lost client connection
// c			client connection to perform the reconnect
// authority	ID of the goroutine attempting a reconnect
func (r *grpcReporter) reconnect(c *grpcConnection, authority reconnectAuthority) {
	if c.reconnectAuthority == UNSET {
		// we might be the first goroutine attempting a reconnect, lock and check again
		c.lock.Lock()
		if c.reconnectAuthority == UNSET {
			// yes, we are indeed the first, so take ownership of the reconnecting procedure
			c.reconnectAuthority = authority
		}
		c.lock.Unlock()
	}

	if c.reconnectAuthority == authority {
		// we are authorized to attempt a reconnect
		c.lock.Lock()
		OboeLog(INFO, "Lost connection -- attempting reconnect...")
		c.client = collector.NewTraceCollectorClient(c.connection)
		c.lock.Unlock()

		c.sendConnectionInit()
	} else {
		// we are not authorized to attempt a reconnect, so simply
		// wait until the connection has been restored
		for c.reconnectAuthority != UNSET {
			time.Sleep(time.Second)
		}
	}
}

// redirect to a different collector
// c			client connection to perform the redirect
// authority	ID of the goroutine attempting a redirect
// address		redirect address
func (r *grpcReporter) redirect(c *grpcConnection, authority reconnectAuthority, address string) {
	// create a new connection object for this client
	conn, err := grpcCreateClientConnection(c.certificate, address, r.insecureSkipVerify)
	if err != nil {
		OboeLog(ERROR, fmt.Sprintf("Failed redirect to: %v %v", address, err))
	}

	// set new connection (need to be protected)
	c.lock.Lock()
	c.connection = conn
	c.lock.Unlock()

	// attempt reconnect using the new connection
	r.reconnect(c, authority)
}

// long-running goroutine that kicks off periodic tasks like collectMetrics() and getSettings()
func (r *grpcReporter) periodicTasks() {
	// set up tickers
	collectMetricsTicker := time.NewTimer(r.collectMetricsNextInterval())
	getSettingsTicker := time.NewTimer(0)
	settingsTimeoutCheckTicker := time.NewTimer(time.Duration(r.settingsTimeoutCheckInterval) * time.Second)
	r.eventConnection.pingTicker = time.NewTimer(time.Duration(grpcPingIntervalDefault) * time.Second)
	r.metricConnection.pingTicker = time.NewTimer(time.Duration(grpcPingIntervalDefault) * time.Second)

	// set up 'ready' channels to indicate if a goroutine has terminated
	collectMetricsReady := make(chan bool, 1)
	sendMetricsReady := make(chan bool, 1)
	getSettingsReady := make(chan bool, 1)
	settingsTimeoutCheckReady := make(chan bool, 1)
	collectMetricsReady <- true
	sendMetricsReady <- true
	getSettingsReady <- true
	settingsTimeoutCheckReady <- true

	for {
		select {
		case <-collectMetricsTicker.C: // collect and send metrics
			// set up ticker for next round
			collectMetricsTicker.Reset(r.collectMetricsNextInterval())
			select {
			case <-collectMetricsReady:
				// only kick off a new goroutine if the previous one has terminated
				go r.collectMetrics(collectMetricsReady, sendMetricsReady)
			default:
			}
		case <-getSettingsTicker.C: // get settings from collector
			// set up ticker for next round
			getSettingsTicker.Reset(time.Duration(r.getSettingsInterval) * time.Second)
			select {
			case <-getSettingsReady:
				// only kick off a new goroutine if the previous one has terminated
				go r.getSettings(getSettingsReady)
			default:
			}
		case <-settingsTimeoutCheckTicker.C: // check for timed out settings
			// set up ticker for next round
			settingsTimeoutCheckTicker.Reset(time.Duration(r.settingsTimeoutCheckInterval) * time.Second)
			select {
			case <-settingsTimeoutCheckReady:
				// only kick off a new goroutine if the previous one has terminated
				go r.checkSettingsTimeout(settingsTimeoutCheckReady)
			default:
			}
		case <-r.eventConnection.pingTicker.C: // ping on event connection (keep alive)
			// set up ticker for next round
			r.eventConnection.resetPing()
			go r.eventConnection.ping()
		case <-r.metricConnection.pingTicker.C: // ping on metrics connection (keep alive)
			// set up ticker for next round
			r.metricConnection.resetPing()
			go r.metricConnection.ping()
		}
	}
}

// backoff strategy to slowly increase the retry delay up to a max delay
// oldDelay	the old delay in milliseconds
//
// returns	the new delay in milliseconds
func (r *grpcReporter) setRetryDelay(oldDelay int) int {
	newDelay := int(float64(oldDelay) * grpcRetryDelayMultiplier)
	if newDelay > grpcRetryDelayMax*1000 {
		newDelay = grpcRetryDelayMax * 1000
	}
	return newDelay
}

// ================================ Event Handling ====================================

// prepares the given event and puts it on the channel so it can be consumed by the
// eventSender() goroutine
// ctx		oboe context
// e		event to be put on the channel
//
// returns	error if something goes wrong during preparation or if channel is full
func (r *grpcReporter) reportEvent(ctx *oboeContext, e *event) error {
	if err := prepareEvent(ctx, e); err != nil {
		// don't continue if preparation failed
		return err
	}

	select {
	case r.eventMessages <- (*e).bbuf.GetBuf():
		go atomic.AddInt64(&r.eventConnection.queueStats.totalEvents, int64(1)) // use goroutine so this won't block on the critical path
		return nil
	default:
		go atomic.AddInt64(&r.eventConnection.queueStats.numOverflowed, int64(1)) // use goroutine so this won't block on the critical path
		return errors.New("Event message queue is full")
	}
}

type grpcResult struct {
	ret collector.ResultCode
	err error
}

// long-running goroutine that listens on the events message channel, collects all messages
// on that channel and attempts to send them to the collector using the GRPC method PostEvents()
func (r *grpcReporter) eventSender() {
	batches := make(chan [][]byte)
	results := r.eventBatchSender(batches)
	inProgress := false
	var messages [][]byte

	for {
		select {
		// block until a message arrives, or done
		case e := <-r.eventMessages:
			messages = append(messages, e)
		case result := <-results:
			_ = result // XXX check return code, log errors?

			// if pending entries, make next Log()
			if len(messages) > 0 {
				inProgress = true
				batches <- messages
				messages = [][]byte{}
			} else {
				// remember that we need to make one
				inProgress = false
			}

		case <-time.After(r.eventMaxBatchInterval):
			if !inProgress && len(messages) > 0 {
				// kick off Log(), none was made after last return
				inProgress = true
				batches <- messages
				messages = [][]byte{}
			}
		case <-r.done:
			break
		}
	}
}

func (r *grpcReporter) eventBatchSender(batches chan [][]byte) chan grpcResult {
	results := make(chan grpcResult)
	go func() {
		r.eventRetrySender(batches, results, POSTEVENTS, r.eventConnection)
	}()
	return results
}

func (r *grpcReporter) eventRetrySender(
	batches <-chan [][]byte,
	results chan<- grpcResult,
	authority reconnectAuthority,
	connection *grpcConnection,
) {
	for {
		var messages [][]byte

		select {
		// this will block until a message arrives
		case b := <-batches:
			messages = b
		}

		connection.queueStats.setQueueLargest(len(messages))

		request := &collector.MessageRequest{
			ApiKey:   connection.serviceKey,
			Messages: messages,
			Encoding: collector.EncodingType_BSON,
		}

		// initial retry delay in milliseconds
		delay := grpcRetryDelayInitial
		// counter for redirects so we know when the limit has been reached
		redirects := 0

		// we'll stay in this loop until the call to PostEvents() succeeds
		resultOk := false
		for !resultOk {
			// protect the call to the client object or we could run into problems if
			// another goroutine is messing with it at the same time, e.g. doing a reconnect()
			connection.lock.RLock()
			response, err := connection.client.PostEvents(context.TODO(), request)
			connection.lock.RUnlock()

			// we sent something, or at least tried to, so we're not idle - reset the keepalive timer
			connection.resetPing()

			if err != nil {
				OboeLog(WARNING, fmt.Sprintf("Error calling PostEvents(): %v", err))
				// some server connection error, attempt reconnect
				r.reconnect(connection, authority)
			} else {
				// server responded, check the result code and perform actions accordingly
				switch result := response.GetResult(); result {
				case collector.ResultCode_OK:
					OboeLog(DEBUG, fmt.Sprintf("Sent %d events", len(messages)))
					resultOk = true
					connection.reconnectAuthority = UNSET
					atomic.AddInt64(&connection.queueStats.numSent, int64(len(messages)))
					results <- grpcResult{ret: result}
				case collector.ResultCode_TRY_LATER:
					OboeLog(DEBUG, "Server responded: Try later")
					atomic.AddInt64(&connection.queueStats.numFailed, int64(len(messages)))
				case collector.ResultCode_LIMIT_EXCEEDED:
					OboeLog(DEBUG, "Server responded: Limit exceeded")
					atomic.AddInt64(&connection.queueStats.numFailed, int64(len(messages)))
				case collector.ResultCode_INVALID_API_KEY:
					OboeLog(DEBUG, "Server responded: Invalid API key")
				case collector.ResultCode_REDIRECT:
					if redirects > grpcRedirectMax {
						OboeLog(ERROR, fmt.Sprintf("Max redirects of %v exceeded", grpcRedirectMax))
					} else {
						r.redirect(connection, authority, response.GetArg())
						// a proper redirect shouldn't cause delays
						delay = grpcRetryDelayInitial
						redirects++
					}
				default:
					OboeLog(DEBUG, "Unknown Server response")
				}
			}

			if !resultOk {
				// wait a little before retrying
				time.Sleep(time.Duration(delay) * time.Millisecond)
				delay = r.setRetryDelay(delay)
			}
		}
	}
}

// ================================ Metrics Handling ====================================

// calculates the interval from now until the next time we need to collect metrics
//
// returns	the interval (nanoseconds)
func (r *grpcReporter) collectMetricsNextInterval() time.Duration {
	r.collectMetricIntervalLock.RLock()
	interval := r.collectMetricInterval - (time.Now().Second() % r.collectMetricInterval)
	r.collectMetricIntervalLock.RUnlock()
	return time.Duration(interval) * time.Second
}

// collects the current metrics, puts them on the channel, and kicks off sendMetrics()
// collectReady	a 'ready' channel to indicate if this routine has terminated
// sendReady	another 'ready' channel to indicate if the sendMetrics() goroutine has terminated
func (r *grpcReporter) collectMetrics(collectReady chan bool, sendReady chan bool) {
	// notify caller that this routine has terminated (defered to end of routine)
	defer func() { collectReady <- true }()

	// access collectMetricInterval protected since it can be updated in updateSettings()
	r.collectMetricIntervalLock.RLock()
	interval := r.collectMetricInterval
	r.collectMetricIntervalLock.RUnlock()

	// generate a new metrics message
	message := generateMetricsMessage(interval, r.eventConnection.queueStats)
	//	printBson(message)

	select {
	// put metrics message onto the channel
	case r.metricMessages <- message:
	default:
	}

	select {
	case <-sendReady:
		// only kick off a new goroutine if the previous one has terminated
		go r.sendMetrics(sendReady)
	default:
	}
}

// listens on the metrics message channel, collects all messages on that channel and
// attempts to send them to the collector using the GRPC method PostMetrics()
// ready	a 'ready' channel to indicate if this routine has terminated
func (r *grpcReporter) sendMetrics(ready chan bool) {
	// notify caller that this routine has terminated (defered to end of routine)
	defer func() { ready <- true }()

	var messages [][]byte

	done := false
	for !done {
		select {
		case m := <-r.metricMessages:
			messages = append(messages, m)
		default:
			done = true
		}
	}

	// no messages on the channel so nothing to send, return
	if len(messages) == 0 {
		return
	}

	request := &collector.MessageRequest{
		ApiKey:   r.metricConnection.serviceKey,
		Messages: messages,
		Encoding: collector.EncodingType_BSON,
	}

	// initial retry delay in milliseconds
	delay := grpcRetryDelayInitial
	// counter for redirects so we know when the limit has been reached
	redirects := 0

	// we'll stay in this loop until the call to PostMetrics() succeeds
	resultOk := false
	for !resultOk {
		// protect the call to the client object or we could run into problems if
		// another goroutine is messing with it at the same time, e.g. doing a reconnect()
		r.metricConnection.lock.RLock()
		response, err := r.metricConnection.client.PostMetrics(context.TODO(), request)
		r.metricConnection.lock.RUnlock()

		// we sent something, or at least tried to, so we're not idle - reset the keepalive timer
		r.metricConnection.resetPing()

		if err != nil {
			OboeLog(WARNING, fmt.Sprintf("Error calling PostMetrics(): %v", err))
			// some server connection error, attempt reconnect
			r.reconnect(r.metricConnection, POSTMETRICS)
		} else {
			// server responded, check the result code and perform actions accordingly
			switch result := response.GetResult(); result {
			case collector.ResultCode_OK:
				OboeLog(DEBUG, "Sent metrics")
				resultOk = true
				r.metricConnection.reconnectAuthority = UNSET
			case collector.ResultCode_TRY_LATER:
				OboeLog(DEBUG, "Server responded: Try later")
			case collector.ResultCode_LIMIT_EXCEEDED:
				OboeLog(DEBUG, "Server responded: Limit exceeded")
			case collector.ResultCode_INVALID_API_KEY:
				OboeLog(DEBUG, "Server responded: Invalid API key")
			case collector.ResultCode_REDIRECT:
				if redirects > grpcRedirectMax {
					OboeLog(ERROR, fmt.Sprintf("Max redirects of %v exceeded", grpcRedirectMax))
				} else {
					r.redirect(r.metricConnection, POSTMETRICS, response.GetArg())
					// a proper redirect shouldn't cause delays
					delay = grpcRetryDelayInitial
					redirects++
				}
			default:
				OboeLog(DEBUG, "Unknown Server response")
			}
		}

		if !resultOk {
			// wait a little before retrying
			time.Sleep(time.Duration(delay) * time.Millisecond)
			delay = r.setRetryDelay(delay)
		}
	}
}

// ================================ Settings Handling ====================================

// retrieves the settings from the collector
// ready	a 'ready' channel to indicate if this routine has terminated
func (r *grpcReporter) getSettings(ready chan bool) {
	// notify caller that this routine has terminated (defered to end of routine)
	defer func() { ready <- true }()

	request := &collector.SettingsRequest{
		ApiKey:        r.metricConnection.serviceKey,
		ClientVersion: grpcReporterVersion,
		Identity: &collector.HostID{
			Hostname:    cachedHostname,
			IpAddresses: getIPAddresses(),
		},
	}

	// initial retry delay in milliseconds
	delay := grpcRetryDelayInitial
	// counter for redirects so we know when the limit has been reached
	redirects := 0

	// we'll stay in this loop until the call to GetSettings() succeeds
	resultOK := false
	for !resultOK {
		// protect the call to the client object or we could run into problems if
		// another goroutine is messing with it at the same time, e.g. doing a reconnect()
		r.metricConnection.lock.RLock()
		response, err := r.metricConnection.client.GetSettings(context.TODO(), request)
		r.metricConnection.lock.RUnlock()

		// we sent something, or at least tried to, so we're not idle - reset the keepalive timer
		r.metricConnection.resetPing()

		if err != nil {
			OboeLog(WARNING, fmt.Sprintf("Error calling GetSettings(): %v", err))
			// some server connection error, attempt reconnect
			r.reconnect(r.metricConnection, GETSETTINGS)
		} else {
			// server responded, check the result code and perform actions accordingly
			switch result := response.GetResult(); result {
			case collector.ResultCode_OK:
				OboeLog(DEBUG, fmt.Sprintf("Got new settings from server %v", r.metricConnection.address))
				r.updateSettings(response)
				resultOK = true
				r.metricConnection.reconnectAuthority = UNSET
			case collector.ResultCode_TRY_LATER:
				OboeLog(DEBUG, "Server responded: Try later")
			case collector.ResultCode_LIMIT_EXCEEDED:
				OboeLog(DEBUG, "Server responded: Limit exceeded")
			case collector.ResultCode_INVALID_API_KEY:
				OboeLog(DEBUG, "Server responded: Invalid API key")
			case collector.ResultCode_REDIRECT:
				if redirects > grpcRedirectMax {
					OboeLog(ERROR, fmt.Sprintf("Max redirects of %v exceeded", grpcRedirectMax))
				} else {
					r.redirect(r.metricConnection, GETSETTINGS, response.GetArg())
					// a proper redirect shouldn't cause delays
					delay = grpcRetryDelayInitial
					redirects++
				}
			default:
				OboeLog(DEBUG, "Unknown Server response")
			}
		}

		if !resultOK {
			// wait a little before retrying
			time.Sleep(time.Duration(delay) * time.Millisecond)
			delay = r.setRetryDelay(delay)
		}
	}

}

// updates the existing settings with the newly received
// settings	new settings
func (r *grpcReporter) updateSettings(settings *collector.SettingsResult) {
	for _, s := range settings.GetSettings() {
		updateSetting(int32(s.Type), string(s.Layer), s.Flags, s.Value, s.Ttl, &s.Arguments)

		// update MetricsFlushInterval
		r.collectMetricIntervalLock.Lock()
		if interval, ok := s.Arguments["MetricsFlushInterval"]; ok {
			r.collectMetricInterval = int(binary.LittleEndian.Uint32(interval))
		} else {
			r.collectMetricInterval = grpcMetricIntervalDefault
		}
		r.collectMetricIntervalLock.Unlock()

		// update MaxTransactions
		metricsHTTPMeasurements.transactionNameMaxLock.Lock()
		if max, ok := s.Arguments["MaxTransactions"]; ok {
			metricsHTTPMeasurements.transactionNameMax = int(binary.LittleEndian.Uint32(max))
		} else {
			metricsHTTPMeasurements.transactionNameMax = metricsTransactionsMaxDefault
		}
		metricsHTTPMeasurements.transactionNameMaxLock.Unlock()
	}
}

// delete settings that have timed out according to their TTL
// ready	a 'ready' channel to indicate if this routine has terminated
func (r *grpcReporter) checkSettingsTimeout(ready chan bool) {
	// notify caller that this routine has terminated (defered to end of routine)
	defer func() { ready <- true }()

	// TODO check TTL
}

// ========================= Status Message Handling =============================

// prepares the given event and puts it on the channel so it can be consumed by the
// statusSender() goroutine
// ctx		oboe context
// e		event to be put on the channel
//
// returns	error if something goes wrong during preparation or if channel is full
func (r *grpcReporter) reportStatus(ctx *oboeContext, e *event) error {
	if err := prepareEvent(ctx, e); err != nil {
		// don't continue if preparation failed
		return err
	}

	select {
	case r.statusMessages <- (*e).bbuf.GetBuf():
		return nil
	default:
		return errors.New("Status message queue is full")
	}
}

// long-running goroutine that listens on the status message channel, collects all messages
// on that channel and attempts to send them to the collector using the GRPC method PostStatus()
func (r *grpcReporter) statusSender() {
	for {
		var messages [][]byte

		select {
		// this will block until a message arrives
		case e := <-r.statusMessages:
			messages = append(messages, e)
		}
		// one message detected, see if there are more and get them all!
		done := false
		for !done {
			select {
			case e := <-r.statusMessages:
				messages = append(messages, e)
			default:
				done = true
			}
		}
		// if for some reason there's no message go back to top
		if len(messages) == 0 {
			continue
		}

		//		for _, aaa := range messages {
		//			printBson(aaa)
		//		}

		request := &collector.MessageRequest{
			ApiKey:   r.metricConnection.serviceKey,
			Messages: messages,
			Encoding: collector.EncodingType_BSON,
		}

		// initial retry delay in milliseconds
		delay := grpcRetryDelayInitial
		// counter for redirects so we know when the limit has been reached
		redirects := 0

		// we'll stay in this loop until the call to PostEvents() succeeds
		resultOk := false
		for !resultOk {
			// protect the call to the client object or we could run into problems if
			// another goroutine is messing with it at the same time, e.g. doing a reconnect()
			r.metricConnection.lock.RLock()
			response, err := r.metricConnection.client.PostStatus(context.TODO(), request)
			r.metricConnection.lock.RUnlock()

			// we sent something, or at least tried to, so we're not idle - reset the keepalive timer
			r.metricConnection.resetPing()

			if err != nil {
				OboeLog(WARNING, fmt.Sprintf("Error calling PostStatus(): %v", err))
				// some server connection error, attempt reconnect
				r.reconnect(r.metricConnection, POSTSTATUS)
			} else {
				// server responded, check the result code and perform actions accordingly
				switch result := response.GetResult(); result {
				case collector.ResultCode_OK:
					OboeLog(DEBUG, "Sent status")
					resultOk = true
					r.metricConnection.reconnectAuthority = UNSET
				case collector.ResultCode_TRY_LATER:
					OboeLog(DEBUG, "Server responded: Try later")
				case collector.ResultCode_LIMIT_EXCEEDED:
					OboeLog(DEBUG, "Server responded: Limit exceeded")
				case collector.ResultCode_INVALID_API_KEY:
					OboeLog(DEBUG, "Server responded: Invalid API key")
				case collector.ResultCode_REDIRECT:
					if redirects > grpcRedirectMax {
						OboeLog(ERROR, fmt.Sprintf("Max redirects of %v exceeded", grpcRedirectMax))
					} else {
						r.redirect(r.metricConnection, POSTSTATUS, response.GetArg())
						// a proper redirect shouldn't cause delays
						delay = grpcRetryDelayInitial
						redirects++
					}
				default:
					OboeLog(DEBUG, "Unknown Server response")
				}
			}

			if !resultOk {
				// wait a little before retrying
				time.Sleep(time.Duration(delay) * time.Millisecond)
				delay = r.setRetryDelay(delay)
			}
		}
	}
}

// ========================= Span Message Handling =============================

// puts the given span messages on the channel so it can be consumed by the spanMessageAggregator()
// goroutine
// span		span message to be put on the channel
//
// returns	error if channel is full
func (r *grpcReporter) reportSpan(span *SpanMessage) error {
	select {
	case r.spanMessages <- *span:
		return nil
	default:
		return errors.New("Span message queue is full")
	}
}

// long-running goroutine that listens on the span message channel and processes (aggregates)
// incoming span messages
func (r *grpcReporter) spanMessageAggregator() {
	for {
		select {
		case span := <-r.spanMessages:
			span.process()
		}
	}
}

// ========================= Ping Handling =============================

// reset keep alive timer on a given GRPC connection
func (c *grpcConnection) resetPing() {
	if c.pingTicker == nil {
		return
	}
	c.pingTickerLock.Lock()
	c.pingTicker.Reset(time.Duration(grpcPingIntervalDefault) * time.Second)
	c.pingTickerLock.Unlock()
}

// send a keep alive (ping) request on a given GRPC connection
func (c *grpcConnection) ping() {
	request := &collector.PingRequest{
		ApiKey: c.serviceKey,
	}

	c.lock.RLock()
	c.client.Ping(context.TODO(), request)
	c.lock.RUnlock()
}

// ========================= Connection Init Handling =============================

// send a connection init message
func (c *grpcConnection) sendConnectionInit() {
	bbuf := NewBsonBuffer()
	bsonAppendBool(bbuf, "ConnectionInit", true)
	appendHostId(bbuf)
	bsonBufferFinish(bbuf)

	var messages [][]byte
	messages = append(messages, bbuf.buf)

	request := &collector.MessageRequest{
		ApiKey:   c.serviceKey,
		Messages: messages,
		Encoding: collector.EncodingType_BSON,
	}

	c.lock.RLock()
	c.client.PostStatus(context.TODO(), request)
	c.lock.RUnlock()
}
