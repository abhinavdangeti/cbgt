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
	"container/list"
	"fmt"
	"io"

	"github.com/gorilla/mux"

	"github.com/rcrowley/go-metrics"
)

// PIndexImpl represents a runtime pindex implementation instance,
// whose runtime type depends on the pindex's type.
type PIndexImpl interface{}

// PIndexImplType defines the functions that every pindex
// implementation type must register on startup.
type PIndexImplType struct {
	// Prepare provides a way to customize the index definition.
	Prepare func(indexDef *IndexDef) (*IndexDef, error)

	// Invoked by the manager when it wants validate indef definition
	// inputs before doing the actual creation.
	Validate func(indexType, indexName, indexParams string) error

	// Invoked by the manager when it wants to create an index
	// partition.  The pindex implementation should persist enough
	// info into the path subdirectory so that it can reconstitute the
	// pindex during restart and Open().
	New func(indexType, indexParams, path string, restart func()) (
		PIndexImpl, Dest, error)

	// Invoked by the manager when it wants a pindex implementation to
	// reconstitute and reload a pindex instance back into the
	// process, such as when the process has re-started.
	Open func(indexType, path string, restart func()) (
		PIndexImpl, Dest, error)

	// Optional, invoked by the manager when it wants a pindex
	// implementation to reconstitute and reload a pindex instance
	// back into the process, with the updated index parameter values.
	OpenUsing func(indexType, path, indexParams string,
		restart func()) (PIndexImpl, Dest, error)

	// Invoked by the manager when it wants a count of documents from
	// an index.  The registered Count() function can be nil.
	Count func(mgr *Manager, indexName, indexUUID string) (
		uint64, error)

	// Invoked by the manager when it wants to query an index.  The
	// registered Query() function can be nil.
	Query func(mgr *Manager, indexName, indexUUID string,
		req []byte, res io.Writer) error

	// Description is used to populate docs, UI, etc, such as index
	// type drop-down control in the web admin UI.  Format of the
	// description string:
	//
	//    $categoryName/$indexType - short descriptive string
	//
	// The $categoryName is something like "advanced", or "general".
	Description string

	// A prototype instance of indexParams JSON that is usable for
	// Validate() and New().
	StartSample interface{}

	// Example instances of JSON that are usable for Query requests().
	// These are used to help generate API documentation.
	QuerySamples func() []Documentation

	// Displayed in docs, web admin UI, etc, and often might be a link
	// to even further help.
	QueryHelp string

	// Invoked during startup to allow pindex implementation to affect
	// the REST API with its own endpoint.
	InitRouter func(r *mux.Router, phase string, mgr *Manager)

	// Optional, additional handlers a pindex implementation may have
	// for /api/diag output.
	DiagHandlers []DiagHandler

	// Optional, allows pindex implementation to add more information
	// to the REST /api/managerMeta output.
	MetaExtra func(map[string]interface{})

	// Optional, allows pindex implementation to specify advanced UI
	// implementations and information.
	UI map[string]string

	// Optional, invoked for checking whether the pindex implementations
	// can effect the config changes through a restart of pindexes.
	AnalyzeIndexDefUpdates func(configUpdates *ConfigAnalyzeRequest) ResultCode

	// Invoked by the manager when it wants to trigger generic operations
	// on the index.
	SubmitTaskRequest func(mgr *Manager, indexName,
		indexUUID string, req []byte) (*TaskRequestStatus, error)
}

// ConfigAnalyzeRequest wraps up the various configuration
// parameters that the PIndexImplType implementations deals with.
type ConfigAnalyzeRequest struct {
	IndexDefnCur         *IndexDef
	IndexDefnPrev        *IndexDef
	SourcePartitionsCur  map[string]bool
	SourcePartitionsPrev map[string]bool
}

// ResultCode represents the return code indicative of the various operations
// recommended by the pindex implementations upon detecting a config change.
type ResultCode string

const (
	// PINDEXES_RESTART suggests a reboot of the pindexes
	PINDEXES_RESTART ResultCode = "request_restart_pindexes"
)

// PIndexImplTypes is a global registry of pindex type backends or
// implementations.  It is keyed by indexType and should be treated as
// immutable/read-only after process init/startup.
var PIndexImplTypes = make(map[string]*PIndexImplType)

// RegisterPIndexImplType registers a index type into the system.
func RegisterPIndexImplType(indexType string, t *PIndexImplType) {
	PIndexImplTypes[indexType] = t
}

// NewPIndexImpl creates an index partition of the given, registered
// index type.
func NewPIndexImpl(indexType, indexParams, path string, restart func()) (
	PIndexImpl, Dest, error) {
	t, exists := PIndexImplTypes[indexType]
	if !exists || t == nil || t.New == nil {
		return nil, nil,
			fmt.Errorf("pindex_impl: NewPIndexImpl indexType: %s",
				indexType)
	}

	return t.New(indexType, indexParams, path, restart)
}

// OpenPIndexImpl loads an index partition of the given, registered
// index type from a given path.
func OpenPIndexImpl(indexType, path string, restart func()) (
	PIndexImpl, Dest, error) {
	t, exists := PIndexImplTypes[indexType]
	if !exists || t == nil || t.Open == nil {
		return nil, nil, fmt.Errorf("pindex_impl: OpenPIndexImpl"+
			" indexType: %s", indexType)
	}

	return t.Open(indexType, path, restart)
}

// OpenPIndexImplUsing loads an index partition of the given, registered
// index type from a given path with the given indexParams.
func OpenPIndexImplUsing(indexType, path, indexParams string,
	restart func()) (PIndexImpl, Dest, error) {
	t, exists := PIndexImplTypes[indexType]
	if !exists || t == nil || t.OpenUsing == nil {
		return nil, nil, fmt.Errorf("pindex_impl: OpenPIndexImplUsing"+
			" indexType: %s", indexType)
	}

	return t.OpenUsing(indexType, path, indexParams, restart)
}

// PIndexImplTypeForIndex retrieves from the Cfg provider the index
// type for a given index.
func PIndexImplTypeForIndex(cfg Cfg, indexName string) (
	*PIndexImplType, error) {
	_, pindexImplType, err := GetIndexDef(cfg, indexName)

	return pindexImplType, err
}

// GetIndexDef retrieves the IndexDef and PIndexImplType for an index.
func GetIndexDef(cfg Cfg, indexName string) (
	*IndexDef, *PIndexImplType, error) {
	indexDefs, _, err := CfgGetIndexDefs(cfg)
	if err != nil || indexDefs == nil {
		return nil, nil, fmt.Errorf("pindex_impl: could not get indexDefs,"+
			" indexName: %s, err: %v",
			indexName, err)
	}

	indexDef := indexDefs.IndexDefs[indexName]
	if indexDef == nil {
		return nil, nil, fmt.Errorf("pindex_impl: no indexDef,"+
			" indexName: %s", indexName)
	}

	pindexImplType := PIndexImplTypes[indexDef.Type]
	if pindexImplType == nil {
		return nil, nil, fmt.Errorf("pindex_impl: no pindexImplType,"+
			" indexName: %s, indexDef.Type: %s",
			indexName, indexDef.Type)
	}

	return indexDef, pindexImplType, nil
}

// ------------------------------------------------

// QueryCtlParams defines the JSON that includes the "ctl" part of a
// query request.  These "ctl" query request parameters are
// independent of any specific pindex type.
type QueryCtlParams struct {
	Ctl QueryCtl `json:"ctl"`
}

// QueryCtl defines the JSON parameters that control query execution
// and which are independent of any specific pindex type.
//
// A PartitionSelection value can optionally be specified for performing
// advanced scatter gather operations, recognized options:
// - ""                : default behavior - active partitions only
// - "advanced-local"  : local partitions are favored
// - "advanced-random" : pseudo-random selection from available options
type QueryCtl struct {
	Timeout            int64              `json:"timeout"`
	Consistency        *ConsistencyParams `json:"consistency"`
	PartitionSelection string             `json:"partition_selection,omitempty"`
}

// QUERY_CTL_DEFAULT_TIMEOUT_MS is the default query timeout.
const QUERY_CTL_DEFAULT_TIMEOUT_MS = int64(10000)

// ------------------------------------------------

// PINDEX_STORE_MAX_ERRORS is the max number of errors that a
// PIndexStoreStats will track.
var PINDEX_STORE_MAX_ERRORS = 40

// PIndexStoreStats provides some common stats/metrics and error
// tracking that some pindex type backends can reuse.
type PIndexStoreStats struct {
	TimerBatchStore metrics.Timer
	Errors          *list.List // Capped list of string (json).
	TotalErrorCount uint64
}

func (d *PIndexStoreStats) WriteJSON(w io.Writer) {
	w.Write([]byte(`{"TimerBatchStore":`))
	WriteTimerJSON(w, d.TimerBatchStore)

	if d.Errors != nil {
		w.Write([]byte(`,"Errors":[`))
		e := d.Errors.Front()
		i := 0
		for e != nil {
			j, ok := e.Value.(string)
			if ok && j != "" {
				if i > 0 {
					w.Write(JsonComma)
				}
				w.Write([]byte(j))
			}
			e = e.Next()
			i = i + 1
		}
		w.Write([]byte(`]`))
	}

	w.Write(JsonCloseBrace)
}

var prefixPIndexStoreStats = []byte(`{"pindexStoreStats":`)
