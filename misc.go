//  Copyright (c) 2014 Couchbase, Inc.
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the
//  License. You may obtain a copy of the License at
//    http://www.apache.org/licenses/LICENSE-2.0
//  Unless required by applicable law or agreed to in writing,
//  software distributed under the License is distributed on an "AS
//  IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
//  express or implied. See the License for the specific language
//  governing permissions and limitations under the License.

package cbgt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rcrowley/go-metrics"
)

func init() {
	rand.Seed(time.Now().UTC().UnixNano())
}

// DiagHandler allows modules to provide their own additions in
// response to "diag" or diagnostic information requests.
type DiagHandler struct {
	Name        string
	Handler     http.Handler
	HandlerFunc http.HandlerFunc
}

// Documentation is used for auto-generated documentation.
type Documentation struct {
	Text string      // Optional documentation text (markdown).
	JSON interface{} // Optional marshall'able to JSON.
}

var EMPTY_BYTES = []byte{}

var JsonNULL = []byte("null")
var JsonOpenBrace = []byte("{")
var JsonCloseBrace = []byte("}")
var JsonCloseBraceComma = []byte("},")
var JsonComma = []byte(",")

// IndentJSON is a helper func that returns indented JSON for its
// interface{} x parameter.
func IndentJSON(x interface{}, prefix, indent string) string {
	j, err := json.Marshal(x)
	if err != nil {
		return fmt.Sprintf("misc: IndentJSON marshal, err: %v", err)
	}
	var buf bytes.Buffer
	err = json.Indent(&buf, j, prefix, indent)
	if err != nil {
		return fmt.Sprintf("misc: IndentJSON indent, err: %v", err)
	}
	return buf.String()
}

// ErrorToString is a helper func that returns e.Error(), but also
// returns "" for nil error.
func ErrorToString(e error) string {
	if e != nil {
		return e.Error()
	}
	return ""
}

// Compares two dotted versioning strings, like "1.0.1" and "1.2.3".
// Returns true when x >= y.
//
// TODO: Need to handle non-numeric parts?
func VersionGTE(x, y string) bool {
	xa := strings.Split(x, ".")
	ya := strings.Split(y, ".")
	for i := range xa {
		if i >= len(ya) {
			return true
		}
		xv, err := strconv.Atoi(xa[i])
		if err != nil {
			return false
		}
		yv, err := strconv.Atoi(ya[i])
		if err != nil {
			return false
		}
		if xv > yv {
			return true
		}
		if xv < yv {
			return false
		}
	}
	return len(xa) >= len(ya)
}

func NewUUID() string {
	val1 := rand.Int63()
	val2 := rand.Int63()
	uuid := fmt.Sprintf("%x%x", val1, val2)
	return uuid[0:16]
}

// Calls f() in a loop, sleeping in an exponential backoff if needed.
// The provided f() function should return < 0 to stop the loop; >= 0
// to continue the loop, where > 0 means there was progress which
// allows an immediate retry of f() with no sleeping.  A return of < 0
// is useful when f() will never make any future progress.
func ExponentialBackoffLoop(name string,
	f func() int,
	startSleepMS int,
	backoffFactor float32,
	maxSleepMS int) {
	nextSleepMS := startSleepMS
	for {
		progress := f()
		if progress < 0 {
			return
		}
		if progress > 0 {
			// When there was some progress, we can reset nextSleepMS.
			nextSleepMS = startSleepMS
		} else {
			// If zero progress was made this cycle, then sleep.
			time.Sleep(time.Duration(nextSleepMS) * time.Millisecond)

			// Increase nextSleepMS in case next time also has 0 progress.
			nextSleepMS = int(float32(nextSleepMS) * backoffFactor)
			if nextSleepMS > maxSleepMS {
				nextSleepMS = maxSleepMS
			}
		}
	}
}

// StringsToMap connverts an array of (perhaps duplicated) strings
// into a map with key of those strings and values of true, and is
// useful for simple set-like operations.
func StringsToMap(strsArr []string) map[string]bool {
	if strsArr == nil {
		return nil
	}
	strs := map[string]bool{}
	for _, str := range strsArr {
		strs[str] = true
	}
	return strs
}

// StringsRemoveStrings returns a copy of stringArr, but with some
// strings removed, keeping the same order as stringArr.
func StringsRemoveStrings(stringArr, removeArr []string) []string {
	removeMap := StringsToMap(removeArr)
	rv := make([]string, 0, len(stringArr))
	for _, s := range stringArr {
		if !removeMap[s] {
			rv = append(rv, s)
		}
	}
	return rv
}

// StringsIntersectStrings returns a brand new array that has the
// intersection of a and b.
func StringsIntersectStrings(a, b []string) []string {
	bMap := StringsToMap(b)
	rMap := map[string]bool{}
	rv := make([]string, 0, len(a))
	for _, s := range a {
		if bMap[s] && !rMap[s] {
			rMap[s] = true
			rv = append(rv, s)
		}
	}
	return rv
}

// TimeoutCancelChan creates a channel that closes after a given
// timeout in milliseconds.
func TimeoutCancelChan(timeout int64) <-chan bool {
	if timeout > 0 {
		cancelCh := make(chan bool, 1)
		go func() {
			time.Sleep(time.Duration(timeout) * time.Millisecond)
			close(cancelCh)
		}()
		return cancelCh
	}
	return nil
}

// Time invokes a func f and updates the totalDuration, totalCount and
// maxDuration metrics.  See also Timer() for a metrics based
// alternative.
func Time(f func() error,
	totalDuration, totalCount, maxDuration *uint64) error {
	startTime := time.Now()
	err := f()
	duration := uint64(time.Since(startTime))
	atomic.AddUint64(totalDuration, duration)
	if totalCount != nil {
		atomic.AddUint64(totalCount, 1)
	}
	if maxDuration != nil {
		retry := true
		for retry {
			retry = false
			md := atomic.LoadUint64(maxDuration)
			if md < duration {
				retry = !atomic.CompareAndSwapUint64(maxDuration, md, duration)
			}
		}
	}
	return err
}

// Timer updates a metrics.Timer.  Unlike metrics.Timer.Time(), this
// version also captures any error return value.
func Timer(f func() error, t metrics.Timer) error {
	var err error
	t.Time(func() {
		err = f()
	})
	return err
}

// AtomicCopyMetrics copies uint64 metrics from s to r (from source to
// result), and also applies an optional fn function to each metric.
// The fn is invoked with metrics from s and r, and can be used to
// compute additions, subtractions, etc.  When fn is nil, AtomicCopyTo
// defaults to just a straight copier.
func AtomicCopyMetrics(s, r interface{},
	fn func(sv uint64, rv uint64) uint64) {
	// Using reflection rather than a whole slew of explicit
	// invocations of atomic.LoadUint64()/StoreUint64()'s.
	if fn == nil {
		fn = func(sv uint64, rv uint64) uint64 { return sv }
	}
	rve := reflect.ValueOf(r).Elem()
	sve := reflect.ValueOf(s).Elem()
	svet := sve.Type()
	for i := 0; i < svet.NumField(); i++ {
		rvef := rve.Field(i)
		svef := sve.Field(i)
		if rvef.CanAddr() && svef.CanAddr() {
			rvefp := rvef.Addr().Interface()
			svefp := svef.Addr().Interface()
			rv := atomic.LoadUint64(rvefp.(*uint64))
			sv := atomic.LoadUint64(svefp.(*uint64))
			atomic.StoreUint64(rvefp.(*uint64), fn(sv, rv))
		}
	}
}

// StructChanges uses reflection to compare the fields of two structs,
// which must the same type, and returns info on the changes of field
// values.
func StructChanges(a1, a2 interface{}) (rv []string) {
	if a1 == nil || a2 == nil {
		return nil
	}

	v1 := reflect.ValueOf(a1)
	v2 := reflect.ValueOf(a2)
	if v1.Type() != v2.Type() {
		return nil
	}
	if v1.Kind() != v2.Kind() ||
		v1.Kind() != reflect.Struct {
		return nil
	}

	for i := 0; i < v1.NumField(); i++ {
		v1f := v1.Field(i)
		v2f := v2.Field(i)
		if v1f.Kind() == v2f.Kind() &&
			v1f.Kind() == reflect.Int {
			if v1f.Int() != v2f.Int() {
				rv = append(rv, fmt.Sprintf("%s: %d -> %d",
					v2.Type().Field(i).Name, v1f.Int(), v2f.Int()))
			}
		}
	}

	return rv
}

var timerPercentiles = []float64{0.5, 0.75, 0.95, 0.99, 0.999}

// WriteTimerJSON writes a metrics.Timer instance as JSON to a
// io.Writer.
func WriteTimerJSON(w io.Writer, timer metrics.Timer) {
	t := timer.Snapshot()
	p := t.Percentiles(timerPercentiles)

	fmt.Fprintf(w, `{"count":%9d,`, t.Count())
	fmt.Fprintf(w, `"min":%9d,`, t.Min())
	fmt.Fprintf(w, `"max":%9d,`, t.Max())
	fmt.Fprintf(w, `"mean":%12.2f,`, t.Mean())
	fmt.Fprintf(w, `"stddev":%12.2f,`, t.StdDev())
	fmt.Fprintf(w, `"percentiles":{`)
	fmt.Fprintf(w, `"median":%12.2f,`, p[0])
	fmt.Fprintf(w, `"75%%":%12.2f,`, p[1])
	fmt.Fprintf(w, `"95%%":%12.2f,`, p[2])
	fmt.Fprintf(w, `"99%%":%12.2f,`, p[3])
	fmt.Fprintf(w, `"99.9%%":%12.2f},`, p[4])
	fmt.Fprintf(w, `"rates":{`)
	fmt.Fprintf(w, `"1-min":%12.2f,`, t.Rate1())
	fmt.Fprintf(w, `"5-min":%12.2f,`, t.Rate5())
	fmt.Fprintf(w, `"15-min":%12.2f,`, t.Rate15())
	fmt.Fprintf(w, `"mean":%12.2f}}`, t.RateMean())
}
