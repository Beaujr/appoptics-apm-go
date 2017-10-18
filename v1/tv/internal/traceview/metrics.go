package traceview

import (
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Linux distributions
const (
	REDHAT    = "/etc/redhat-release"
	AMAZON    = "/etc/release-cpe"
	UBUNTU    = "/etc/lsb-release"
	DEBIAN    = "/etc/debian_version"
	SUSE      = "/etc/SuSE-release"
	SLACKWARE = "/etc/slackware-version"
	GENTOO    = "/etc/gentoo-release"
	OTHER     = "/etc/issue"
)

const (
	metricsHTTPTransactionsMax = 200
	metricsTagNameLenghtMax    = 64
	metricsTagValueLenghtMax   = 255
)

type HttpSpanMessage struct {
	transaction string
	url         string
	duration    int64
	status      int
	method      string
	hasError    bool
}

type Measurement struct {
	name        string
	tags        map[string]string
	count       int
	sum         float64
	reportValue bool
}

type Measurements struct {
	measurements            map[string]*Measurement
	transactionNameOverflow bool
	lock                    sync.Mutex
}

var cachedDistro string
var cachedMACAddresses = "uninitialized"
var cachedAWSInstanceId = "uninitialized"
var cachedAWSInstanceZone = "uninitialized"
var cachedContainerID = "uninitialized"

var metricsURLRegex = regexp.MustCompile(`^(https?://)?[^/]+(/([^/\?]+))?(/([^/\?]+))?`)
var metricsHTTPTransactions = make(map[string]bool)
var metricsHTTPMeasurements = &Measurements{measurements: make(map[string]*Measurement)}

func generateMetricsMessage(metricsFlushInterval int) []byte {
	bbuf := NewBsonBuffer()

	bsonAppendString(bbuf, "Hostname", cachedHostname)
	bsonAppendString(bbuf, "Distro", getDistro())
	bsonAppendInt(bbuf, "PID", cachedPid)
	appendUname(bbuf)
	appendIPAddresses(bbuf)
	appendMACAddresses(bbuf)

	if getAWSInstanceID() != "" {
		bsonAppendString(bbuf, "EC2InstanceID", getAWSInstanceID())
	}
	if getAWSInstanceZone() != "" {
		bsonAppendString(bbuf, "EC2AvailabilityZone", getAWSInstanceZone())
	}
	if getContainerId() != "" {
		bsonAppendString(bbuf, "DockerContainerID", getContainerId())
	}

	bsonAppendInt64(bbuf, "Timestamp_u", int64(time.Now().UnixNano()/1000))
	bsonAppendInt(bbuf, "MetricsFlushInterval", metricsFlushInterval)

	// measurements
	// ==========================================
	start := bsonAppendStartArray(bbuf, "measurements")
	index := 0

	// TODO add request counters

	// TODO add event queue stats

	// system load of last minute
	if s := getStrByKeyword("/proc/loadavg", ""); s != "" {
		load, err := strconv.ParseFloat(strings.Fields(s)[0], 64)
		if err == nil {
			addMetricsValue(bbuf, &index, "Load1", load)
		}
	}

	// system total memory
	if s := getStrByKeyword("/proc/meminfo", "MemTotal"); s != "" {
		memTotal := strings.Fields(s) // MemTotal: 7657668 kB
		if len(memTotal) == 3 {
			if total, err := strconv.Atoi(memTotal[1]); err == nil {
				addMetricsValue(bbuf, &index, "TotalRAM", total*1024)
			}
		}
	}

	// free memory
	if s := getStrByKeyword("/proc/meminfo", "MemFree"); s != "" {
		memFree := strings.Fields(s) // MemFree: 161396 kB
		if len(memFree) == 3 {
			if free, err := strconv.Atoi(memFree[1]); err == nil {
				addMetricsValue(bbuf, &index, "FreeRAM", free*1024) // bytes
			}
		}
	}

	// process memory
	if s := getStrByKeyword("/proc/self/statm", ""); s != "" {
		processRAM := strings.Fields(s)
		if len(processRAM) != 0 {
			for _, ps := range processRAM {
				if p, err := strconv.Atoi(ps); err == nil {
					addMetricsValue(bbuf, &index, "ProcessRAM", p*os.Getpagesize())
					break
				}
			}
		}
	}

	// service / transaction measurements
	metricsHTTPMeasurements.lock.Lock()
	transactionNameOverflow := metricsHTTPMeasurements.transactionNameOverflow

	for _, m := range metricsHTTPMeasurements.measurements {
		addMeasurementToBSON(bbuf, &index, m)
	}
	metricsHTTPMeasurements.measurements = make(map[string]*Measurement)
	metricsHTTPMeasurements.lock.Unlock()

	bsonAppendFinishObject(bbuf, start)
	// ==========================================

	if transactionNameOverflow {
		bsonAppendBool(bbuf, "TransactionNameOverflow", true)
	}

	bsonBufferFinish(bbuf)
	return bbuf.buf
}

func getDistro() string {
	if cachedDistro != "" {
		return cachedDistro
	}

	var ds []string // distro slice

	// Note: Order of checking is important because some distros share same file names
	// but with different function.
	// Keep this order: redhat based -> ubuntu -> debian

	// redhat
	if cachedDistro = getStrByKeyword(REDHAT, ""); cachedDistro != "" {
		return cachedDistro
	}
	// amazon linux
	cachedDistro = getStrByKeyword(AMAZON, "")
	ds = strings.Split(cachedDistro, ":")
	cachedDistro = ds[len(ds)-1]
	if cachedDistro != "" {
		cachedDistro = "Amzn Linux " + cachedDistro
		return cachedDistro
	}
	// ubuntu
	cachedDistro = getStrByKeyword(UBUNTU, "DISTRIB_DESCRIPTION")
	if cachedDistro != "" {
		ds = strings.Split(cachedDistro, "=")
		cachedDistro = ds[len(ds)-1]
		if cachedDistro != "" {
			cachedDistro = strings.Trim(cachedDistro, "\"")
		} else {
			cachedDistro = "Ubuntu unknown"
		}
		return cachedDistro
	}

	pathes := []string{DEBIAN, SUSE, SLACKWARE, GENTOO, OTHER}
	if path, line := getStrByKeywordFiles(pathes, ""); path != "" && line != "" {
		cachedDistro = line
		if path == "Debian" {
			cachedDistro = "Debian " + cachedDistro
		}
	} else {
		cachedDistro = "Unknown"
	}
	return cachedDistro
}

func appendUname(bbuf *bsonBuffer) {
	if runtime.GOOS == "linux" {
		var uname syscall.Utsname
		if err := syscall.Uname(&uname); err == nil {
			sysname := Byte2String(uname.Sysname[:])
			version := Byte2String(uname.Version[:])
			bsonAppendString(bbuf, "UnameSysName", strings.TrimRight(sysname, "\x00"))
			bsonAppendString(bbuf, "UnameVersion", strings.TrimRight(version, "\x00"))
		}
	}
}

func appendIPAddresses(bbuf *bsonBuffer) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return
	}

	start := bsonAppendStartArray(bbuf, "IPAddresses")
	for _, addr := range addrs {
		i := 0
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			bsonAppendString(bbuf, strconv.Itoa(i), ipnet.IP.String())
			i++
		}
	}
	bsonAppendFinishObject(bbuf, start)
}

func appendMACAddresses(bbuf *bsonBuffer) {
	macs := strings.Split(getMACAddressList(), ",")

	start := bsonAppendStartArray(bbuf, "MACAddresses")
	for _, mac := range macs {
		i := 0
		bsonAppendString(bbuf, strconv.Itoa(i), mac)
		i++
	}
	bsonAppendFinishObject(bbuf, start)
}

func getMACAddressList() string {
	if cachedMACAddresses != "uninitialized" {
		return cachedMACAddresses
	}

	cachedMACAddresses = ""
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			if mac := iface.HardwareAddr.String(); mac != "" {
				cachedMACAddresses += iface.HardwareAddr.String() + ","
			}
		}
	}
	cachedMACAddresses = strings.TrimSuffix(cachedMACAddresses, ",") // trim the final one

	return cachedMACAddresses
}

func getAWSInstanceID() string {
	if cachedAWSInstanceId != "uninitialized" {
		return cachedAWSInstanceId
	}

	cachedAWSInstanceId = ""
	if isEC2Instance() {
		url := "http://169.254.169.254/latest/meta-data/instance-id"
		client := http.Client{Timeout: time.Second}
		resp, err := client.Get(url)
		if err == nil {
			defer resp.Body.Close()
			body, err := ioutil.ReadAll(resp.Body)
			if err == nil {
				cachedAWSInstanceId = string(body)
			}
		}
	}

	return cachedAWSInstanceId
}

func getAWSInstanceZone() string {
	if cachedAWSInstanceZone != "uninitialized" {
		return cachedAWSInstanceZone
	}

	cachedAWSInstanceZone = ""
	if isEC2Instance() {
		url := "http://169.254.169.254/latest/meta-data/placement/availability-zone"
		client := http.Client{Timeout: time.Second}
		resp, err := client.Get(url)
		if err == nil {
			defer resp.Body.Close()
			body, err := ioutil.ReadAll(resp.Body)
			if err == nil {
				cachedAWSInstanceZone = string(body)
			}
		}
	}

	return cachedAWSInstanceZone
}

func isEC2Instance() bool {
	match := getLineByKeyword("/sys/hypervisor/uuid", "ec2")
	return match != "" && strings.HasPrefix(match, "ec2")
}

func getContainerId() string {
	if cachedContainerID != "uninitialized" {
		return cachedContainerID
	}

	cachedContainerID = ""
	line := getLineByKeyword("/proc/self/cgroup", "docker")
	if line != "" {
		tokens := strings.Split(line, "/")
		// A typical line returned by cat /proc/self/cgroup (that's why we expect 3 tokens):
		// 9:devices:/docker/40188af19439697187e3f60b933e7e37c5c41035f4c0b266a51c86c5a0074b25
		if len(tokens) == 3 {
			cachedContainerID = tokens[2]
		}
	}

	return cachedContainerID
}

func addMetricsValue(bbuf *bsonBuffer, index *int, name string, value interface{}) {
	start := bsonAppendStartObject(bbuf, strconv.Itoa(*index))

	bsonAppendString(bbuf, "name", name)
	switch value.(type) {
	case int:
		bsonAppendInt(bbuf, "value", value.(int))
	case float32, float64:
		bsonAppendFloat64(bbuf, "value", value.(float64))
	default:
		bsonAppendString(bbuf, "value", "unknown")
	}

	bsonAppendFinishObject(bbuf, start)
	*index += 1
}

func getTransactionFromURL(url string) string {
	matches := metricsURLRegex.FindStringSubmatch(url)
	var ret string
	if matches[3] != "" {
		ret += "/" + matches[3]
		if matches[5] != "" {
			ret += "/" + matches[5]
		}
	} else {
		ret = "/"
	}

	return ret
}

func isWithinLimit(m *map[string]bool, element string, max int) bool {
	if _, ok := (*m)[element]; !ok {
		// only record if we haven't reached the limits yet
		if len(*m) < max {
			(*m)[element] = true
			return true
		}
		return false
	} else {
		return true
	}
}

func processHttpMeasurements(transactionName string, httpSpan *HttpSpanMessage) {
	name := "TransactionResponseTime"
	duration := float64((*httpSpan).duration)

	metricsHTTPMeasurements.lock.Lock()

	// primary ID: TransactionName
	primaryTags := make(map[string]string)
	primaryTags["TransactionName"] = transactionName
	recordMeasurement(metricsHTTPMeasurements, name, &primaryTags, duration, 1, true)

	// secondary keys: HttpMethod, HttpStatus, Errors
	withMethodTags := copyMap(&primaryTags)
	withMethodTags["HttpMethod"] = httpSpan.method
	recordMeasurement(metricsHTTPMeasurements, name, &withMethodTags, duration, 1, true)

	withStatusTags := copyMap(&primaryTags)
	withStatusTags["HttpStatus"] = strconv.Itoa(httpSpan.status)
	recordMeasurement(metricsHTTPMeasurements, name, &withStatusTags, duration, 1, true)

	if httpSpan.hasError {
		withErrorTags := copyMap(&primaryTags)
		withErrorTags["Errors"] = "true"
		recordMeasurement(metricsHTTPMeasurements, name, &withErrorTags, duration, 1, true)
	}

	metricsHTTPMeasurements.lock.Unlock()
}

func recordMeasurement(m *Measurements, name string, tags *map[string]string,
	value float64, count int, reportValue bool) {

	measurements := m.measurements
	id := name + "&" + strconv.FormatBool(reportValue) + "&"
	for k, v := range *tags {
		id += k + ":" + v + "&"
	}

	var measurement *Measurement
	var ok bool

	// create a new measurement if it doesn't exist
	if measurement, ok = measurements[id]; !ok {
		measurement = &Measurement{
			name:        name,
			tags:        *tags,
			reportValue: reportValue,
		}
		measurements[id] = measurement
	}

	measurement.count += count
	measurement.sum += value
}

func setTransactionNameOverflow(flag bool) {
	metricsHTTPMeasurements.lock.Lock()
	metricsHTTPMeasurements.transactionNameOverflow = flag
	metricsHTTPMeasurements.lock.Unlock()
}

func addMeasurementToBSON(bbuf *bsonBuffer, index *int, m *Measurement) {
	start := bsonAppendStartObject(bbuf, strconv.Itoa(*index))

	bsonAppendString(bbuf, "name", m.name)
	bsonAppendInt(bbuf, "count", m.count)
	if m.reportValue {
		bsonAppendFloat64(bbuf, "sum", m.sum)
	}

	if len(m.tags) > 0 {
		start := bsonAppendStartObject(bbuf, "tags")
		for k, v := range m.tags {
			if len(k) > metricsTagNameLenghtMax {
				k = k[0:metricsTagNameLenghtMax]
			}
			if len(v) > metricsTagValueLenghtMax {
				v = v[0:metricsTagValueLenghtMax]
			}
			bsonAppendString(bbuf, k, v)
		}
		bsonAppendFinishObject(bbuf, start)
	}

	bsonAppendFinishObject(bbuf, start)
	*index += 1
}
