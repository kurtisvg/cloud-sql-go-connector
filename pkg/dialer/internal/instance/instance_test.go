// Copyright 2020 Google LLC

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     https://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package instance

import (
	"os"
	"testing"
)

var (
	instConnName = os.Getenv("INSTANCE_CONNECTION_NAME")
)

func TestParseConnName(t *testing.T) {
	tests := []struct {
		name string
		want connName
	}{
		{
			"project:region:instance",
			connName{"project", "region", "instance"},
		},
		{
			"google.com:project:region:instance",
			connName{"google.com:project", "region", "instance"},
		},
		{
			"project:instance",
			connName{},
		},
	}

	for _, tc := range tests {
		c, err := parseConnName(tc.name)
		if err != nil && tc.want != (connName{}) {
			t.Errorf("unexpected error: %e", err)
		}
		if c != tc.want {
			t.Errorf("ParseConnName(%s) failed: want %v, got %v", tc.name, tc.want, err)
		}
	}
}
