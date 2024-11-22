// Copyright 2024 Google LLC
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

package regexpand

import (
	"errors"
	"math"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestExpand_OK(t *testing.T) {
	for _, tc := range []struct {
		name  string
		regex string
		want  []string
	}{
		{
			name:  "cannot match: line anchors",
			regex: "$foo",
			want:  []string{},
		},
		{
			name:  "empty string: line anchors",
			regex: "^$^$",
			want:  []string{""},
		},
		{
			name:  "empty string: repetition",
			regex: "(foo){0}",
			want:  []string{""},
		},
		{
			name:  "empty string: empty regex",
			regex: "",
			want:  []string{""},
		},
		{
			name:  "anchors",
			regex: "(^red$|^blue$)",
			want:  []string{"blue", "red"},
		},
		{
			name:  "literal",
			regex: "foo",
			want:  []string{"foo"},
		},
		{
			name:  "char class",
			regex: "[0-9]",
			want:  []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"},
		},
		{
			name:  "alternate",
			regex: "pump(kin|s|)",
			want:  []string{"pump", "pumpkin", "pumps"},
		},
		{
			name:  "alternate 2",
			regex: "(^|^)^pumpkin$(ignored|)",
			want:  []string{"pumpkin"},
		},
		{
			name:  "quest",
			regex: "a?b?",
			want:  []string{"", "a", "ab", "b"},
		},
		{
			name:  "final quest",
			regex: "^park((ing$)?)?", // Only the outermost quests can include $.
			want:  []string{"park", "parking"},
		},
		{
			name:  "case folding",
			regex: `(?i:ab)-[cC]`,
			want:  []string{"AB-C", "AB-c", "Ab-C", "Ab-c", "aB-C", "aB-c", "ab-C", "ab-c"},
		},
		{
			// Unicode case folding has more than just "upper" and "lower".
			name:  "case orbit",
			regex: "(?i)σ",
			want: []string{
				"Σ", // Upper case
				"ς", // Lower case
				"σ", // Fold case
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Expand(tc.regex, 100)
			if err != nil {
				t.Fatalf("Expand(%q) returned error %v", tc.regex, err)
			}
			slices.Sort(got)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Expand(%q) returned diff (-want +got):\n%s", tc.regex, diff)
			}
		})
	}
}

func TestExpand_Errors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		regex   string
		maxLen  int
		wantErr error
	}{
		{
			name:    "char class",
			regex:   "^[a-z]$",
			maxLen:  10,
			wantErr: ErrorLargeResult,
		},
		{
			name:    "unreasonable folding",
			regex:   "^(?i:abcdefghijklmnopqrstuvwxyz)$",
			maxLen:  math.MaxInt,
			wantErr: ErrorLargeResult,
		},
		{
			name:    "wildcard",
			regex:   "^.*$",
			maxLen:  10,
			wantErr: ErrorInfinite,
		},
		{
			name:    "unbound repeat",
			regex:   "^a{100,}$",
			maxLen:  10,
			wantErr: ErrorInfinite,
		},
		{
			name:    "non-word",
			regex:   `^\bb\Bonjour$`,
			maxLen:  10,
			wantErr: ErrorUnsupported,
		},
		{
			name:    "branching of begin anchor",
			regex:   `(alt1|)^bonjour$`,
			maxLen:  10,
			wantErr: ErrorUnsupported,
		},
		{
			name:    "branching alternate of end anchor",
			regex:   `^bonjou(alt$|)r`,
			maxLen:  10,
			wantErr: ErrorUnsupported,
		},
		{
			name:    "branching quest of end anchor",
			regex:   `^bonjou$?r`,
			maxLen:  10,
			wantErr: ErrorUnsupported,
		},
		{
			name:   "final quest",
			maxLen: 10,
			regex:  "^park(ing$)?foo?", // Only the outermost quests can include $.
			// Check for the non-exported error to ensure the test exercises that code path.
			wantErr: errNotFinalQuest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Expand(tc.regex, tc.maxLen)
			if err == nil {
				t.Fatalf("Expand(%q) returned %v, want error", tc.regex, got)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Expand(%q) returned error %v, want %v", tc.regex, err, tc.wantErr)
			}
		})
	}
}

func TestASCIIRange(t *testing.T) {
	for _, tc := range []struct {
		name  string
		regex string
		want  string
	}{
		{
			name:  "empty",
			regex: "",
			want:  `[]`,
		},
		{
			name:  "literal",
			regex: "abcdnxyz",
			want:  `[a-dnx-z]`,
		},
		{
			name:  "case insensitive",
			regex: "(?i:abcd)-",
			want:  `[\-A-Da-d]`,
		},
		{
			name:  "numbers",
			regex: `[0-9-]`,
			want:  `[\-0-9]`,
		},
		{
			name:  "char class",
			regex: "[a-z]+",
			want:  `[a-z]`,
		},
		{
			name:  "alternate",
			regex: "ab?(c|d|)e{0,2}",
			want:  `[a-e]`,
		},
		{
			name:  "date-like",
			regex: `^\d+[/.]\d+[/.]\d+$`,
			want:  `[.-9]`,
		},
		{
			name:  "range limits",
			regex: `[\x00-\x7f]`,
			want:  `[\x00-\x7f]`,
		},
		{
			name:  "upper limit",
			regex: `\x7f`,
			want:  `[\x7f]`,
		},
		{
			name:  "lower limit",
			regex: `\x00`,
			want:  `[\x00]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, ok := ASCIIRange(tc.regex)
			if !ok {
				t.Fatalf("ASCIIRange(%q) returned false", tc.regex)
			}
			got := set.String()
			if got != tc.want {
				t.Errorf("ASCIIRange(%q) returned %s, want %s", tc.regex, got, tc.want)
			}
		})
	}
}

func FuzzExpand(f *testing.F) {
	const maxLen = 100

	// Skip re.Match() if we exceed these limits.
	// Fuzzing generates strings that cause re.Match() to timeout, even though Expand() is fine.
	const matchMaxReLen = 32 // Max regex length.
	const matchMaxNum = 16   // Max number of re.Match calls.
	const matchMaxLen = 1024 // Max matched string length.

	f.Add("(?:i)purple")
	f.Add("^green$")
	f.Add("[X-Z](red|blue){1,2}")

	f.Fuzz(func(t *testing.T, reStr string) {
		re, err := regexp.Compile(reStr)
		if err != nil {
			// Tells the fuzzer that this input is not interesting.
			t.Skip("invalid reStr")
		}
		got, err := Expand(reStr, maxLen)
		if err != nil {
			if errors.Is(err, errorInternal) {
				t.Errorf("Expand(%s) failed with: %s", reStr, err)
			}
			got = []string{}
			if _, complete := re.LiteralPrefix(); complete {
				t.Errorf("Expand(%s) failed with %s but LiteralPrefix succeeds", reStr, err)
			}
			t.Skipf("Expand(%s) failed with %s", reStr, err)
		}
		t.Logf("matches %d strings", len(got))
		if len(reStr) < matchMaxReLen {
			for _, s := range got[:min(len(got), matchMaxNum)] {
				if len(s) > matchMaxLen {
					continue
				}
				if !re.MatchString(s) && !strings.ContainsRune(s, utf8.RuneError) {
					t.Errorf("Expand(%q) returned non-matching %q (%+d)", reStr, s, []rune(s))
				}
			}
		}
		if prefix, complete := re.LiteralPrefix(); complete {
			found := false
			for _, s := range got {
				if prefix == s {
					found = true
				}
			}
			if !found {
				t.Errorf("Expand(%q) did not contain %q", reStr, prefix)
			}
		}
	})
}
