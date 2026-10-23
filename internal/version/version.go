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

import (
	"fmt"
)

// Version and CommitHash are set at build time with -ldflags. Version is the
// git tag when the build is exactly on a tag, and empty otherwise.
var (
	Version    string
	CommitHash string
)

// GetVersionString returns the version and commit for display, or "devel" in
// place of the version for a build that is not on a tag.
func GetVersionString() string {
	if Version != "" {
		return fmt.Sprintf("%s (commit %s)", Version, CommitHash)
	}
	return fmt.Sprintf("devel (commit %s)", CommitHash)
}
