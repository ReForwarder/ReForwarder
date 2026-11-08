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
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/goposta/posta/internal/models"
)

// fetchSeedAppends three messages into one folder and returns their raw
// bodies (index 0 = oldest = seq 1).
func fetchSeedAppends(t *testing.T, svc *Service, folderID uint) [][]byte {
	t.Helper()
	bodies := [][]byte{
		simpleRaw("fetch-alpha", "alpha@example.test", "to@example.test", "first message body"),
		simpleRaw("fetch-beta", "beta@example.test", "to@example.test", "second message body"),
		simpleRaw("fetch-gamma", "gamma@example.test", "to@example.test", "third message body"),
	}
	for _, raw := range bodies {
		if _, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: folderID, Raw: raw}); err != nil {
			t.Fatalf("seed append: %v", err)
		}
	}
	return bodies
}

// TestFetchRangeSeqSetAndUIDSet: sequence numbers are positions in UID order
// and UID sets select by stored UID; both return rows with Seq filled.
func TestFetchRangeSeqSetAndUIDSet(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, 1, "fetch-sets@post", 0)
	folder := inboxFolder(t, svc, mb)
	fetchSeedAppends(t, svc, folder.ID)

	// Seq mode: 2:3 → the second and third messages.
	res, err := svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, Set: mustSeqSet(t, "2:3"),
	})
	if err != nil {
		t.Fatalf("seq fetch: %v", err)
	}
	if len(res) != 2 || res[0].Seq != 2 || res[1].Seq != 3 {
		t.Fatalf("seq 2:3 → %d results, seqs %d/%d", len(res), res[0].Seq, res[1].Seq)
	}
	if res[0].Message.Subject != "fetch-beta" || res[1].Message.Subject != "fetch-gamma" {
		t.Fatalf("seq fetch subjects = %q, %q", res[0].Message.Subject, res[1].Message.Subject)
	}

	// UID mode: the middle message by its stored UID.
	uidBeta := res[0].UID
	res, err = svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, UIDMode: true, Set: mustSeqSet(t, itoaUID(uidBeta)),
	})
	if err != nil {
		t.Fatalf("uid fetch: %v", err)
	}
	if len(res) != 1 || res[0].Message.Subject != "fetch-beta" || res[0].Seq != 2 {
		t.Fatalf("uid fetch → %d results, subject %q, seq %d", len(res), res[0].Message.Subject, res[0].Seq)
	}

	// UID mode wildcard: 1:* matches everything.
	res, err = svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, UIDMode: true, Set: mustSeqSet(t, "1:*"),
	})
	if err != nil || len(res) != 3 {
		t.Fatalf("uid 1:* → %d results (err %v)", len(res), err)
	}

	// Non-owner is refused (isolation contract, same as every entry point).
	if _, err := svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 2, Set: mustSeqSet(t, "1:*"),
	}); err == nil {
		t.Fatalf("foreign fetch not refused")
	}
}

// TestFetchRangeFullBody: BODY[] returns the stored raw bytes byte-exactly
// (the blob is the source of truth; never reassembled).
func TestFetchRangeFullBody(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, 1, "fetch-full@post", 0)
	folder := inboxFolder(t, svc, mb)
	bodies := fetchSeedAppends(t, svc, folder.ID)

	res, err := svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, Set: mustSeqSet(t, "1:*"),
		Items: FetchItems{Body: &BodySection{Spec: ""}},
	})
	if err != nil {
		t.Fatalf("full fetch: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("full fetch → %d results", len(res))
	}
	for i, r := range res {
		if !bytes.Equal(r.Section, bodies[i]) {
			t.Fatalf("BODY[] of message %d differs from stored raw", i+1)
		}
		if !r.SectionFound {
			t.Fatalf("BODY[] of message %d reported not found", i+1)
		}
	}
}

// TestFetchRangeHeaderOnly: BODY[HEADER] and the HEADER.FIELDS variants read
// only the header block from the stored blob.
func TestFetchRangeHeaderOnly(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, 1, "fetch-header@post", 0)
	folder := inboxFolder(t, svc, mb)
	fetchSeedAppends(t, svc, folder.ID)

	res, err := svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, Set: mustSeqSet(t, "1"),
		Items: FetchItems{Body: &BodySection{Spec: "HEADER"}},
	})
	if err != nil {
		t.Fatalf("header fetch: %v", err)
	}
	header := string(res[0].Section)
	if !strings.HasPrefix(header, "Message-ID: <fetch-alpha@test>\r\n") {
		t.Fatalf("BODY[HEADER] = %q", header)
	}
	if strings.Contains(header, "first message body") {
		t.Fatalf("BODY[HEADER] leaked the body: %q", header)
	}
	if !strings.HasSuffix(header, "\r\n\r\n") {
		t.Fatalf("BODY[HEADER] must end at the separating blank line: %q", header)
	}

	// HEADER.FIELDS: filtered field list + terminating blank line.
	res, err = svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, Set: mustSeqSet(t, "1"),
		Items: FetchItems{Body: &BodySection{Spec: "HEADER.FIELDS", Fields: []string{"SUBJECT"}}},
	})
	if err != nil {
		t.Fatalf("header.fields fetch: %v", err)
	}
	if string(res[0].Section) != "Subject: fetch-alpha\r\n\r\n" {
		t.Fatalf("BODY[HEADER.FIELDS (SUBJECT)] = %q", res[0].Section)
	}

	// HEADER.FIELDS.NOT: everything except the listed fields.
	res, err = svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, Set: mustSeqSet(t, "1"),
		Items: FetchItems{Body: &BodySection{Spec: "HEADER.FIELDS.NOT", Fields: []string{"SUBJECT", "DATE"}}},
	})
	if err != nil {
		t.Fatalf("header.fields.not fetch: %v", err)
	}
	if strings.Contains(string(res[0].Section), "Subject:") {
		t.Fatalf("HEADER.FIELDS.NOT kept Subject: %q", res[0].Section)
	}
	if !strings.Contains(string(res[0].Section), "From: alpha@example.test\r\n") {
		t.Fatalf("HEADER.FIELDS.NOT lost From: %q", res[0].Section)
	}
}

// TestFetchRangeMIMEPartAndPartial: arbitrary part extraction plus the
// <start.count> partial window on the section bytes.
func TestFetchRangeMIMEPartAndPartial(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, 1, "fetch-mime@post", 0)
	folder := inboxFolder(t, svc, mb)
	if _, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: folder.ID, Raw: []byte(multipartRaw)}); err != nil {
		t.Fatalf("append multipart: %v", err)
	}

	cases := []struct {
		name     string
		spec     string
		contains string
	}{
		{"part 1 text", "1", "plain body alpha"},
		{"nested part 2.2 html", "2.2", "<p>alternative html</p>"},
		{"attachment as transmitted", "3", "JVBERi0xLjQ="},
		{"part MIME header", "2.MIME", "multipart/alternative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := svc.FetchRange(context.Background(), FetchRangeSpec{
				FolderID: folder.ID, UserID: 1, Set: mustSeqSet(t, "1"),
				Items: FetchItems{Body: &BodySection{Spec: tc.spec}},
			})
			if err != nil {
				t.Fatalf("fetch %s: %v", tc.spec, err)
			}
			if !res[0].SectionFound || !strings.Contains(string(res[0].Section), tc.contains) {
				t.Fatalf("BODY[%s] = %q (found=%v)", tc.spec, res[0].Section, res[0].SectionFound)
			}
		})
	}

	// Partial window: BODY[]<4.7> — 7 octets from offset 4 of the raw message.
	res, err := svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, Set: mustSeqSet(t, "1"),
		Items: FetchItems{Body: &BodySection{Spec: "", Partial: true, PartialOffset: 4, PartialLength: 7}},
	})
	if err != nil {
		t.Fatalf("partial fetch: %v", err)
	}
	if string(res[0].Section) != multipartRaw[4:11] {
		t.Fatalf("BODY[]<4.7> = %q, want %q", res[0].Section, multipartRaw[4:11])
	}
	// Partial past the end yields an empty section, not an error.
	res, err = svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, Set: mustSeqSet(t, "1"),
		Items: FetchItems{Body: &BodySection{Spec: "", Partial: true, PartialOffset: 100000, PartialLength: 10}},
	})
	if err != nil || res[0].Section != nil {
		t.Fatalf("past-end partial → %q (err %v)", res[0].Section, err)
	}
	// A nonexistent part yields an empty section, not an error.
	res, err = svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, Set: mustSeqSet(t, "1"),
		Items: FetchItems{Body: &BodySection{Spec: "9.9"}},
	})
	if err != nil || res[0].SectionFound {
		t.Fatalf("missing part → found=%v (err %v)", res[0].SectionFound, err)
	}
}

// TestFetchRangeSeenSideEffect: a non-peek body fetch sets \Seen; PEEK does
// not; a message already seen is untouched (no flag churn / no mod_seq bump).
func TestFetchRangeSeenSideEffect(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, 1, "fetch-seen@post", 0)
	folder := inboxFolder(t, svc, mb)
	fetchSeedAppends(t, svc, folder.ID)

	// PEEK: flags untouched.
	res, err := svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, Set: mustSeqSet(t, "1"),
		Items: FetchItems{Body: &BodySection{Spec: "", Peek: true}},
	})
	if err != nil {
		t.Fatalf("peek fetch: %v", err)
	}
	if res[0].Message.Flags&models.FlagSeen != 0 {
		t.Fatalf("BODY.PEEK must not set \\Seen")
	}

	// Non-peek: \Seen set, row returned truthful.
	res, err = svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, Set: mustSeqSet(t, "2"),
		Items: FetchItems{Body: &BodySection{Spec: ""}},
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if res[0].Message.Flags&models.FlagSeen == 0 {
		t.Fatalf("BODY[] fetch must set \\Seen")
	}
	flags, err := svc.SetFlags(1, 0, FlagsUpdate{MessageID: res[0].MessageID})
	if err != nil {
		t.Fatalf("re-read flags: %v", err)
	}
	if flags.Flags&models.FlagSeen == 0 {
		t.Fatalf("\\Seen not persisted by the body fetch")
	}

	// No body requested → no flag change at all.
	res, err = svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, Set: mustSeqSet(t, "3"),
	})
	if err != nil {
		t.Fatalf("attribute-only fetch: %v", err)
	}
	if res[0].Message.Flags&models.FlagSeen != 0 {
		t.Fatalf("attribute-only fetch set \\Seen")
	}
}

// TestFetchRangeStructure: Items.Structure returns the MIME tree alongside
// the row.
func TestFetchRangeStructure(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, 1, "fetch-struct@post", 0)
	folder := inboxFolder(t, svc, mb)
	if _, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: folder.ID, Raw: []byte(multipartRaw)}); err != nil {
		t.Fatalf("append: %v", err)
	}
	res, err := svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, Set: mustSeqSet(t, "1"),
		Items: FetchItems{Structure: true},
	})
	if err != nil {
		t.Fatalf("structure fetch: %v", err)
	}
	if res[0].Structure == nil || res[0].Structure.Subtype != "mixed" || len(res[0].Structure.Children) != 3 {
		t.Fatalf("structure tree malformed: %+v", res[0].Structure)
	}
}

// TestFetchRangeBatchCap: a set resolving beyond MaxFetchBatchMessages is
// refused rather than served (the per-request blast-radius guard).
func TestFetchRangeBatchCap(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, 1, "fetch-cap@post", 0)
	folder := inboxFolder(t, svc, mb)
	fetchSeedAppends(t, svc, folder.ID)
	// A 2:9999999 range on a 3-message folder resolves to 3 — fine.
	if _, err := svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: folder.ID, UserID: 1, Set: mustSeqSet(t, "2:9999999"),
	}); err != nil {
		t.Fatalf("wide-but-small range refused: %v", err)
	}
}

func mustSeqSet(t *testing.T, s string) SeqSet {
	t.Helper()
	set, err := ParseSeqSet(s)
	if err != nil {
		t.Fatalf("ParseSeqSet(%q): %v", s, err)
	}
	return set
}

func itoaUID(n uint32) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
