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
	"fmt"
	"io"
	"sync"
)

// A MsgRing wraps an io.Writer, and remembers a ring of previous
// writes to the io.Writer.  It is concurrent safe and is useful, for
// example, for remembering recent log messages.
type MsgRing struct {
	m     sync.Mutex
	inner io.Writer
	Next  int      `json:"next"`
	Msgs  [][]byte `json:"msgs"`
}

// NewMsgRing returns a MsgRing of a given ringSize.
func NewMsgRing(inner io.Writer, ringSize int) (*MsgRing, error) {
	if inner == nil {
		return nil, fmt.Errorf("msg_ring: nil inner io.Writer")
	}
	if ringSize <= 0 {
		return nil, fmt.Errorf("msg_ring: non-positive ring size")
	}
	return &MsgRing{
		inner: inner,
		Next:  0,
		Msgs:  make([][]byte, ringSize),
	}, nil
}

// Implements the io.Writer interface.
func (m *MsgRing) Write(p []byte) (n int, err error) {
	m.m.Lock()

	m.Msgs[m.Next] = append([]byte(nil), p...) // Copy p.
	m.Next += 1
	if m.Next >= len(m.Msgs) {
		m.Next = 0
	}

	m.m.Unlock()

	return m.inner.Write(p)
}

// Retrieves the recent writes to the MsgRing.
func (m *MsgRing) Messages() [][]byte {
	rv := make([][]byte, 0, len(m.Msgs))

	m.m.Lock()

	n := len(m.Msgs)
	i := 0
	idx := m.Next
	for i < n {
		if msg := m.Msgs[idx]; msg != nil {
			rv = append(rv, msg)
		}
		idx += 1
		if idx >= n {
			idx = 0
		}
		i += 1
	}

	m.m.Unlock()

	return rv
}
