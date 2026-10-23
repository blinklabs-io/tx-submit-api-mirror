// Copyright 2024 Blink Labs Software
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package version

import "testing"

// A tagged build reports its tag and commit; an untagged build reports "devel"
// so a development binary is never mistaken for a release.
func TestGetVersionString(t *testing.T) {
	origVersion, origCommit := Version, CommitHash
	t.Cleanup(func() { Version, CommitHash = origVersion, origCommit })

	for _, tt := range []struct {
		name, version, commit, want string
	}{
		{"tagged", "v1.2.3", "abc1234", "v1.2.3 (commit abc1234)"},
		{"untagged", "", "abc1234", "devel (commit abc1234)"},
		{"no build info", "", "", "devel (commit )"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			Version, CommitHash = tt.version, tt.commit
			if got := GetVersionString(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
