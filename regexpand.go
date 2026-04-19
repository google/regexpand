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

// Package regexpand provides a way to expand a regex into a list of strings that it matches.
package regexpand

import (
	"errors"
	"fmt"
	"regexp/syntax"
	"slices"
	"strings"
	"unicode"
)

// ErrorInfinite is returned when the regex matches an infinite number of strings.
var ErrorInfinite = errors.New("infinite cardinality")

// ErrorLargeResult is returned when the regex would result in a large number of strings.
var ErrorLargeResult = errors.New("large cardinality")

// ErrorUnsupported is returned when the regex is not supported.
//
// In practice this can happen when:
//   - a regex contains an alternate with mismatching anchors, such as (a$|b).
//     On the other hand, (a$|b$) and (a|b)$ are supported.
//   - a regex contains begin of words or end of words anchors such as \b or \B.
var ErrorUnsupported = errors.New(`regex unsupported`)

var errNotFinalQuest = fmt.Errorf("different end anchor in alternate : %w", ErrorUnsupported)

// maxLenCap is the maximum number of strings that can be returned by Expand.
const maxLenCap = 1 << 10

var errorInternal = errors.New("internal error")

// Expand takes a regex and returns a list of strings that the regex matches.
//
// Expand operates in O(len(re)*maxLen) time, thus it fails if the regex is too complex and returns
// ErrorUnsupported.
// If the number of strings is larger than maxLen, Expand returns ErrorLargeResult.
// If the regex matches an infinite number of strings, Expand returns ErrorInfinite.
// If the regex matches an invalid rune, it is substituted with RuneInvalid.
func Expand(regex string, maxLen int) ([]string, error) {
	if maxLen < 0 {
		return nil, ErrorLargeResult
	}
	maxLen = min(maxLen, maxLenCap)

	re, err := syntax.Parse(regex, syntax.Perl)
	if err != nil {
		return nil, err
	}
	re = re.Simplify() // removes syntax.OpRepeat

	res, _, err := expandRec(re, location{atBegin: triTrue}, maxLen)
	if len(res) > maxLen {
		// Some operations defer the maxLen check or accept some imprecision.
		// This check prevents clients code from depending on this imprecision.
		return nil, ErrorLargeResult
	}
	return cordsToStrings(res), err
}

// trinary is either unknown, false, or true.
type trinary int8

const (
	triUnknown trinary = iota
	triFalse
	triTrue
)

// location describes where expandRec is in a regex.
type location struct {
	atBegin trinary
	// requireEnd indicates that returned cords can be discarded unless we're at the very end.
	requireEnd bool
	// inFinalQuest indicates that errNotFinalQuest must be returned unless we're at the very end.
	//
	// See expandQuest for more information.
	inFinalQuest bool
}

// locUnknown is used as a placeholder for locations that are not relevant.
var locUnknown = location{}

// expandRec is the recursive implementation of Expand.
func expandRec(re *syntax.Regexp, loc location, maxLen int) ([]cord, location, error) {
	if maxLen <= 0 {
		return nil, locUnknown, ErrorLargeResult
	}

	switch re.Op {
	case syntax.OpNoMatch:
		return nil, loc, nil
	case syntax.OpEmptyMatch:
		return []cord{emptyCord}, loc, nil
	case syntax.OpLiteral:
		return expandLiteral(re, loc, maxLen)
	case syntax.OpCharClass:
		return expandCharClass(re, loc, maxLen)
	case syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return nil, locUnknown, ErrorInfinite
	case syntax.OpBeginLine, syntax.OpBeginText:
		switch loc.atBegin {
		case triFalse:
			return nil, loc, nil
		case triTrue:
			return []cord{emptyCord}, loc, nil
		default:
			return nil, locUnknown, ErrorUnsupported
		}
	case syntax.OpEndLine, syntax.OpEndText:
		loc.requireEnd = true
		return []cord{emptyCord}, loc, nil
	case syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return nil, locUnknown, ErrorUnsupported
	case syntax.OpCapture:
		if len(re.Sub) != 1 {
			return nil, locUnknown, fmt.Errorf("capturing op with %d children : %w", len(re.Sub), errorInternal)
		}
		return expandRec(re.Sub[0], loc, maxLen)
	case syntax.OpStar, syntax.OpPlus:
		return nil, locUnknown, ErrorInfinite
	case syntax.OpQuest:
		return expandQuest(re, loc, maxLen)
	case syntax.OpRepeat:
		// We expect the regex to be simplified before calling visitRe.
		return nil, locUnknown, fmt.Errorf("unexpected repetitions : %w", errorInternal)
	case syntax.OpConcat:
		return expandConcat(re, loc, maxLen)
	case syntax.OpAlternate:
		return expandAlternate(re, loc, maxLen)
	default:
		return nil, locUnknown, fmt.Errorf("unsupported regex op: %v: %w", re.Op, errorInternal)
	}
}

// expandLiteral assumes re.Op is syntax.OpLiteral.
func expandLiteral(re *syntax.Regexp, loc location, maxLen int) ([]cord, location, error) {
	if len(re.Rune) > 0 {
		loc.atBegin = triFalse
	}
	if re.Flags&syntax.FoldCase == 0 {
		return []cord{newCord(string(re.Rune))}, loc, nil
	}
	cords, err := allCases(string(re.Rune), maxLen)
	return cords, loc, err
}

// expandConcat assumes re.Op is syntax.OpConcat.
func expandConcat(re *syntax.Regexp, loc location, maxLen int) ([]cord, location, error) {
	var res []cord
	var prevFinalQuest bool
	for _, subRe := range re.Sub {
		subCords, subLoc, err := expandRec(subRe, loc, maxLen-len(res))
		if err != nil {
			return nil, locUnknown, err
		}
		if loc.requireEnd {
			// non-empty alternatives can be ignored.
			subCords = keepOnlyEmpty(subCords)
		}
		if prevFinalQuest && !allCordsEmpty(subCords) {
			return nil, locUnknown, errNotFinalQuest
		} else if subLoc.inFinalQuest {
			prevFinalQuest = true
		}
		loc = subLoc
		if len(subCords) == 0 {
			return nil, subLoc, nil // concatenating with OpNoMatch or equivalent means no matches.
		}
		if len(res) == 0 {
			res = subCords
			continue
		}
		if len(res)*len(subCords) > maxLen {
			return nil, locUnknown, ErrorLargeResult
		}

		newRes := make([]cord, 0, len(res)*len(subCords))
		for _, s1 := range res {
			for _, s2 := range subCords {
				joined := s1.JoinedWith(s2)
				newRes = append(newRes, joined)
			}
		}
		res = newRes
	}
	loc.inFinalQuest = prevFinalQuest
	return res, loc, nil
}

// expandCharClass assumes re.Op is syntax.OpCharClass.
func expandCharClass(re *syntax.Regexp, loc location, maxLen int) ([]cord, location, error) {
	if len(re.Rune) == 0 {
		return nil, loc, nil // length 0 char class matches nothing.
	}
	if len(re.Rune)%2 != 0 {
		return nil, locUnknown, fmt.Errorf("char class has odd length: %w", errorInternal)
	}
	foldCase := re.Flags&syntax.FoldCase != 0
	var res []cord
	for i := 0; i < len(re.Rune); i += 2 {
		lo, hi := re.Rune[i], re.Rune[i+1]
		for r := lo; r <= hi; r++ {
			res = append(res, newRuneCord(r))
			if foldCase {
				fold := r
				for {
					fold = unicode.SimpleFold(fold)
					if fold == r {
						break
					}
					// We check for maxLen later, there are only so few possible folds.
					res = append(res, newRuneCord(fold))
				}
			}
			if len(res) > maxLen {
				return nil, locUnknown, ErrorLargeResult
			}
		}
	}
	if len(res) > 0 { // all cords in res are nonempty.
		loc.atBegin = triFalse
	}
	return res, loc, nil
}

// expandQuest assumes re.Op is syntax.OpQuest.
func expandQuest(re *syntax.Regexp, loc location, maxLen int) ([]cord, location, error) {
	if len(re.Sub) != 1 {
		return nil, locUnknown, fmt.Errorf("quest op with %d children : %w", len(re.Sub), errorInternal)
	}
	children, childrenLoc, err := expandRec(re.Sub[0], loc, maxLen-1)
	if err != nil {
		return nil, locUnknown, err
	}
	if loc.atBegin != childrenLoc.atBegin {
		// Depending on whether we follow the quest or not, we'll be at the beginning or not.
		// To handle this, we'd need to backtrack.
		loc.atBegin = triUnknown
	}
	if childrenLoc.requireEnd {
		// Depending on whether we follow the quest or not, we'll be at the end or not.
		// To handle this, we'd need to backtrack.
		// Having the quest at the end is a notable exception.
		loc.inFinalQuest = true
	} else if childrenLoc.inFinalQuest {
		// Propagate the finalQuestErr constraint upward.
		loc.inFinalQuest = childrenLoc.inFinalQuest
	}
	return append(children, emptyCord), loc, nil
}

// expandAlternate assumes re.Op is syntax.OpAlternate.
func expandAlternate(re *syntax.Regexp, loc location, maxLen int) ([]cord, location, error) {
	resLoc := loc
	res := make([]cord, 0, len(re.Sub))
	for n, subRe := range re.Sub {
		subCords, subLoc, err := expandRec(subRe, loc, maxLen-len(res))
		if err != nil {
			return nil, locUnknown, err
		}
		if loc.requireEnd {
			subCords = keepOnlyEmpty(subCords)
		}
		if n == 0 {
			resLoc.requireEnd = subLoc.requireEnd
			resLoc.inFinalQuest = subLoc.inFinalQuest
		} else if resLoc.requireEnd != subLoc.requireEnd || subLoc.inFinalQuest != resLoc.inFinalQuest {
			return nil, locUnknown, fmt.Errorf("different end anchor in alternate : %w", ErrorUnsupported)
		}
		if resLoc.atBegin != subLoc.atBegin {
			// Depending on whether we follow the alternate or not, we'll be at the beginning or not.
			// To handle this, we'd need to backtrack.
			resLoc.atBegin = triUnknown
		}
		res = append(res, subCords...)
	}
	return res, resLoc, nil
}

// ASCIISet is a set of ASCII characters.
type ASCIISet struct {
	hi, lo uint64
	str    string
}

// Contains reports whether the set contains the given rune.
func (s *ASCIISet) Contains(r rune) bool {
	if r < 0 || r > unicode.MaxASCII {
		return false
	}
	return s.contains(uint(r))
}

func (s *ASCIISet) set(r uint) {
	// Avoid branching, instead rely on underflow to produce a left shift to yields 0.
	s.hi |= 1 << (r - 64) // Underflow intentional.
	s.lo |= 1 << (r)
}

func (s *ASCIISet) contains(r uint) bool {
	// Avoid branching, instead rely on underflow to produce a left shift to yields 0.
	hi := s.hi & (1 << (r - 64))
	lo := s.lo & (1 << r)
	return hi+lo != 0
}

// appendRune appends the escaped representation of r onto dst.
//
// It is a helper function for ASCIISet.String(), thus it only handles r<=unicode.MaxASCII.
func appendRune(dst []byte, r byte) []byte {
	const hex = "0123456789abcdef"

	// Handle non-graphic characters, such as " " or \t.
	if r < ' ' {
		return append(dst, '\\', 'x', '0', hex[byte(r)])
	}

	switch r {
	case unicode.MaxASCII:
		return append(dst, `\x7f`...)
	case '-', '\\', '[', ']':
		dst = append(dst, '\\') // Need escaping.
	}
	return append(dst, r)
}

func (s *ASCIISet) String() string {
	if len(s.str) != 0 {
		return s.str
	}

	const notInRange = unicode.MaxASCII + 1
	res := []byte{'['}
	rangeStart := uint(notInRange)
	for i := uint(0); i <= unicode.MaxASCII; i++ {
		if s.contains(uint(i)) {
			if rangeStart == notInRange {
				rangeStart = i
				res = appendRune(res, byte(i))
			}
		} else if rangeStart != notInRange {
			if rangeStart != i-1 {
				res = append(res, '-')
				// $i cannot be 0 here because during the first loop, rangeStart==notInRange.
				res = appendRune(res, byte(i-1))
			}
			rangeStart = notInRange
		}
	}
	if rangeStart != notInRange && rangeStart != unicode.MaxASCII {
		// The range includes unicode.MaxASCII.
		res = append(res, '-')
		res = appendRune(res, unicode.MaxASCII)
	}
	res = append(res, ']')
	s.str = string(res)
	return s.str
}

func (s *ASCIISet) add(r rune, foldCase bool) {
	s.set(uint(r))
	if foldCase {
		if 'a' <= r && r <= 'z' {
			r = r - 'a' + 'A'
		} else if 'A' <= r && r <= 'Z' {
			r = r - 'A' + 'a'
		} else {
			return
		}
		s.set(uint(r))
	}
}

// ASCIIRange returns the set of ASCII characters that are matched by the given regex.
//
// If the regex contains non-ASCII characters, ASCIIRange returns false.
func ASCIIRange(regex string) (res *ASCIISet, ok bool) {
	re, err := syntax.Parse(regex, syntax.Perl)
	if err != nil {
		return nil, false
	}

	res = new(ASCIISet)
	open := []*syntax.Regexp{re}
	for len(open) > 0 {
		re := open[len(open)-1]
		open = open[:len(open)-1]
		foldCase := re.Flags&syntax.FoldCase != 0
		switch re.Op {
		case syntax.OpNoMatch, syntax.OpEmptyMatch,
			syntax.OpBeginLine, syntax.OpBeginText,
			syntax.OpEndText, syntax.OpWordBoundary,
			syntax.OpNoWordBoundary, syntax.OpEndLine:
			continue
		case syntax.OpLiteral:
			for _, r := range re.Rune {
				if r > unicode.MaxASCII {
					return nil, false
				}
				res.add(r, foldCase)
			}
		case syntax.OpCharClass:
			for i := 0; i < len(re.Rune); i += 2 {
				lo, hi := re.Rune[i], re.Rune[i+1]
				for r := lo; r <= hi; r++ {
					if r > unicode.MaxASCII {
						return nil, false
					}
					res.add(r, foldCase)
				}
			}
		case syntax.OpAnyCharNotNL, syntax.OpAnyChar:
			return nil, false
		case syntax.OpCapture, syntax.OpStar,
			syntax.OpPlus, syntax.OpQuest,
			syntax.OpRepeat, syntax.OpConcat,
			syntax.OpAlternate:
			open = append(open, re.Sub...)
		default:
			return nil, false
		}
	}
	return res, true
}

// allCases generates all possible case permutations of a given string,
func allCases(s string, maxLen int) ([]cord, error) {
	orig := []rune(s)
	buf := append([]rune(nil), orig...)
	res := make([]cord, 1, maxLen)
	res[0] = newCord(s)
	for {
		if done := nextCase(buf, orig); done {
			break
		}
		res = append(res, newCord(string(buf)))
		if len(res) > maxLen {
			return nil, ErrorLargeResult
		}
	}
	return res, nil
}

// nextCase turns "abcd" into "Abcd", "Abcd" into "aBcd", "aBcd" into "ABcd" and so forth.
//
// It returns true with the last possible case ("ABCD").
// inc is the buffer to modify, orig is the initial case (eg: "abcd").
func nextCase(buf, orig []rune) (done bool) {
	for i, r := range buf {
		rNext := unicode.SimpleFold(r)
		if rNext == orig[i] {
			continue // Cannot increment buf[i], try i+1.
		}
		copy(buf, orig[:i])
		buf[i] = rNext
		return false
	}
	return true
}

var emptyCord = cord{}

// cord is a string-like immutable object that can be efficiently joined with other cords.
//
// It allows the asymptotic complexity of Expand to remain in O(len(re)*maxLen).
// Details:
//   - expandConcat performs O(n*m) concatenations, where n is the number of expansions
//     concatenated and m the average number of alternatives in each expansion.
//   - Thus expandConcat ∈ O(n*m) * ( O(cord.JoinedWith)) + O(cord.String) ).
//   - cord allows JoinedWith() to be in O(1) amortised time, String() to be in O(len(re)) time.
//   - Furthermore, O(n*m)=O(maxLen).
//   - Therefore expandConcat is in O(len(re)*maxLen).
//   - Other operations are in O(1) or in O(len(re)).
type cord struct {
	ss  []string  // cannot contain empty strings.
	ss0 [1]string // for efficient creation of cords with 1 string (common case)
}

func newCord(s string) cord {
	if len(s) == 0 {
		return emptyCord // ss cannot contain empty strings.
	}
	c := cord{ss0: [1]string{s}}
	c.ss = c.ss0[:]
	return c
}

func newRuneCord(r rune) cord { return newCord(string(r)) }

// Empty reports whether the cord represents the empty string.
func (c cord) Empty() bool { return len(c.ss) == 0 }

func (c cord) JoinedWith(c2 cord) cord {
	if c2.Empty() {
		return c // so that we don't allocate a new cord.
	}
	res := make([]string, len(c.ss)+len(c2.ss))
	copy(res, c.ss)
	copy(res[len(c.ss):], c2.ss)
	return cord{ss: res}
}

func (c cord) String() string {
	if len(c.ss) == 1 {
		return c.ss[0]
	}
	return strings.Join(c.ss, "")
}

func cordsToStrings(cs []cord) []string {
	if len(cs) == 0 {
		return nil
	}

	sorted := make([]string, len(cs))
	for n, c := range cs {
		sorted[n] = c.String()
	}
	slices.Sort(sorted)

	res := sorted[:1]
	prev := res[0]
	for _, v := range sorted[1:] {
		if v != prev {
			// Relies on res to have enough capacity to store sorted.
			res = append(res, v)
		}
		prev = v
	}
	return res
}

func allCordsEmpty(cs []cord) bool {
	for _, c := range cs {
		if !c.Empty() {
			return false
		}
	}
	return true
}

func keepOnlyEmpty(cs []cord) []cord {
	for _, c := range cs {
		if c.Empty() {
			return []cord{emptyCord}
		}
	}
	return nil
}
