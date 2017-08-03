// Copyright (C) 2017 Librato, Inc. All rights reserved.

package traceview

import (
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Metrics key/value strings
const (
	BSON_KEY_HOST_ID                   = "HostID"
	BSON_KEY_UUID                      = "UUID"
	BSON_KEY_HOSTNAME                  = "Hostname"
	BSON_KEY_DISTRO                    = "Distro"
	BSON_KEY_PID                       = "PID"
	BSON_KEY_SYSNAME                   = "UnameSysName"
	BSON_KEY_VERSION                   = "UnameVersion"
	BSON_KEY_IPADDR                    = "IPAddresses"
	BSON_KEY_MAC                       = "MACAddresses"
	BSON_KEY_EC2_ID                    = "EC2InstanceID"
	BSON_KEY_EC2_ZONE                  = "EC2AvailabilityZone"
	BSON_KEY_DOCKER_CONTAINER_ID       = "DockerContainerID"
	BSON_KEY_TIMESTAMP                 = "Timestamp_u"
	BSON_KEY_FLUSH_INTERVAL            = "MetricsFlushInterval"
	BSON_KEY_TRANSACTION_NAME_OVERFLOW = "TransactionNameOverflow"
	BSON_KEY_MEASUREMENTS			= "measurements"
	BSON_KEY_HISTOGRAMS                = "histograms"
	BSON_KEY_HIST_NAME                 = "name"
	BSON_KEY_HIST_VALUE                = "value"
	BSON_KEY_HIST_TAGS                 = "tags"
	BSON_VALUE_HIST_TRANSACTION_RT     = "TransactionResponseTime"
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

// Paths (URLs or filesystems) for fetching instance metadata (AWS, Docker, etc.)
const (
	URL_FOR_AWS_INSTANCE_ID = "http://169.254.169.254/latest/meta-data/instance-id"
	URL_FOR_AWS_ZONE_ID     = "http://169.254.169.254/latest/meta-data/placement/availability-zone"
	CONTAINER_META_FILE     = "/proc/self/cgroup"
)

// Configurations
const (
	AWSInstanceIDFetchTimeout = 1 // in seconds
)

// metricsAppendSysMetadata appends system metadata to the metrics message
func (am *metricsAggregator) metricsAppendSysMetadata(bbuf *bsonBuffer) {
	am.appendHostname(bbuf)
	am.appendUUID(bbuf)
	am.appendDistro(bbuf)
	am.appendPID(bbuf)
	am.appendUname(bbuf)
	am.appendIPAddresses(bbuf)
	am.appendMAC(bbuf)
	am.appendAWSInstanceID(bbuf)
	am.appendAWSInstanceZone(bbuf)
	am.appendContainerID(bbuf)
	am.appendTimestamp(bbuf)
	am.appendFlushInterval(bbuf)
}

// resetCounters resets the maps/lists in metricsAggregateor. It's called after each time
// the metrics message is encoded
func (am *metricsAggregator) resetCounters() {
	am.transNames = make(map[string]bool)
	am.metrics = MetricsRaw{
		histograms:   make(map[string]*Histogram),
		measurements: make(map[string]*Measurement),
		transNamesOverflow: false,
	}
}

type Retriever func(am *metricsAggregator) interface{}

// appendData append data of various types with various appenders
func (am *metricsAggregator) appendData(bbuf *bsonBuffer, k string, retriever Retriever) {
	v := retriever(am)
	// Only support string int/int64 and string slice for now, as no other types are needed,
	// therefore we don't need reflect for generic types.
	switch v.(type) {
	case int:
		vi := v.(int)
		bsonAppendInt(bbuf, k, vi)
	case int64:
		vi64 := v.(int64)
		bsonAppendInt64(bbuf, k, vi64)
	case string:
		vs := v.(string)
		if vs == "" {
			return
		}
		bsonAppendString(bbuf, k, vs)
	case []string:
		vss := v.([]string)
		if vss == nil {
			return
		}
		start := bsonAppendStartArray(bbuf, k)
		var idx int = 0
		for _, v := range vss {
			if v == "" {
				continue
			}
			bsonAppendString(bbuf, strconv.Itoa(idx), v)
		}
		bsonAppendFinishObject(bbuf, start)

	default:
		errStr := fmt.Sprintf("appendData(): Bad type %v(%T)", v, v)
		OboeLog(INFO, errStr, nil)
	}
}

// appendHostname appends the hostname to the BSON buffer
func (am *metricsAggregator) appendHostname(bbuf *bsonBuffer) {
	am.appendData(
		bbuf,
		BSON_KEY_HOSTNAME,
		func(am *metricsAggregator) interface{} {
			hostname, _ := os.Hostname()
			return hostname
		})
}

// appendUUID appends the UUID to the BSON buffer
func (am *metricsAggregator) appendUUID(bbuf *bsonBuffer) {
	am.appendData(
		bbuf,
		BSON_KEY_UUID,
		func(am *metricsAggregator) interface{} {
			return am.getHostId()
		})
}

// getHostId gets the unique host identifier (e.g., AWS instance ID, docker container ID or MAC address)
// This function is not concurrency-safe.
func (am *metricsAggregator) getHostId() (id string) {
	var err error

	if id, ok := am.cachedSysMeta[BSON_KEY_HOST_ID]; ok {
		return id
	}

	// Use cached hostID
	defer func() {
		am.cachedSysMeta[BSON_KEY_HOST_ID] = id
	}()

	// Calculate the ID
	id = am.getContainerID()
	if id != "" {
		id = "container:" + id
		return id
	}

	// getAWSInstanceMeta has a internal cache and always return a string.
	id = am.getAWSInstanceMeta(BSON_KEY_EC2_ID, URL_FOR_AWS_INSTANCE_ID)
	if id != "" {
		id = "aws:" + id
		return id
	}

	// getMacList has a internal cache and always returns a string.
	ids := am.getMACList()
	if ids != nil {
		id = "mac:" + strings.Join(ids, ",")
		return id
	}

	// Fallback to undefined, no error is returned.
	id = "undefined"
	OboeLog(WARNING, "getHostId(): could not retrieve host ID. Last error", err)

	return id
}

// appendDistro appends the distro information to BSON buffer
func (am *metricsAggregator) appendDistro(bbuf *bsonBuffer) {
	am.appendData(
		bbuf,
		BSON_KEY_DISTRO,
		func(am *metricsAggregator) interface{} {
			return am.getDistro()
		})
}

// getDistro retrieves the distribution information and caches it in metricsAggregator
func (am *metricsAggregator) getDistro() (distro string) {
	if distro, ok := am.cachedSysMeta[BSON_KEY_DISTRO]; ok {
		return distro
	}

	var ds []string // distro slice

	// Use cached hostID
	defer func() {
		am.cachedSysMeta[BSON_KEY_DISTRO] = distro
	}()

	// Note: Order of checking is important because some distros share same file names
	// but with different function.
	// Keep this order: redhat based -> ubuntu -> debian

	// redhat
	if distro = getStrByKeyword(REDHAT, ""); distro != "" {
		return distro
	}
	// amazon linux
	distro = getStrByKeyword(AMAZON, "")
	ds = strings.Split(distro, ":")
	distro = ds[len(ds)-1]
	if distro != "" {
		distro = "Amzn Linux " + distro
		return distro
	}
	// ubuntu
	distro = getStrByKeyword(UBUNTU, "DISTRIB_DESCRIPTION")
	if distro != "" {
		ds = strings.Split(distro, "=")
		distro = ds[len(ds)-1]
		if distro != "" {
			distro = strings.Trim(distro, "\"")
		} else {
			distro = "Ubuntu unknown"
		}
		return distro
	}

	pathes := []string{DEBIAN, SUSE, SLACKWARE, GENTOO, OTHER}
	if path, line := getStrByKeywordFiles(pathes, ""); path != "" && line != "" {
		distro = line
		if path == "Debian" {
			distro = "Debian " + distro
		}
	} else {
		distro = "Unknown"
	}
	return distro
}

// appendPID appends the process ID to the BSON buffer
func (am *metricsAggregator) appendPID(bbuf *bsonBuffer) {
	am.appendData(
		bbuf,
		BSON_KEY_PID,
		func(am *metricsAggregator) interface{} {
			return os.Getpid()
		})
}

// appendSysName appends the uname.Sysname and Version to BSON buffer
func (am *metricsAggregator) appendUname(bbuf *bsonBuffer) {
	// There is no syscall.Uname (as well as Utsname) on macOS
	var uname syscall.Utsname
	if err := syscall.Uname(&uname); err == nil {
		am.appendData(
			bbuf,
			BSON_KEY_SYSNAME,
			func(am *metricsAggregator) interface{} {
				return uname.Sysname
			})
		am.appendData(
			bbuf,
			BSON_KEY_VERSION,
			func(am *metricsAggregator) interface{} {
				return uname.Version
			})
	}
}

// appendIPAddresses appends the IP addresses to the BSON buffer
func (am *metricsAggregator) appendIPAddresses(bbuf *bsonBuffer) {
	am.appendData(
		bbuf,
		BSON_KEY_IPADDR,
		func(am *metricsAggregator) interface{} {
			return am.getIPList()
		})
}

// getIPList gets the non-loopback IP addresses and returns a string with
// IP addresses separated with comma.
func (am *metricsAggregator) getIPList() (ips []string) {
	var ipStr string
	if ipStr, ok := am.cachedSysMeta[BSON_KEY_IPADDR]; ok {
		return strings.Split(ipStr, ",")
	}

	// Use cached MACList
	defer func() {
		am.cachedSysMeta[BSON_KEY_IPADDR] = ipStr
	}()

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			ipStr += ipnet.IP.String() + ","
		}
	}
	ipStr = strings.TrimSuffix(ipStr, ",") // Trim the final one
	ips = strings.Split(ipStr, ",")
	return
}

// appendMAC appends the MAC addresses to the BSON buffer
func (am *metricsAggregator) appendMAC(bbuf *bsonBuffer) {
	am.appendData(
		bbuf,
		BSON_KEY_MAC,
		func(am *metricsAggregator) interface{} {
			return am.getMACList()
		})
}

// getMACList retrieves the MAC addresses and caches it in the metrics struct
func (am *metricsAggregator) getMACList() (macs []string) {
	var macStr string
	if macStr, ok := am.cachedSysMeta[BSON_KEY_MAC]; ok {
		return strings.Split(macStr, ",")
	}

	// Use cached MACList
	defer func() {
		am.cachedSysMeta[BSON_KEY_MAC] = macStr
	}()

	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}
	for _, iface := range ifaces {
		if iface.Flags & net.FlagLoopback {
			continue
		}
		if mac := iface.HardwareAddr.String(); mac != "" {
			macStr += iface.HardwareAddr.String() + ","
		}
	}
	macStr = strings.TrimSuffix(macStr, ",") // Trim the final one
	macs = strings.Split(macStr, ",")
	return
}

// appendAWSInstanceID appends the EC2 Instance ID if any
func (am *metricsAggregator) appendAWSInstanceID(bbuf *bsonBuffer) {
	am.appendData(
		bbuf,
		BSON_KEY_EC2_ID,
		func(am *metricsAggregator) interface{} {
			return am.getAWSInstanceMeta(BSON_KEY_EC2_ID, URL_FOR_AWS_INSTANCE_ID)
		})
}

// getAWSInstanceMeta gets the AWS instance metadata, if any
func (am *metricsAggregator) getAWSInstanceMeta(key string, url string) (meta string) {
	if meta, ok := am.cachedSysMeta[key]; ok {
		return meta
	}

	// Use cached Instance metadata
	defer func() {
		am.cachedSysMeta[key] = meta
	}()

	// Retrieve the instance meta from a pre-defined URL
	// It's a synchronous call but we're OK as the metricsSender interval is 1 minute
	// (or 30 seconds?)
	timeout := time.Duration(AWSInstanceIDFetchTimeout * time.Second)
	client := http.Client{
		Timeout: timeout,
	}
	// I don't want to retry as the connection is supposed to be reliable if this is
	// an AWS instance.
	resp, err := client.Get(url)
	if err != nil {
		OboeLog(DEBUG, "getAWSInstanceMeta(): Timeout, not on AWS?", err)
		return
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return
	}

	meta = string(body)
	return
}

// appendAWSInstanceZone appends the EC2 zone information to the BSON buffer
func (am *metricsAggregator) appendAWSInstanceZone(bbuf *bsonBuffer) {
	am.appendData(
		bbuf,
		BSON_KEY_EC2_ZONE,
		func(am *metricsAggregator) interface{} {
			return am.getAWSInstanceMeta(BSON_KEY_EC2_ZONE, URL_FOR_AWS_ZONE_ID)
		})
}

// appendContainerID appends the docker container ID to the BSON buffer
func (am *metricsAggregator) appendContainerID(bbuf *bsonBuffer) {
	am.appendData(
		bbuf,
		BSON_KEY_DOCKER_CONTAINER_ID,
		func(am *metricsAggregator) interface{} {
			return am.getContainerID()
		})
}

// getContainerID retrieves the docker container id, if any, and caches it.
func (am *metricsAggregator) getContainerID() (id string) {
	if id, ok := am.cachedSysMeta[BSON_KEY_DOCKER_CONTAINER_ID]; ok {
		return id
	}

	// Use cached Instance metadata
	defer func() {
		am.cachedSysMeta[BSON_KEY_DOCKER_CONTAINER_ID] = id
	}()

	line := getLineByKeyword(CONTAINER_META_FILE, "docker")
	if line == "" {
		return "" // not found
	}
	tokens := strings.Split(line, "/")
	// A typical line returned by cat /proc/self/cgroup (that's why we expect 3 tokens):
	// 9:devices:/docker/40188af19439697187e3f60b933e7e37c5c41035f4c0b266a51c86c5a0074b25
	if len(tokens) != 3 {
		return ""
	}
	id = tokens[2]
	return
}

// appendTimestamp appends the timestamp information to the BSON buffer
func (am *metricsAggregator) appendTimestamp(bbuf *bsonBuffer) {
	//micro seconds since epoch
	am.appendData(
		bbuf,
		BSON_KEY_TIMESTAMP,
		func(am *metricsAggregator) interface{} {
			return int64(time.Now().UnixNano() / 1000)
		})
}

// appendFlushInterval appends the flush interval to the BSON buffer
func (am *metricsAggregator) appendFlushInterval(bbuf *bsonBuffer) {
	am.appendData(
		bbuf,
		BSON_KEY_FLUSH_INTERVAL,
		func(am *metricsAggregator) interface{} {
			return int64(agentMetricsInterval / time.Second)
		})
}

// appendTransactionNameOverflow appends the transaction name overflow flag to BSON buffer
func appendTransactionNameOverflow(bbuf *bsonBuffer, raw *MetricsRaw) {
	bsonAppendBool(bbuf, BSON_KEY_TRANSACTION_NAME_OVERFLOW, raw.transNamesOverflow)
}

// metricsAppendMeasurements appends global and transaction measurements to the metrics message
// This is a function rather than a method of metricsAggregator to avoid using am.metrics
// by mistake.
func metricsAppendMeasurements(bbuf *bsonBuffer, raw *MetricsRaw) {
	metricsAppendGlobalMeasurements(bbuf, raw)
	metricsAppendReporterStats(bbuf, raw)
	metricsAppendSystemLoad(bbuf, raw)
	metricsAppendTransactionMeasurements(bbuf, raw)
}

// metricsAppendGlobalMeasurements appends global counters to metrics message
func metricsAppendGlobalMeasurements(bbuf *bsonBuffer, raw *MetricsRaw) {
	// TODO: update global counters recorded in entry layers map
	// TODO: finish this part after the settings part is done.
}

// metricsAppendReporterStats appends reporter counters
func metricsAppendReporterStats(bbuf *bsonBuffer, raw *MetricsRaw) {
	// TODO:
}

// metricsAppendSystemLoad appends system load to metrics message
func metricsAppendSystemLoad(bbuf *bsonBuffer, raw *MetricsRaw) {
	// TODO
}

// metricsAppendTransactionMeasurements appends transaction based measurements to metrics
// message.
func metricsAppendTransactionMeasurements(bbuf *bsonBuffer, raw *MetricsRaw) {
	appendTransactionNameOverflow(bbuf, raw)
	//TODO: not done
}

// metricsAppendHistograms appends histograms to the metrics message
// This is a function rather than a method of metricsAggregator to avoid using am.metrics
// by mistake.
func metricsAppendHistograms(bbuf *bsonBuffer, raw *MetricsRaw) {
	start := bsonAppendStartArray(bbuf, BSON_KEY_HISTOGRAMS)
	var index int = 0
	for _, hist := range raw.histograms {
		addHistogram(bbuf, index, hist.encode(), hist.tags)
		index += 1
	}
	bsonAppendFinishObject(bbuf, start)
}

// addHistogram encode a single histogram to the metrics message
// This is a function rather than a method of metricsAggregator to avoid using am.metrics
// by mistake.
func addHistogram(bbuf *bsonBuffer, idx int, data string, tags map[string]string) {
	histStart := bsonAppendStartObject(bbuf, string(idx))
	bsonAppendString(bbuf, BSON_KEY_HIST_NAME, BSON_VALUE_HIST_TRANSACTION_RT)
	bsonAppendString(bbuf, BSON_KEY_HIST_VALUE, data)

	tagsStart := bsonAppendStartObject(bbuf, BSON_KEY_HIST_TAGS)
	for k, v := range tags {
		k = k[:MaxTagNameLength]
		v = v[:MaxTagValueLength]
		bsonAppendString(bbuf, k, v)
	}
	bsonAppendFinishObject(bbuf, tagsStart)
	bsonAppendFinishObject(bbuf, histStart)
}
