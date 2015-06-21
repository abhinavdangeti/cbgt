//  Copyright (c) 2014 Couchbase, Inc.
//  Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file
//  except in compliance with the License. You may obtain a copy of the License at
//    http://www.apache.org/licenses/LICENSE-2.0
//  Unless required by applicable law or agreed to in writing, software distributed under the
//  License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
//  either express or implied. See the License for the specific language governing permissions
//  and limitations under the License.

package rest

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	log "github.com/couchbase/clog"
	"github.com/couchbaselabs/cbgt"
)

var StartTime = time.Now()

func ShowError(w http.ResponseWriter, r *http.Request,
	msg string, code int) {
	log.Printf("rest: error code: %d, msg: %s", code, msg)
	http.Error(w, msg, code)
}

func MuxVariableLookup(req *http.Request, name string) string {
	return mux.Vars(req)[name]
}

func DocIDLookup(req *http.Request) string {
	return MuxVariableLookup(req, "docID")
}

func IndexNameLookup(req *http.Request) string {
	return MuxVariableLookup(req, "indexName")
}

func PIndexNameLookup(req *http.Request) string {
	return MuxVariableLookup(req, "pindexName")
}

// RESTMeta represents the metadata of a REST API endpoint and is used
// for auto-generated REST API documentation.
type RESTMeta struct {
	Path   string
	Method string
	Opts   map[string]string
}

// RESTOpts interface may be optionally implemented by REST API
// handlers to provide even more information for auto-generated REST
// API documentation.
type RESTOpts interface {
	RESTOpts(map[string]string)
}

// InitManagerRESTRouter initializes a mux.Router with REST API
// routes.
func InitManagerRESTRouter(r *mux.Router, versionMain string,
	mgr *cbgt.Manager, staticDir, staticETag string,
	mr *cbgt.MsgRing) (
	*mux.Router, map[string]RESTMeta, error) {
	PIndexTypesInitRouter(r, "manager.before")

	methodOrds := map[string]string{
		"GET":    "0",
		"POST":   "1",
		"PUT":    "2",
		"DELETE": "3",
	}

	meta := map[string]RESTMeta{}
	handle := func(path string, method string, h http.Handler,
		opts map[string]string) {
		if a, ok := h.(RESTOpts); ok {
			a.RESTOpts(opts)
		}
		meta[path+" "+methodOrds[method]+method] =
			RESTMeta{path, method, opts}
		r.Handle(path, h).Methods(method)
	}
	handleFunc := func(path string, method string, h http.HandlerFunc,
		opts map[string]string) {
		meta[path+" "+methodOrds[method]+method] =
			RESTMeta{path, method, opts}
		r.HandleFunc(path, h).Methods(method)
	}

	handle("/api/index", "GET", NewListIndexHandler(mgr),
		map[string]string{
			"_category":          "Indexing|Index definition",
			"_about":             `Returns all index definitions as JSON.`,
			"version introduced": "0.0.1",
		})
	handle("/api/index/{indexName}", "PUT", NewCreateIndexHandler(mgr),
		map[string]string{
			"_category":          "Indexing|Index definition",
			"_about":             `Creates/updates an index definition.`,
			"version introduced": "0.0.1",
		})
	handle("/api/index/{indexName}", "DELETE", NewDeleteIndexHandler(mgr),
		map[string]string{
			"_category":          "Indexing|Index definition",
			"_about":             `Deletes an index definition.`,
			"version introduced": "0.0.1",
		})
	handle("/api/index/{indexName}", "GET", NewGetIndexHandler(mgr),
		map[string]string{
			"_category":          "Indexing|Index definition",
			"_about":             `Returns the definition of an index as JSON.`,
			"version introduced": "0.0.1",
		})

	if mgr == nil || mgr.TagsMap() == nil || mgr.TagsMap()["queryer"] {
		handle("/api/index/{indexName}/count", "GET",
			NewCountHandler(mgr),
			map[string]string{
				"_category":          "Indexing|Index querying",
				"_about":             `Returns the count of indexed documents.`,
				"version introduced": "0.0.1",
			})
		handle("/api/index/{indexName}/query", "POST",
			NewQueryHandler(mgr),
			map[string]string{
				"_category":          "Indexing|Index querying",
				"_about":             `Queries an index.`,
				"version introduced": "0.2.0",
			})
	}

	handle("/api/index/{indexName}/planFreezeControl/{op}", "POST",
		NewIndexControlHandler(mgr, "planFreeze", map[string]bool{
			"freeze":   true,
			"unfreeze": true,
		}),
		map[string]string{
			"_category": "Indexing|Index management",
			"_about":    `Freeze the assignment of index partitions to nodes.`,
			"param: op": "required, string, URL path parameter\n\n" +
				`Allowed values for op are "freeze" or "unfreeze".`,
			"version introduced": "0.0.1",
		})
	handle("/api/index/{indexName}/ingestControl/{op}", "POST",
		NewIndexControlHandler(mgr, "write", map[string]bool{
			"pause":  true,
			"resume": true,
		}),
		map[string]string{
			"_category": "Indexing|Index management",
			"_about": `Pause index updates and maintenance (no more
                          ingesting document mutations).`,
			"param: op": "required, string, URL path parameter\n\n" +
				`Allowed values for op are "pause" or "resume".`,
			"version introduced": "0.0.1",
		})
	handle("/api/index/{indexName}/queryControl/{op}", "POST",
		NewIndexControlHandler(mgr, "read", map[string]bool{
			"allow":    true,
			"disallow": true,
		}),
		map[string]string{
			"_category": "Indexing|Index management",
			"_about":    `Disallow queries on an index.`,
			"param: op": "required, string, URL path parameter\n\n" +
				`Allowed values for op are "allow" or "disallow".`,
			"version introduced": "0.0.1",
		})

	if mgr == nil || mgr.TagsMap() == nil || mgr.TagsMap()["pindex"] {
		handle("/api/pindex", "GET",
			NewListPIndexHandler(mgr),
			map[string]string{
				"_category":          "x/Advanced|x/Index partition definition",
				"version introduced": "0.0.1",
			})
		handle("/api/pindex/{pindexName}", "GET",
			NewGetPIndexHandler(mgr),
			map[string]string{
				"_category":          "x/Advanced|x/Index partition definition",
				"version introduced": "0.0.1",
			})
		handle("/api/pindex/{pindexName}/count", "GET",
			NewCountPIndexHandler(mgr),
			map[string]string{
				"_category":          "x/Advanced|x/Index partition querying",
				"version introduced": "0.0.1",
			})
		handle("/api/pindex/{pindexName}/query", "POST",
			NewQueryPIndexHandler(mgr),
			map[string]string{
				"_category":          "x/Advanced|x/Index partition querying",
				"version introduced": "0.2.0",
			})
	}

	handle("/api/cfg", "GET", NewCfgGetHandler(mgr),
		map[string]string{
			"_category": "Node|Node configuration",
			"_about": `Returns the node's current view
                       of the cluster's configuration as JSON.`,
			"version introduced": "0.0.1",
		})

	handle("/api/cfgRefresh", "POST", NewCfgRefreshHandler(mgr),
		map[string]string{
			"_category": "Node|Node configuration",
			"_about": `Requests the node to refresh its configuration
                       from the configuration provider.`,
			"version introduced": "0.0.1",
		})

	handle("/api/log", "GET", NewLogGetHandler(mgr, mr),
		map[string]string{
			"_category": "Node|Node diagnostics",
			"_about": `Returns recent log messages
                       and key events for the node as JSON.`,
			"version introduced": "0.0.1",
		})

	handle("/api/managerKick", "POST", NewManagerKickHandler(mgr),
		map[string]string{
			"_category": "Node|Node configuration",
			"_about": `Forces the node to replan resource assignments
                       (by running the planner, if enabled) and to update
                       its runtime state to reflect the latest plan
                       (by running the janitor, if enabled).`,
			"version introduced": "0.0.1",
		})

	handle("/api/managerMeta", "GET", NewManagerMetaHandler(mgr, meta),
		map[string]string{
			"_category": "Node|Node configuration",
			"_about": `Returns information on the node's capabilities,
                       including available indexing and storage options as JSON,
                       and is intended to help management tools and web UI's
                       to be more dynamically metadata driven.`,
			"version introduced": "0.0.1",
		})

	handle("/api/runtime", "GET",
		NewRuntimeGetHandler(versionMain, mgr),
		map[string]string{
			"_category": "Node|Node diagnostics",
			"_about": `Returns information on the node's software,
                       such as version strings and slow-changing
                       runtime settings as JSON.`,
			"version introduced": "0.0.1",
		})

	handleFunc("/api/runtime/args", "GET",
		restGetRuntimeArgs, map[string]string{
			"_category": "Node|Node diagnostics",
			"_about": `Returns information on the node's command-line,
                       parameters, environment variables and
                       O/S process values as JSON.`,
			"version introduced": "0.0.1",
		})

	handleFunc("/api/runtime/gc", "POST",
		restPostRuntimeGC, map[string]string{
			"_category":          "Node|Node management",
			"_about":             `Requests the node to perform a GC.`,
			"version introduced": "0.0.1",
		})

	handleFunc("/api/runtime/profile/cpu", "POST",
		restProfileCPU, map[string]string{
			"_category": "Node|Node diagnostics",
			"_about": `Requests the node to capture local
                       cpu usage profiling information.`,
			"version introduced": "0.0.1",
		})

	handleFunc("/api/runtime/profile/memory", "POST",
		restProfileMemory, map[string]string{
			"_category": "Node|Node diagnostics",
			"_about": `Requests the node to capture lcoal
                       memory usage profiling information.`,
			"version introduced": "0.0.1",
		})

	handleFunc("/api/runtime/stats", "GET",
		restGetRuntimeStats, map[string]string{
			"_category": "Node|Node monitoring",
			"_about": `Returns information on the node's
                       low-level runtime stats as JSON.`,
			"version introduced": "0.0.1",
		})

	handleFunc("/api/runtime/statsMem", "GET",
		restGetRuntimeStatsMem, map[string]string{
			"_category": "Node|Node monitoring",
			"_about": `Returns information on the node's
                       low-level GC and memory related runtime stats as JSON.`,
			"version introduced": "0.0.1",
		})

	handle("/api/stats", "GET", NewStatsHandler(mgr),
		map[string]string{
			"_category": "Indexing|Index monitoring",
			"_about": `Returns indexing and data related metrics,
                       timings and counters from the node as JSON.`,
			"version introduced": "0.0.1",
		})

	// TODO: If we ever implement cluster-wide index stats, we should
	// have it under /api/index/{indexName}/stats GET endpoint.
	//
	handle("/api/stats/index/{indexName}", "GET", NewStatsHandler(mgr),
		map[string]string{
			"_category": "Indexing|Index monitoring",
			"_about": `Returns metrics, timings and counters
                       for a single index from the node as JSON.`,
			"version introduced": "0.0.1",
		})

	PIndexTypesInitRouter(r, "manager.after")

	return r, meta, nil
}

// PIndexTypesInitRouter initializes a mux.Router with the REST API
// routes provided by registered pindex types.
func PIndexTypesInitRouter(r *mux.Router, phase string) {
	for _, t := range cbgt.PIndexImplTypes {
		if t.InitRouter != nil {
			t.InitRouter(r, phase)
		}
	}
}

// --------------------------------------------------------

// RuntimeGetHandler is a REST handler for runtime GET endpoint.
type RuntimeGetHandler struct {
	versionMain string
	mgr         *cbgt.Manager
}

func NewRuntimeGetHandler(
	versionMain string, mgr *cbgt.Manager) *RuntimeGetHandler {
	return &RuntimeGetHandler{versionMain: versionMain, mgr: mgr}
}

func (h *RuntimeGetHandler) ServeHTTP(
	w http.ResponseWriter, r *http.Request) {
	cbgt.MustEncode(w, map[string]interface{}{
		"versionMain": h.versionMain,
		"versionData": h.mgr.Version(),
		"arch":        runtime.GOARCH,
		"os":          runtime.GOOS,
		"numCPU":      runtime.NumCPU(),
		"go": map[string]interface{}{
			"GOMAXPROCS": runtime.GOMAXPROCS(0),
			"GOROOT":     runtime.GOROOT(),
			"version":    runtime.Version(),
			"compiler":   runtime.Compiler,
		},
	})
}

func restGetRuntimeArgs(w http.ResponseWriter, r *http.Request) {
	flags := map[string]interface{}{}
	flag.VisitAll(func(f *flag.Flag) {
		flags[f.Name] = f.Value
	})

	env := []string(nil)
	for _, e := range os.Environ() {
		if !strings.Contains(e, "PASSWORD") &&
			!strings.Contains(e, "PSWD") &&
			!strings.Contains(e, "AUTH") {
			env = append(env, e)
		}
	}

	groups, groupsErr := os.Getgroups()
	hostname, hostnameErr := os.Hostname()
	user, userErr := user.Current()
	wd, wdErr := os.Getwd()

	cbgt.MustEncode(w, map[string]interface{}{
		"args":  os.Args,
		"env":   env,
		"flags": flags,
		"process": map[string]interface{}{
			"euid":        os.Geteuid(),
			"gid":         os.Getgid(),
			"groups":      groups,
			"groupsErr":   cbgt.ErrorToString(groupsErr),
			"hostname":    hostname,
			"hostnameErr": cbgt.ErrorToString(hostnameErr),
			"pageSize":    os.Getpagesize(),
			"pid":         os.Getpid(),
			"ppid":        os.Getppid(),
			"user":        user,
			"userErr":     cbgt.ErrorToString(userErr),
			"wd":          wd,
			"wdErr":       cbgt.ErrorToString(wdErr),
		},
	})
}

func restPostRuntimeGC(w http.ResponseWriter, r *http.Request) {
	runtime.GC()
}

// To start a cpu profiling...
//    curl -X POST http://127.0.0.1:9090/api/runtime/profile/cpu -d secs=5
// To analyze a profiling...
//    go tool pprof ./cbft run-cpu.pprof
func restProfileCPU(w http.ResponseWriter, r *http.Request) {
	secs, err := strconv.Atoi(r.FormValue("secs"))
	if err != nil || secs <= 0 {
		http.Error(w, "incorrect or missing secs parameter", 400)
		return
	}
	fname := "./run-cpu.pprof"
	os.Remove(fname)
	f, err := os.Create(fname)
	if err != nil {
		http.Error(w, fmt.Sprintf("profileCPU:"+
			" couldn't create file: %s, err: %v",
			fname, err), 500)
		return
	}
	log.Printf("profileCPU: start, file: %s", fname)
	err = pprof.StartCPUProfile(f)
	if err != nil {
		http.Error(w, fmt.Sprintf("profileCPU:"+
			" couldn't start CPU profile, file: %s, err: %v",
			fname, err), 500)
		return
	}
	go func() {
		time.Sleep(time.Duration(secs) * time.Second)
		pprof.StopCPUProfile()
		f.Close()
		log.Printf("profileCPU: end, file: %s", fname)
	}()
	w.WriteHeader(204)
}

// To grab a memory profiling...
//    curl -X POST http://127.0.0.1:9090/api/runtime/profile/memory
// To analyze a profiling...
//    go tool pprof ./cbft run-memory.pprof
func restProfileMemory(w http.ResponseWriter, r *http.Request) {
	fname := "./run-memory.pprof"
	os.Remove(fname)
	f, err := os.Create(fname)
	if err != nil {
		http.Error(w, fmt.Sprintf("profileMemory:"+
			" couldn't create file: %v, err: %v",
			fname, err), 500)
		return
	}
	defer f.Close()
	pprof.WriteHeapProfile(f)
}

func restGetRuntimeStatsMem(w http.ResponseWriter, r *http.Request) {
	memStats := &runtime.MemStats{}
	runtime.ReadMemStats(memStats)
	cbgt.MustEncode(w, memStats)
}

func restGetRuntimeStats(w http.ResponseWriter, r *http.Request) {
	cbgt.MustEncode(w, map[string]interface{}{
		"currTime":  time.Now(),
		"startTime": StartTime,
		"go": map[string]interface{}{
			"numGoroutine":   runtime.NumGoroutine(),
			"numCgoCall":     runtime.NumCgoCall(),
			"memProfileRate": runtime.MemProfileRate,
		},
	})
}
