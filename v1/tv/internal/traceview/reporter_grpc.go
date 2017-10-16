package traceview

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"sync"
	"time"

	"github.com/librato/go-traceview/v1/tv/internal/traceview/collector"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	grpcReporterVersion = "golang-v2"
	grpcAddressDefault  = "ec2-54-175-46-34.compute-1.amazonaws.com:5555"
	grpcCertDefault     = `-----BEGIN CERTIFICATE-----
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

	grpcMetricIntervalDefault      = 5   // seconds
	grpcGetSettingsIntervalDefault = 30  // seconds
	grpcRetryDelayInitial          = 500 // miliseconds
	grpcRetryDelayMultiplier       = 1.5 //
	grpcRetryDelayMax              = 60  //seconds
)

type ReconnectAuthority int

const (
	UNSET ReconnectAuthority = iota
	POSTMETRICS
	GETSETTINGS
)

type Connection struct {
	client     collector.TraceCollectorClient
	connection *grpc.ClientConn
}

type grpcReporter struct {
	metricConnection      Connection
	serviceKey            string
	collectorAddress      string
	collectMetricInterval int
	getSettingsInterval   int
	lock                  sync.Mutex
	reconnectAuthority    ReconnectAuthority
}

var grpcMetricMessages = make(chan []byte, 2)

func grpcNewReporter() Reporter {
	serviceKey := os.Getenv("APPOPTICS_SERVICE_KEY")
	if serviceKey == "" {
		OboeLog(WARNING, "No service key found, check environment variable APPOPTICS_SERVICE_KEY.")
		return &nullReporter{}
	}

	collectorAddress := os.Getenv("APPOPTICS_COLLECTOR")
	if collectorAddress == "" {
		collectorAddress = grpcAddressDefault
	}

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

	conn, err := grpcCreateClientConnection(cert, collectorAddress)
	if err != nil {
		OboeLog(ERROR, fmt.Sprintf("Failed to initialize gRPC reporter: %v %v", collectorAddress, err))
		return &nullReporter{}
	}

	reporter := &grpcReporter{
		metricConnection: Connection{
			client:     collector.NewTraceCollectorClient(conn),
			connection: conn,
		},
		serviceKey:       serviceKey,
		collectorAddress: collectorAddress,

		collectMetricInterval: grpcMetricIntervalDefault,
		getSettingsInterval:   grpcGetSettingsIntervalDefault,
	}

	go reporter.metricSender()
	return reporter
}

func grpcCreateClientConnection(cert []byte, addr string) (*grpc.ClientConn, error) {
	certPool := x509.NewCertPool()

	if ok := certPool.AppendCertsFromPEM(cert); !ok {
		return nil, errors.New("Unable to append the certificate to pool.")
	}

	creds := credentials.NewTLS(&tls.Config{
		ServerName:         addr,
		RootCAs:            certPool,
		InsecureSkipVerify: true, // TODO: a workaround, don't turn it on for production.
	})

	return grpc.Dial(addr, grpc.WithTransportCredentials(creds))
}

func (r *grpcReporter) reconnect(authority ReconnectAuthority) {
	if r.reconnectAuthority == UNSET {
		r.lock.Lock()
		if r.reconnectAuthority == UNSET {
			r.reconnectAuthority = authority
		}
		r.lock.Unlock()
	}

	if r.reconnectAuthority == authority {
		r.lock.Lock()
		OboeLog(INFO, "Lost connection -- attempting reconnect...")
		r.metricConnection.client = collector.NewTraceCollectorClient(r.metricConnection.connection)
		r.lock.Unlock()
	} else {
		for r.reconnectAuthority != UNSET {
			time.Sleep(time.Second)
		}
	}
}

func (r *grpcReporter) metricSender() {
	//	collectMetricsTicker := time.NewTimer(r.getMetricsNextInterval())
	collectMetricsTicker := time.NewTimer(0)
	getSettingsTicker := time.NewTimer(0)

	collectMetricsReady := make(chan bool, 1)
	sendMetricsReady := make(chan bool, 1)
	getSettingsReady := make(chan bool, 1)
	collectMetricsReady <- true
	sendMetricsReady <- true
	getSettingsReady <- true

	for {
		select {

		case <-collectMetricsTicker.C:
			collectMetricsTicker.Reset(r.collectMetricsNextInterval())
			select {
			case <-collectMetricsReady:
				go r.collectMetrics(collectMetricsReady, sendMetricsReady)
			default:
			}

		case <-getSettingsTicker.C:
			getSettingsTicker.Reset(time.Duration(r.getSettingsInterval) * time.Second)
			select {
			case <-getSettingsReady:
				go r.getSettings(getSettingsReady)
			default:
			}
		}
	}
}

func (r *grpcReporter) collectMetricsNextInterval() time.Duration {
	interval := r.collectMetricInterval - (time.Now().Second() % r.collectMetricInterval)
	return time.Duration(interval) * time.Second
}

func (r *grpcReporter) collectMetrics(collectReady chan bool, sendReady chan bool) {
	defer func() { collectReady <- true }()

	message := generateMetricsMessage(r.collectMetricInterval)
	printBson(message)

	select {
	case grpcMetricMessages <- message:
	default:
	}

	select {
	case <-sendReady:
		go r.sendMetrics(sendReady)
	default:
	}

}

func (r *grpcReporter) sendMetrics(ready chan bool) {
	defer func() { ready <- true }()

	var messages [][]byte

	done := false
	for !done {
		select {
		case m := <-grpcMetricMessages:
			messages = append(messages, m)
		default:
			done = true
		}
	}

	request := &collector.MessageRequest{
		ApiKey:   r.serviceKey,
		Messages: messages,
		Encoding: collector.EncodingType_BSON,
	}

	delay := grpcRetryDelayInitial

	resultOk := false
	for !resultOk {
		r.lock.Lock()
		response, err := r.metricConnection.client.PostMetrics(context.TODO(), request)
		r.lock.Unlock()

		if err != nil {
			r.reconnect(POSTMETRICS)
		} else {
			r.reconnectAuthority = UNSET

			switch result := response.GetResult(); result {
			case collector.ResultCode_OK:
				OboeLog(DEBUG, "Sent metrics")
				resultOk = true
			case collector.ResultCode_TRY_LATER:
				OboeLog(DEBUG, "Server responded: Try later")
			case collector.ResultCode_LIMIT_EXCEEDED:
				OboeLog(DEBUG, "Server responded: Limit exceeded")
			case collector.ResultCode_INVALID_API_KEY:
				OboeLog(DEBUG, "Server responded: Invalid API key")
			default:
				OboeLog(DEBUG, "Unknown Server response")
			}
		}

		if !resultOk {
			r.setRetryDelay(&delay)
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
	}
}

func (r *grpcReporter) getSettings(ready chan bool) {
	defer func() { ready <- true }()

	var ipAddrs []string
	var uuid string
	ipAddrs = nil
	uuid = ""

	request := &collector.SettingsRequest{
		ApiKey:        r.serviceKey,
		ClientVersion: grpcReporterVersion,
		Identity: &collector.HostID{
			Hostname:    cachedHostname,
			IpAddresses: ipAddrs,
			Uuid:        uuid,
		},
	}

	delay := grpcRetryDelayInitial

	resultOK := false
	for !resultOK {
		r.lock.Lock()
		response, err := r.metricConnection.client.GetSettings(context.TODO(), request)
		r.lock.Unlock()

		if err != nil {
			r.reconnect(GETSETTINGS)
		} else {
			r.reconnectAuthority = UNSET

			switch result := response.GetResult(); result {
			case collector.ResultCode_OK:
				OboeLog(DEBUG, fmt.Sprintf("Got new settings from server %v", r.collectorAddress))
				r.updateSettings(response)
				resultOK = true
			case collector.ResultCode_TRY_LATER:
				OboeLog(DEBUG, "Server responded: Try later")
			case collector.ResultCode_LIMIT_EXCEEDED:
				OboeLog(DEBUG, "Server responded: Limit exceeded")
			case collector.ResultCode_INVALID_API_KEY:
				OboeLog(DEBUG, "Server responded: Invalid API key")
			default:
				OboeLog(DEBUG, "Unknown Server response")
			}
		}

		if !resultOK {
			r.setRetryDelay(&delay)
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
	}

}

func (r *grpcReporter) updateSettings(settings *collector.SettingsResult) {
	for _, s := range settings.Settings {
		//TODO save new settings
		fmt.Println(s)
	}
}

func (r *grpcReporter) setRetryDelay(delay *int) {
	*delay = int(float64(*delay) * grpcRetryDelayMultiplier)
	if *delay > grpcRetryDelayMax*1000 {
		*delay = grpcRetryDelayMax * 1000
	}
}
