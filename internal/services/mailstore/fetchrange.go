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
	"context"
	"errors"
	"fmt"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// FetchRange (OB-4 wave W1, ADR-004 decision 1): sequence-set and UID-set
// fetching with BODY section extraction, the seam both mail-protocol servers
// build on. Sequence numbers are positions in the folder's UID-ascending
// order (RFC 9051 §2.3.1.2); UIDs are the stored per-folder monotonic values.
//
// Ownership scoping matches every other Service entry point: the folder must
// resolve for (userID, workspaceID) or the call fails with the typed errors.
//
// Mechanism: one transaction resolves the folder (ownership) and takes a
// light (id, uid) scan — ordered by UID — that provides the sequence mapping
// and the wildcard maximum; the requested rows are then loaded by ID
// (bounded by the set, not the folder); body sections are located in the
// stored raw bytes (mimesection.go) and \Seen side effects are applied
// through the existing CAS flag store, one small transaction each.

// MaxFetchBatchMessages bounds a single FetchRange call. Protocol rate limits
// (the servers' layer) bound request rates; this bounds one request's blast
// radius so a "1:* BODY[]" against a huge folder cannot pin the store.
const MaxFetchBatchMessages = 10000

// ErrFetchRangeTooLarge is returned when a fetch set resolves to more than
// MaxFetchBatchMessages messages.
var ErrFetchRangeTooLarge = errors.New("mailstore: fetch range exceeds the per-request message cap")

// BodySection selects one BODY[...] section (plus optional partial window and
// the BODY.PEEK marker).
type BodySection struct {
	// Spec is the section specifier text: "", "HEADER", "TEXT", "1", "1.2",
	// "2.HEADER", "2.MIME", "HEADER.FIELDS", "HEADER.FIELDS.NOT".
	Spec string
	// Fields carries the HEADER.FIELDS field list (case-insensitive match).
	Fields []string
	// Not flips HEADER.FIELDS to its complement (HEADER.FIELDS.NOT).
	Not bool
	// Peek = BODY.PEEK: do not set \Seen as a side effect.
	Peek bool
	// Partial enables the <start.count> window over the section bytes (the
	// FETCH BODY[...]<partial> form). Explicit rather than sentinel-derived:
	// the zero value must mean "no partial", not "<0.0>".
	Partial bool
	// PartialOffset is the window start; PartialLength >= 0 takes
	// [Offset, Offset+Length) of the section bytes, Length < 0 takes to the
	// end.
	PartialOffset int64
	PartialLength int64
}

// FetchItems selects the per-message work a FetchRange performs. The message
// row always rides the result; these control blob reads, structure walks and
// the \Seen side effect.
type FetchItems struct {
	// Body requests one BODY section (nil = no blob read).
	Body *BodySection
	// Structure requests the MIME structure tree (BuildMimeStructure).
	Structure bool
}

// FetchRangeSpec is one fetch request.
type FetchRangeSpec struct {
	FolderID    uint
	UserID      uint
	WorkspaceID uint
	// UIDMode selects UID-set semantics; false = sequence-number set.
	UIDMode bool
	// Set is the parsed sequence set (wildcards resolve against the folder).
	Set SeqSet
	// Items selects the per-message work.
	Items FetchItems
}

// FetchRangeResult is one fetched message.
type FetchRangeResult struct {
	MessageID uint
	UID       uint32
	Seq       uint32 // position in UID order, 1-based (always computed)
	Message   *models.MailMessage
	// Section is the requested BODY section bytes (nil when none requested or
	// the section does not exist — an empty section is a legal IMAP response).
	Section []byte
	// SectionFound distinguishes "no section requested" from "requested and
	// found"; a missing section returns Section == nil, SectionFound == false.
	SectionFound bool
	// Structure is the MIME structure tree when Items.Structure was set.
	Structure *MimePart
}

// fetchPosition is the light two-column scan row used for seq mapping and
// wildcard resolution without loading full rows.
type fetchPosition struct {
	ID  uint   `gorm:"column:id"`
	UID uint32 `gorm:"column:uid"`
}

// FetchRange executes a sequence-set / UID-set fetch.
func (s *Service) FetchRange(ctx context.Context, spec FetchRangeSpec) ([]FetchRangeResult, error) {
	if len(spec.Set) == 0 {
		return nil, errors.New("mailstore: fetch range with empty sequence set")
	}
	// Ownership + position scan.
	var positions []fetchPosition
	var uidNext uint32
	err := s.db.Transaction(func(tx *gorm.DB) error {
		f, _, err := s.resolveFolder(tx, spec.FolderID, spec.UserID, spec.WorkspaceID)
		if err != nil {
			return err
		}
		uidNext = f.UIDNext
		if err := tx.Model(&models.MailMessage{}).
			Select("id", "uid").
			Where("folder_id = ?", f.ID).
			Order("uid ASC").
			Scan(&positions).Error; err != nil {
			return fmt.Errorf("fetch scan of folder %d: %w", f.ID, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	matches := matchFetchSet(spec, positions, uidNext)
	if len(matches) > MaxFetchBatchMessages {
		return nil, ErrFetchRangeTooLarge
	}
	if len(matches) == 0 {
		return []FetchRangeResult{}, nil
	}

	ids := make([]uint, 0, len(matches))
	for _, p := range matches {
		ids = append(ids, p.ID)
	}
	var rows []models.MailMessage
	if err := s.db.Where("id IN ?", ids).Order("uid ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load %d fetch messages: %w", len(ids), err)
	}

	out := make([]FetchRangeResult, 0, len(rows))
	for i := range rows {
		m := &rows[i]
		res := FetchRangeResult{
			MessageID: m.ID,
			UID:       m.UID,
			Seq:       seqOfUID(positions, m.UID),
			Message:   m,
		}
		if spec.Items.Body != nil {
			raw, err := s.readBlob(ctx, m.BodyBlobRef)
			if err != nil {
				return nil, fmt.Errorf("fetch body of message %d: %w", m.ID, err)
			}
			if sec, ok := locateSection(raw, parseFetchSection(*spec.Items.Body)); ok {
				res.Section = sec
				if spec.Items.Body.Partial {
					res.Section = applyPartial(sec, spec.Items.Body.PartialOffset, spec.Items.Body.PartialLength)
				}
				res.SectionFound = true
			}
			// \Seen side effect for non-peek body fetches (RFC 9051 §4.3.5.1):
			// a BODY[] fetch implicitly sets \Seen; BODY.PEEK never does.
			if !spec.Items.Body.Peek && m.Flags&models.FlagSeen == 0 {
				if _, err := s.SetFlags(spec.UserID, spec.WorkspaceID, FlagsUpdate{
					MessageID: m.ID,
					Set:       models.FlagSeen,
				}); err != nil {
					return nil, fmt.Errorf("mark message %d seen: %w", m.ID, err)
				}
				m.Flags |= models.FlagSeen // keep the returned row truthful
			}
		}
		if spec.Items.Structure {
			raw, err := s.readBlob(ctx, m.BodyBlobRef)
			if err != nil {
				return nil, fmt.Errorf("fetch structure of message %d: %w", m.ID, err)
			}
			res.Structure = BuildMimeStructure(raw)
		}
		out = append(out, res)
	}
	return out, nil
}

// matchFetchSet maps the parsed set onto the position scan. UID mode matches
// stored UIDs (wildcard max = highest UID, or UIDNEXT-1 on an empty folder);
// sequence mode matches 1-based positions in UID order (wildcard max = count).
func matchFetchSet(spec FetchRangeSpec, positions []fetchPosition, uidNext uint32) []fetchPosition {
	if spec.UIDMode {
		maxUID := resolveUIDWildcardMax(lastUID(positions), uidNext)
		resolved := spec.Set.Resolve(maxUID)
		var out []fetchPosition
		for _, p := range positions {
			if resolved.Contains(p.UID) {
				out = append(out, p)
			}
		}
		return out
	}
	resolved := spec.Set.Resolve(uint32(len(positions)))
	var out []fetchPosition
	for i, p := range positions {
		if resolved.Contains(uint32(i + 1)) {
			out = append(out, p)
		}
	}
	return out
}

// seqOfUID returns the 1-based position of uid in the folder's UID order.
func seqOfUID(positions []fetchPosition, uid uint32) uint32 {
	for i, p := range positions {
		if p.UID == uid {
			return uint32(i + 1)
		}
	}
	return 0
}

func lastUID(positions []fetchPosition) uint32 {
	if len(positions) == 0 {
		return 0
	}
	return positions[len(positions)-1].UID
}

// parseFetchSection converts the caller-facing BodySection into the internal
// sectionSpec (an unknown specifier yields an empty section, never an error).
func parseFetchSection(b BodySection) sectionSpec {
	spec, ok := parseSectionSpec(b.Spec)
	if !ok {
		return sectionSpec{}
	}
	spec.Fields = b.Fields
	spec.FieldsNot = b.Not
	return spec
}

// applyPartial cuts the partial window <start.count> out of the section bytes.
func applyPartial(b []byte, offset, length int64) []byte {
	if offset < 0 {
		return b
	}
	if offset >= int64(len(b)) {
		return nil
	}
	if length < 0 || offset+length > int64(len(b)) {
		return b[offset:]
	}
	return b[offset : offset+length]
}
