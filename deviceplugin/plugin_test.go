// Copyright 2026 the generic-device-plugin authors
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

package deviceplugin

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketNameUsesShortStableResourceHash(t *testing.T) {
	const (
		resource  = "devices.bytetrade.io/audio-p-3acc19de29010789aa7cd22d5b25446e"
		timestamp = int64(1789368506)
		want      = "gdp-f493094861b904a23678f932daccd14f-1789368506.sock"
	)

	got := socketName(resource, timestamp)
	if got != want {
		t.Fatalf("socketName() = %q, want %q", got, want)
	}
	if strings.ContainsAny(got, "/+=") {
		t.Fatalf("socketName() contains unsafe path characters: %q", got)
	}

	const pluginDir = "/var/lib/kubelet/device-plugins"
	if gotLength := len(filepath.Join(pluginDir, got)); gotLength >= 108 {
		t.Fatalf("socket path length = %d, must be less than 108", gotLength)
	}
}

func TestSocketNameDiffersByResource(t *testing.T) {
	const timestamp = int64(1789368506)
	serial := socketName("devices.bytetrade.io/serial-s-device-one", timestamp)
	video := socketName("devices.bytetrade.io/video-s-device-one", timestamp)
	if serial == video {
		t.Fatalf("different resources produced the same socket name %q", serial)
	}
}
