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
	"testing"
)

func TestSyncWorkReq(t *testing.T) {
	ch := make(chan *workReq)
	go func() {
		w, ok := <-ch
		if !ok || w == nil {
			t.Errorf("expected ok and w")
		}
		if w.op != "op" || w.msg != "msg" {
			t.Errorf("expected op and msg")
		}
		close(w.resCh)
		w, ok = <-ch
		if ok || w != nil {
			t.Errorf("expected done")
		}
	}()

	err := syncWorkReq(ch, "op", "msg", nil)
	if err != nil {
		t.Errorf("expect nil err")
	}
	close(ch)
}
