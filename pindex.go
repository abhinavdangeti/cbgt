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

package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
)

// A PIndex represents a "physical" index or a index "partition".

const PINDEX_META_FILENAME string = "PINDEX_META"
const pindexPathSuffix string = ".pindex"

type PIndex struct {
	Name             string     `json:"name"`
	UUID             string     `json:"uuid"`
	IndexType        string     `json:"indexType"`
	IndexName        string     `json:"indexName"`
	IndexUUID        string     `json:"indexUUID"`
	IndexSchema      string     `json:"indexSchema"`
	SourceType       string     `json:"sourceType"`
	SourceName       string     `json:"sourceName"`
	SourceUUID       string     `json:"sourceUUID"`
	SourcePartitions string     `json:"sourcePartitions"`
	Path             string     `json:"-"` // Transient, not persisted.
	Impl             PIndexImpl `json:"-"` // Transient, not persisted.
	Stream           Stream     `json:"-"` // Transient, not persisted.
}

type PIndexImpl interface {
	Close()
}

type PIndexManager interface {
	ClosePIndex(pindex *PIndex) error
}

func NewPIndex(mgr PIndexManager, name, uuid,
	indexType, indexName, indexUUID, indexSchema,
	sourceType, sourceName, sourceUUID, sourcePartitions,
	path string) (*PIndex, error) {
	impl, err := NewPIndexImpl(indexType, indexSchema, path)
	if err != nil {
		os.RemoveAll(path)
		return nil, fmt.Errorf("error: new indexType: %s, indexSchema: %s,"+
			" path: %s, err: %s", indexType, indexSchema, path, err)
	}

	pindex := &PIndex{
		Name:             name,
		UUID:             uuid,
		IndexType:        indexType,
		IndexName:        indexName,
		IndexUUID:        indexUUID,
		IndexSchema:      indexSchema,
		SourceType:       sourceType,
		SourceName:       sourceName,
		SourceUUID:       sourceUUID,
		SourcePartitions: sourcePartitions,
		Path:             path,
		Impl:             impl,
		Stream:           make(Stream),
	}
	buf, err := json.Marshal(pindex)
	if err != nil {
		impl.Close()
		os.RemoveAll(path)
		return nil, err
	}

	err = ioutil.WriteFile(path+string(os.PathSeparator)+PINDEX_META_FILENAME,
		buf, 0600)
	if err != nil {
		impl.Close()

		os.RemoveAll(path)

		return nil, fmt.Errorf("error: could not save PINDEX_META_FILENAME,"+
			" path: %s, err: %v", path, err)
	}

	go pindex.Run(mgr)
	return pindex, nil
}

// NOTE: Path argument must be a directory.
func OpenPIndex(mgr PIndexManager, path string) (*PIndex, error) {
	buf, err := ioutil.ReadFile(path + string(os.PathSeparator) + PINDEX_META_FILENAME)
	if err != nil {
		return nil, fmt.Errorf("error: could not load PINDEX_META_FILENAME,"+
			" path: %s, err: %v", path, err)
	}

	pindex := &PIndex{}
	err = json.Unmarshal(buf, pindex)
	if err != nil {
		return nil, fmt.Errorf("error: could not parse pindex json,"+
			" path: %s, err: %v", path, err)
	}

	impl, err := OpenPIndexImpl(pindex.IndexType, path)
	if err != nil {
		return nil, fmt.Errorf("error: could not open indexType: %s, path: %s, err: %v",
			pindex.IndexType, path, err)
	}

	pindex.Path = path
	pindex.Impl = impl
	pindex.Stream = make(Stream)

	go pindex.Run(mgr)
	return pindex, nil
}

func PIndexPath(dataDir, pindexName string) string {
	// TODO: path security checks / mapping here; ex: "../etc/pswd"
	return dataDir + string(os.PathSeparator) + pindexName + pindexPathSuffix
}

func ParsePIndexPath(dataDir, pindexPath string) (string, bool) {
	if !strings.HasSuffix(pindexPath, pindexPathSuffix) {
		return "", false
	}
	prefix := dataDir + string(os.PathSeparator)
	if !strings.HasPrefix(pindexPath, prefix) {
		return "", false
	}
	pindexName := pindexPath[len(prefix):]
	pindexName = pindexName[0 : len(pindexName)-len(pindexPathSuffix)]
	return pindexName, true
}
