// Copyright (C) 2017 Librato, Inc. All rights reserved.

package reporter

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"time"

	"gopkg.in/mgo.v2/bson"
)

// printBson prints the BSON message. It's not concurrent-safe and is for testing only
func printBson(message []byte) {
	m := make(map[string]interface{})
	bson.Unmarshal(message, m)
	b, _ := json.MarshalIndent(m, "", "  ")
	fmt.Println(time.Now().Format("15:04:05"), string(b))
}

// getLineByKeword reads a file, searches for the keyword and returns the matched line.
// It returns empty string "" if no match found or failed to open the path.
// Pass an empty string "" if you just need to get the first line.
func getLineByKeyword(path string, keyword string) string {
	if path == "" {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if line := scanner.Text(); strings.Contains(line, keyword) {
			return line
		}
	}
	// ignore any scanner.Err(), just return an empty string.
	return ""
}

// getStrByKeyword read a file, searches for the keyword and returns the matched line
// with trailing line-feed character trimmed.
func getStrByKeyword(path string, keyword string) string {
	return strings.Trim(getLineByKeyword(path, keyword), "\n")
}

// getStrByKeywordFiles does the same thing as getStrByKeyword but searches for a list
// of files and returns the first matched files and line
func getStrByKeywordFiles(pathes []string, keyword string) (path string, line string) {
	for _, path = range pathes {
		line = getStrByKeyword(path, keyword)
		if line != "" {
			return path, line
		}
	}
	return "", ""
}

// Min returns the lower value
func Min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

// Max returns the greater value
func Max(x, y int) int {
	if x > y {
		return x
	}
	return y
}

// Byte2String converts a byte array into a string
func Byte2String(bs []int8) string {
	b := make([]byte, len(bs))
	for i, v := range bs {
		b[i] = byte(v)
	}
	return string(b)
}

type hostnamer interface {
	Hostname() (name string, err error)
}
type osHostnamer struct{}

func (h osHostnamer) Hostname() (string, error) { return os.Hostname() }

func copyMap(from *map[string]string) map[string]string {
	to := make(map[string]string)
	for k, v := range *from {
		to[k] = v
	}

	return to
}

func argsToMap(capacity, ratePerSec float64, metricsFlushInterval, maxTransactions int) *map[string][]byte {
	args := make(map[string][]byte)

	if capacity > -1 {
		bits := math.Float64bits(capacity)
		bytes := make([]byte, 8)
		binary.LittleEndian.PutUint64(bytes, bits)
		args["BucketCapacity"] = bytes
	}
	if ratePerSec > -1 {
		bits := math.Float64bits(ratePerSec)
		bytes := make([]byte, 8)
		binary.LittleEndian.PutUint64(bytes, bits)
		args["BucketRate"] = bytes
	}
	if metricsFlushInterval > -1 {
		bytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(bytes, uint32(metricsFlushInterval))
		args["MetricsFlushInterval"] = bytes
	}
	if maxTransactions > -1 {
		bytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(bytes, uint32(maxTransactions))
		args["MaxTransactions"] = bytes
	}

	return &args
}

// simple check if go version is higher or equal to the given version
func isHigherOrEqualGoVersion(version string) bool {
	goVersion := strings.Split(runtime.Version(), ".")
	compVersion := strings.Split(version, ".")
	for i := 0; i < len(goVersion) && i < len(compVersion); i++ {
		l := len(compVersion[i])
		if len(goVersion[i]) > l {
			l = len(goVersion[i])
		}
		compVersion[i] = strings.Repeat("0", l-len(compVersion[i])) + compVersion[i]
		goVersion[i] = strings.Repeat("0", l-len(goVersion[i])) + goVersion[i]
		if strings.Compare(goVersion[i], compVersion[i]) == 1 {
			return true
		} else if strings.Compare(goVersion[i], compVersion[i]) == -1 {
			return false
		}
	}
	return true
}
