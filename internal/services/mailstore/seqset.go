// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Posta.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailstore

import (
	"fmt"
	"strconv"
	"strings"
)

// IMAP sequence-set semantics (RFC 9051 §4.1.2.6, OB-4 wave W1): the numeric
// grammar shared by FETCH, SEARCH and STORE. The wire syntax ("2:4,7,9:*") is
// parsed here, once, and both servers consume the parsed form; `*` is carried
// as the sentinel 0 and resolved against a caller-supplied maximum so the
// same SeqSet works in sequence-number mode (max = message count) and UID
// mode (max = highest UID, or UIDNEXT-1 on an empty folder).
//
// RFC quirk kept deliberately (RFC 9051 §4.1.2.6): a range ending in `*`
// ("559:*") ALWAYS includes the maximum value even when the range's fixed end
// exceeds it, so "*:*" and "n:*" on a folder whose max is below n still match
// the last message.

// maxIMAPNumber is the numeric ceiling of the grammar (32-bit UID space).
const maxIMAPNumber = uint32(4294967295)

// seqSetWildcard is the sentinel SeqRange value standing for "*". It resolves
// to the caller's max at match time.
const seqSetWildcard = uint32(0)

// SeqRange is one inclusive range of a sequence set. Start > End encodes a
// reversed pair ("5:2") and still matches [End, Start].
type SeqRange struct {
	Start uint32
	End   uint32
}

// SeqSet is a parsed IMAP sequence-set, kept in wire order (order is not
// significant for matching).
type SeqSet []SeqRange

// ParseSeqSet parses the IMAP sequence-set grammar:
//
//	seq-set      = *(seq-range ",") seq-range
//	seq-range    = seq-num ":" seq-num / seq-num
//	seq-num      = nz-number / "*"
//
// Empty sets and malformed tokens are errors. Reversed ranges are preserved
// (matching normalizes them).
func ParseSeqSet(s string) (SeqSet, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("mailstore: empty sequence set")
	}
	var set SeqSet
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			return nil, fmt.Errorf("mailstore: empty element in sequence set %q", s)
		}
		a, b, isRange, err := parseSeqRangeToken(tok, s)
		if err != nil {
			return nil, err
		}
		if !isRange {
			set = append(set, SeqRange{Start: a, End: a})
			continue
		}
		set = append(set, SeqRange{Start: a, End: b})
	}
	return set, nil
}

// parseSeqRangeToken parses one comma element: "n", "*", "n:m", "n:*", "*:m".
func parseSeqRangeToken(tok, whole string) (a, b uint32, isRange bool, err error) {
	num := func(t string) (uint32, error) {
		if t == "*" {
			return seqSetWildcard, nil
		}
		v, perr := strconv.ParseUint(t, 10, 64)
		if perr != nil || v == 0 || v > uint64(maxIMAPNumber) {
			// 0 is not a valid IMAP sequence number (nz-number) and out-of-range
			// values overflow the 32-bit UID space.
			return 0, fmt.Errorf("mailstore: invalid sequence number %q in set %q", t, whole)
		}
		return uint32(v), nil
	}
	if i := strings.Index(tok, ":"); i >= 0 {
		left, right := tok[:i], tok[i+1:]
		if left == "" || right == "" {
			return 0, 0, false, fmt.Errorf("mailstore: empty range endpoint in set %q", whole)
		}
		a, err = num(left)
		if err != nil {
			return 0, 0, false, err
		}
		b, err = num(right)
		if err != nil {
			return 0, 0, false, err
		}
		return a, b, true, nil
	}
	a, err = num(tok)
	if err != nil {
		return 0, 0, false, err
	}
	return a, a, false, nil
}

// Resolve substitutes the wildcard with max on every range. RFC 9051 §4.1.2.6:
// a range whose right endpoint is "*" always includes max, even when the left
// endpoint is larger than max — normalizing the reversed pair achieves exactly
// that (e.g. 559:* on a folder whose top UID is 12 becomes {12, 559}).
func (s SeqSet) Resolve(max uint32) SeqSet {
	out := make(SeqSet, 0, len(s))
	for _, r := range s {
		if r.Start == seqSetWildcard {
			r.Start = max
		}
		if r.End == seqSetWildcard {
			r.End = max
		}
		if r.Start > r.End {
			r.Start, r.End = r.End, r.Start
		}
		out = append(out, r)
	}
	return out
}

// Contains reports whether n is inside the resolved set.
func (s SeqSet) Contains(n uint32) bool {
	for _, r := range s {
		if n >= r.Start && n <= r.End {
			return true
		}
	}
	return false
}

// ContainsWildcard reports whether the set references "*".
func (s SeqSet) ContainsWildcard() bool {
	for _, r := range s {
		if r.Start == seqSetWildcard || r.End == seqSetWildcard {
			return true
		}
	}
	return false
}

// MaxUID returns the wildcard ceiling used in UID mode (RFC 9051 §2.3.1.2):
// the UID of the last message in the mailbox, or — when the mailbox is empty —
// UIDNEXT minus 1 (falling back to 1 so an empty folder's "*" still resolves
// to something sane rather than 0).
func resolveUIDWildcardMax(maxUID, uidNext uint32) uint32 {
	if maxUID > 0 {
		return maxUID
	}
	if uidNext > 1 {
		return uidNext - 1
	}
	return 1
}
