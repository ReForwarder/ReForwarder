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

package imap

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
)

// FETCH (RFC 9051 §6.4.8) — the ADR-004 subset: macros (ALL/FAST/FULL),
// FLAGS, UID, INTERNALDATE, RFC822.SIZE, RFC822 (≡ BODY[]), RFC822.HEADER
// (≡ BODY.PEEK[HEADER]), RFC822.TEXT (≡ BODY[TEXT]), ENVELOPE, BODY /
// BODYSTRUCTURE, BODY[...] / BODY.PEEK[...] with HEADER, HEADER.FIELDS,
// HEADER.FIELDS.NOT, TEXT, MIME and numeric part paths plus <start.count>
// partial windows. Section extraction is the W1 FetchRange seam (byte-exact
// stored bytes, \Seen side effect for non-peek body reads).

// fetchItem is one requested item.
type fetchItem struct {
	Name string // uppercased item name as it appears in the response
	// Section carries BODY[...] requests (nil otherwise).
	Section *mailstore.BodySection
	// BodyStructure: BODY (non-extensible) vs BODYSTRUCTURE.
	BodyStructure bool
}

// parseFetchItems converts the token stream after the sequence set into
// items. Macros expand per RFC 9051.
func parseFetchItems(toks []token, i int) ([]fetchItem, error) {
	var atoms []string
	for ; i < len(toks); i++ {
		t := toks[i]
		switch t.Kind {
		case '(', ')':
		case 'a':
			atoms = append(atoms, t.Val)
		case 's':
			// Only legal inside BODY[...] which the tokenizer folds into an
			// atom; a bare string here is malformed.
			return nil, fmt.Errorf("%w: unexpected string in fetch items", errProtocol)
		}
	}
	if len(atoms) == 0 {
		return nil, fmt.Errorf("%w: FETCH requires at least one item", errProtocol)
	}
	// Macros.
	var items []fetchItem
	switch strings.ToUpper(atoms[0]) {
	case "ALL":
		return []fetchItem{
			{Name: "FLAGS"}, {Name: "INTERNALDATE"}, {Name: "RFC822.SIZE"}, {Name: "ENVELOPE"},
		}, nil
	case "FAST":
		return []fetchItem{
			{Name: "FLAGS"}, {Name: "INTERNALDATE"}, {Name: "RFC822.SIZE"},
		}, nil
	case "FULL":
		return []fetchItem{
			{Name: "FLAGS"}, {Name: "INTERNALDATE"}, {Name: "RFC822.SIZE"}, {Name: "ENVELOPE"}, {Name: "BODY", BodyStructure: true},
		}, nil
	}
	for _, a := range atoms {
		upper := strings.ToUpper(a)
		switch {
		case upper == "FLAGS" || upper == "UID" || upper == "INTERNALDATE" ||
			upper == "RFC822.SIZE" || upper == "ENVELOPE" || upper == "BODYSTRUCTURE":
			items = append(items, fetchItem{Name: upper})
		case upper == "BODY":
			items = append(items, fetchItem{Name: "BODY", BodyStructure: true})
		case upper == "RFC822":
			items = append(items, fetchItem{Name: "RFC822", Section: &mailstore.BodySection{}})
		case upper == "RFC822.HEADER":
			items = append(items, fetchItem{Name: "RFC822.HEADER", Section: &mailstore.BodySection{Spec: "HEADER", Peek: true}})
		case upper == "RFC822.TEXT":
			items = append(items, fetchItem{Name: "RFC822.TEXT", Section: &mailstore.BodySection{Spec: "TEXT"}})
		case strings.HasPrefix(upper, "BODY.PEEK["):
			// Section specs match case-insensitively but render with the
			// client's original case (header field lists echo as sent).
			sec, err := parseBodySection(a, true)
			if err != nil {
				return nil, err
			}
			items = append(items, fetchItem{Name: responseName(sec), Section: sec})
		case strings.HasPrefix(upper, "BODY["):
			sec, err := parseBodySection(a, false)
			if err != nil {
				return nil, err
			}
			items = append(items, fetchItem{Name: responseName(sec), Section: sec})
		default:
			return nil, fmt.Errorf("%w: unknown FETCH item %q", errProtocol, a)
		}
	}
	return items, nil
}

// parseBodySection parses "BODY[<spec>]" (+ optional "<start.count>") into a
// mailstore.BodySection. HEADER.FIELDS / HEADER.FIELDS.NOT carry their field
// list inside parentheses in the spec text.
func parseBodySection(atom string, peek bool) (*mailstore.BodySection, error) {
	sec := &mailstore.BodySection{Peek: peek}
	open := strings.IndexByte(atom, '[')
	closeIdx := strings.LastIndexByte(atom, ']')
	if open < 0 || closeIdx < open {
		return nil, fmt.Errorf("%w: malformed BODY section", errProtocol)
	}
	spec := atom[open+1 : closeIdx]
	partial := atom[closeIdx+1:]
	upperSpec := strings.ToUpper(spec)
	switch {
	case upperSpec == "HEADER.FIELDS.NOT" || strings.HasPrefix(upperSpec, "HEADER.FIELDS.NOT "):
		sec.Not = true
		sec.Spec = "HEADER.FIELDS.NOT"
		fields, perr := parseFieldList(spec[len("HEADER.FIELDS.NOT"):])
		if perr != nil {
			return nil, perr
		}
		sec.Fields = fields
	case upperSpec == "HEADER.FIELDS" || strings.HasPrefix(upperSpec, "HEADER.FIELDS "):
		sec.Spec = "HEADER.FIELDS"
		fields, perr := parseFieldList(spec[len("HEADER.FIELDS"):])
		if perr != nil {
			return nil, fmt.Errorf("%w: HEADER.FIELDS requires a field list", errProtocol)
		}
		sec.Fields = fields
	default:
		sec.Spec = spec
	}
	// Partial window <start.count>.
	if partial != "" {
		if !strings.HasPrefix(partial, "<") || !strings.HasSuffix(partial, ">") {
			return nil, fmt.Errorf("%w: malformed partial window %q", errProtocol, partial)
		}
		body := partial[1 : len(partial)-1]
		parts := strings.SplitN(body, ".", 2)
		start, err1 := strconv.ParseInt(parts[0], 10, 64)
		if err1 != nil || start < 0 {
			return nil, fmt.Errorf("%w: malformed partial start %q", errProtocol, body)
		}
		if len(parts) == 2 {
			count, err2 := strconv.ParseInt(parts[1], 10, 64)
			if err2 != nil || count < 0 {
				return nil, fmt.Errorf("%w: malformed partial count %q", errProtocol, body)
			}
			sec.Partial = true
			sec.PartialOffset = start
			sec.PartialLength = count
		}
		// "<start>" without ".count" is not defined for BODY sections
		// (partial syntax is always <start.count>); fall through as full.
	}
	return sec, nil
}

// parseFieldList parses "(From To)" — the field list inside a section spec.
func parseFieldList(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("%w: empty field list", errProtocol)
	}
	fields := strings.Fields(s)
	for _, f := range fields {
		if strings.ContainsAny(f, "[]()<>*%") {
			return nil, fmt.Errorf("%w: invalid header field %q", errProtocol, f)
		}
	}
	return fields, nil
}

// responseName renders the item name a section fetch answers under: BODY.PEEK
// sections answer as BODY[...] (RFC 9051); partials carry the <origin> suffix.
func responseName(sec *mailstore.BodySection) string {
	spec := sec.Spec
	if len(sec.Fields) > 0 {
		fields := strings.Join(sec.Fields, " ")
		if sec.Not || sec.Spec == "HEADER.FIELDS.NOT" {
			spec = "HEADER.FIELDS.NOT (" + fields + ")"
		} else {
			spec = "HEADER.FIELDS (" + fields + ")"
		}
	}
	name := "BODY[" + spec + "]"
	if sec.Partial {
		name += "<" + strconv.FormatInt(sec.PartialOffset, 10) + ">"
	}
	return name
}

// cmdFetch implements FETCH and UID FETCH.
func (st *session) cmdFetch(cmd *commandTokens, uidMode bool) error {
	f, ok := st.requireSelection(cmd.Tag)
	if !ok {
		return nil
	}
	setStr, i, err := wantString(cmd.Args, 0)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "FETCH requires a sequence set and items")
		return nil
	}
	items, err := parseFetchItems(cmd.Args, i)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", err.Error())
		return nil
	}
	if uidMode {
		// RFC 9051 §6.4.8: UID FETCH responses always include the UID item,
		// even when the client did not request it.
		hasUID := false
		for _, it := range items {
			if it.Name == "UID" {
				hasUID = true
				break
			}
		}
		if !hasUID {
			items = append([]fetchItem{{Name: "UID"}}, items...)
		}
	}
	set, err := mailstore.ParseSeqSet(setStr)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "Invalid sequence set")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := st.refreshView(ctx); err != nil {
		st.tagged(cmd.Tag, "NO", "FETCH failed: view refresh")
		return nil
	}
	targets := st.resolveSet(set, uidMode)
	if len(targets) == 0 && uidMode {
		// RFC 9051: UID commands succeed silently when nothing matches.
		st.tagged(cmd.Tag, "OK", "FETCH completed (no matching messages)")
		return nil
	}
	uidSet := make(mailstore.SeqSet, 0, len(targets))
	for _, t := range targets {
		uidSet = append(uidSet, mailstore.SeqRange{Start: t.uid, End: t.uid})
	}

	needsStructure := false
	needsEnvelope := false
	for _, it := range items {
		if it.BodyStructure || it.Name == "BODYSTRUCTURE" {
			needsStructure = true
		}
		if it.Name == "ENVELOPE" {
			needsEnvelope = true
		}
	}

	// Metadata + structure rows (one pass).
	metas := map[uint32]*models.MailMessage{}
	structures := map[uint32]*mailstore.MimePart{}
	if len(uidSet) > 0 {
		res, err := st.srv.store.FetchRange(ctx, mailstore.FetchRangeSpec{
			FolderID:    f.ID,
			UserID:      st.user.ID,
			WorkspaceID: 0,
			UIDMode:     true,
			Set:         uidSet,
			Items:       mailstore.FetchItems{Structure: needsStructure},
		})
		if err != nil {
			st.tagged(cmd.Tag, "NO", "FETCH failed: "+err.Error())
			return nil
		}
		for k := range res {
			metas[res[k].UID] = res[k].Message
			if res[k].Structure != nil {
				structures[res[k].UID] = res[k].Structure
			}
		}
	}

	// Section fetches: one FetchRange per distinct requested section.
	sections := map[string]map[uint32][]byte{} // item name → uid → bytes
	for _, it := range items {
		if it.Section == nil {
			continue
		}
		key := it.Name
		if _, done := sections[key]; done {
			continue
		}
		fetched := map[uint32][]byte{}
		if len(uidSet) > 0 {
			res, err := st.srv.store.FetchRange(ctx, mailstore.FetchRangeSpec{
				FolderID:    f.ID,
				UserID:      st.user.ID,
				WorkspaceID: 0,
				UIDMode:     true,
				Set:         uidSet,
				Items:       mailstore.FetchItems{Body: it.Section},
			})
			if err != nil {
				st.tagged(cmd.Tag, "NO", "FETCH failed: "+err.Error())
				return nil
			}
			for k := range res {
				if res[k].SectionFound {
					fetched[res[k].UID] = res[k].Section
				} else {
					fetched[res[k].UID] = []byte{} // missing section: empty response
				}
			}
		}
		sections[key] = fetched
	}

	// Envelopes: raw reads for the messages that need one.
	envelopes := map[uint32]envelopeText{}
	if needsEnvelope {
		for _, t := range targets {
			raw, err := st.srv.store.GetMessageBody(ctx, st.view.msgIDs[t.uid], st.user.ID, 0)
			if err != nil {
				st.tagged(cmd.Tag, "NO", "FETCH failed: "+err.Error())
				return nil
			}
			envelopes[t.uid] = buildEnvelope(raw)
		}
	}

	// Render, one untagged FETCH per message, in sequence order. Literal
	// payloads are streamed straight to the wire between the text fragments.
	for _, t := range targets {
		m := metas[t.uid]
		if m == nil {
			continue
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("* %d FETCH (", t.seq))
		first := true
		emit := func(s string) {
			if !first {
				b.WriteByte(' ')
			}
			first = false
			b.WriteString(s)
		}
		for _, it := range items {
			switch it.Name {
			case "FLAGS":
				emit("FLAGS (" + flagNames(m.Flags) + ")")
			case "UID":
				emit(fmt.Sprintf("UID %d", t.uid))
			case "INTERNALDATE":
				emit(`INTERNALDATE ` + quoteString(internalDate(m.CreatedAt)))
			case "RFC822.SIZE":
				emit(fmt.Sprintf("RFC822.SIZE %d", m.Size))
			case "ENVELOPE":
				env := envelopes[t.uid]
				emit("ENVELOPE " + env.render())
			case "BODY":
				emit("BODY " + renderBodyStructure(structures[t.uid], false))
			case "BODYSTRUCTURE":
				emit("BODYSTRUCTURE " + renderBodyStructure(structures[t.uid], true))
			default:
				// Section items (BODY[...], RFC822, RFC822.HEADER, RFC822.TEXT).
				sec := sections[it.Name][t.uid]
				if sec == nil {
					sec = []byte{}
				}
				emit(it.Name + " {" + strconv.Itoa(len(sec)) + "}\r\n")
				st.write(b.String())
				st.writeBytes(sec)
				b.Reset()
				continue
			}
		}
		b.WriteString(")\r\n")
		st.write(b.String())
	}
	st.srv.auditf(AuditFetch, st.user.ID, st.user.Email, st.remote,
		map[string]any{"folder": f.IMAPPath, "count": len(targets)})
	st.tagged(cmd.Tag, "OK", "FETCH completed")
	return nil
}
