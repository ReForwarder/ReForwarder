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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package managesieve

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/filterengine"
	"github.com/goposta/posta/internal/storage/repositories"
)

// MailboxFinder is the mailbox-resolution seam (the mailstore's
// ListMailboxes — the POP3 maildrop resolver's read).
type MailboxFinder interface {
	ListMailboxes(userID, workspaceID uint) ([]models.Mailbox, error)
}

// FilterScriptStore is the ScriptStore adapter over the SF-14 filter-script
// repository: it maps the RFC 5804 script surface onto the engine's storage
// rows (models.FilterScript: Conditions + ActionsJSON + Enabled) and confines
// every operation to the authenticated user's PRIMARY mailbox (the ADR-004
// amendment's account-scoping decision — the lowest-ID active mailbox, the
// POP3 maildrop resolver's rule).
type FilterScriptStore struct {
	scripts *repositories.FilterScriptRepository
	boxes   MailboxFinder
}

// NewFilterScriptStore assembles the adapter. Both dependencies are
// required.
func NewFilterScriptStore(scripts *repositories.FilterScriptRepository, boxes MailboxFinder) (*FilterScriptStore, error) {
	if scripts == nil {
		return nil, errors.New("managesieve: nil filter-script repository")
	}
	if boxes == nil {
		return nil, errors.New("managesieve: nil mailbox finder")
	}
	return &FilterScriptStore{scripts: scripts, boxes: boxes}, nil
}

// primaryMailbox resolves the user's primary ACTIVE mailbox (lowest ID —
// ListMailboxes returns them oldest first). A suspended or closed mailbox is
// refused exactly like the POP3 maildrop resolution.
func (st *FilterScriptStore) primaryMailbox(userID uint) (*models.Mailbox, error) {
	boxes, err := st.boxes.ListMailboxes(userID, 0)
	if err != nil {
		return nil, err
	}
	for i := range boxes {
		if boxes[i].Status == models.MailboxStatusActive {
			return &boxes[i], nil
		}
	}
	return nil, errors.New("managesieve: no active mailbox for this account")
}

// PrimaryMailboxID returns the primary active mailbox's ID.
func (st *FilterScriptStore) PrimaryMailboxID(userID uint) (uint, error) {
	box, err := st.primaryMailbox(userID)
	if err != nil {
		return 0, err
	}
	return box.ID, nil
}

// ListScripts returns the user's scripts in evaluation order, serialized as
// the engine's document shape, with the ACTIVE flag on the enabled one.
func (st *FilterScriptStore) ListScripts(userID uint) ([]ScriptEntry, error) {
	box, err := st.primaryMailbox(userID)
	if err != nil {
		return nil, err
	}
	rows, err := st.scripts.ListForUser(userID, box.ID)
	if err != nil {
		return nil, err
	}
	out := make([]ScriptEntry, 0, len(rows))
	for i := range rows {
		out = append(out, ScriptEntry{
			Name:   rows[i].Name,
			Body:   encodeDocument(&rows[i]),
			Active: rows[i].Enabled,
		})
	}
	return out, nil
}

// GetScript returns one stored document + its active flag. A foreign or
// missing name reads as missing (the isolation posture).
func (st *FilterScriptStore) GetScript(userID uint, name string) (string, bool, error) {
	box, err := st.primaryMailbox(userID)
	if err != nil {
		return "", false, err
	}
	row, err := st.scripts.FindByNameForUser(userID, box.ID, name)
	if err != nil {
		return "", false, err
	}
	return encodeDocument(row), row.Enabled, nil
}

// PutScript stores (or replaces) a named script INACTIVE. The mailbox
// argument must be the user's primary mailbox (the session passes exactly
// that; the check makes the adapter safe for any caller). The body was
// already validated by the session through the strict evaluator; the adapter
// decodes it once more to persist the engine's column shape.
func (st *FilterScriptStore) PutScript(userID, mailboxID uint, name, body string) error {
	box, err := st.primaryMailbox(userID)
	if err != nil {
		return err
	}
	if mailboxID != box.ID {
		return errors.New("managesieve: scripts are managed on the primary mailbox")
	}
	conditions, actions, err := parseScriptDocument(body)
	if err != nil {
		return err
	}
	acts, merr := json.Marshal(actions)
	if merr != nil {
		return fmt.Errorf("managesieve: encode actions: %w", merr)
	}
	return st.scripts.UpsertForUser(&models.FilterScript{
		UserID:      userID,
		MailboxID:   box.ID,
		Name:        name,
		Priority:    100,
		Enabled:     false, // PUTSCRIPT stores INACTIVE (the ADR-004 amendment)
		Conditions:  models.FilterRules(conditions),
		ActionsJSON: string(acts),
	})
}

// DeleteScript removes one named script (the session refuses the ACTIVE
// script before this is reached).
func (st *FilterScriptStore) DeleteScript(userID uint, name string) error {
	box, err := st.primaryMailbox(userID)
	if err != nil {
		return err
	}
	if _, err := st.scripts.FindByNameForUser(userID, box.ID, name); err != nil {
		return err
	}
	_, err = st.scripts.DeleteByNameForUser(userID, box.ID, name)
	return err
}

// SetActive activates one script and deactivates the others — exactly one
// active script per mailbox, mapped onto the filter engine's enabled flag.
// An empty name deactivates all.
func (st *FilterScriptStore) SetActive(userID uint, name string) error {
	box, err := st.primaryMailbox(userID)
	if err != nil {
		return err
	}
	if name == "" {
		return st.scripts.DeactivateAllForMailbox(userID, box.ID)
	}
	return st.scripts.SetActiveForMailbox(userID, box.ID, name)
}

// encodeDocument serializes one engine row into the ManageSieve document
// shape ({"conditions": [...], "actions": [...]}).
func encodeDocument(row *models.FilterScript) string {
	var actions []filterengine.ScriptAction
	if err := json.Unmarshal([]byte(row.ActionsJSON), &actions); err != nil {
		actions = []filterengine.ScriptAction{}
	}
	doc, err := json.Marshal(scriptDocument{
		Conditions: []models.FilterRule(row.Conditions),
		Actions:    actions,
	})
	if err != nil {
		return "{}"
	}
	return string(doc)
}
