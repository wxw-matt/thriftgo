// Copyright 2023 CloudWeGo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package trim

import "github.com/cloudwego/thriftgo/parser"

func (t *Trimmer) preProcess(ast *parser.Thrift, filename string) bool {
	ret := t.markKeptPart(ast, filename)
	for i, include := range ast.Includes {
		marked := t.preProcess(include.Reference, filename)
		if marked {
			t.marks[filename][ast.Includes[i]] = struct{}{}
			ret = true
		}
	}
	return ret
}
