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

package handlers

import (
	"errors"
	"math"
	"strings"

	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// mailWsPtr turns the scoped workspace id (0 = personal, unbound) into the
// nullable ownership column the mailstore tables carry.
func mailWsPtr(wsID uint) *uint {
	if wsID == 0 {
		return nil
	}
	return &wsID
}

// SF-65 mail-account alias/group endpoints (ADR-003 mailbox identity + the
// ADR-002 deliver action's expansion). They ride the /api/v1/mail surface's
// gating (jwtOnly + optional workspace header — a machine API key must not
// manage mail accounts) and its scoping posture: strictly per session user,
// the mailstore enforces ownership below every call, a foreign mailbox reads
// as 404 exactly like a missing one.
//
// Semantics:
//   - An alias maps one address onto ONE existing mailbox of the session
//     user; the inbound deliver action files mail to the alias address into
//     that mailbox.
//   - A group owns an anchor mailbox (auto-provisioned at create under the
//     group address) and a member list; the deliver action fans a delivery
//     out to every member mailbox idempotently (one copy per mailbox no
//     matter how many paths reach it). The anchor mailbox itself receives
//     nothing from its own group; it is not removed on group delete.
//   - An address maps to at most ONE shape, globally: an address that is
//     already an alias or a group (any user's) cannot become the other.
//   - Nesting: members may include other groups' anchor mailboxes; delivery
//     expansion cuts cycles with its visited set and a depth bound, so a
//     cyclic configuration terminates at delivery time (create accepts it).

// ─── Requests ────────────────────────────────────────────────────────────────

type MailAliasListRequest struct{}

type MailAliasCreateRequest struct {
	Body struct {
		Mailbox uint   `json:"mailbox" required:"true" doc:"Target mailbox ID; the session user's own"`
		Address string `json:"address" required:"true" minLength:"3" maxLength:"255" doc:"The alias address (local@domain; lowercased)"`
	} `json:"body"`
}

type MailAliasIDRequest struct {
	ID int `param:"id"`
}

type MailGroupListRequest struct{}

type MailGroupCreateRequest struct {
	Body struct {
		Address         string `json:"address" required:"true" minLength:"3" maxLength:"255" doc:"The group address (local@domain; lowercased); its anchor mailbox is provisioned under it"`
		MemberMailboxes []uint `json:"member_mailboxes" doc:"Member mailbox IDs; each must be the session user's own"`
	} `json:"body"`
}

type MailGroupPatchRequest struct {
	ID int `param:"id"`
	Body struct {
		MemberMailboxes []uint `json:"member_mailboxes" required:"true" doc:"The full replacement member list (mailbox IDs, the session user's own)"`
	} `json:"body"`
}

type MailGroupIDRequest struct {
	ID int `param:"id"`
}

// ─── Response shapes ─────────────────────────────────────────────────────────

type MailAliasResponse struct {
	ID          uint   `json:"id"`
	MailboxID   uint   `json:"mailbox_id"`
	MailboxName string `json:"mailbox_name,omitempty"`
	Address     string `json:"address"`
	CreatedAt   string `json:"created_at"`
}

type MailGroupMemberResponse struct {
	MailboxID   uint   `json:"mailbox_id"`
	MailboxName string `json:"mailbox_name,omitempty"`
}

type MailGroupResponse struct {
	ID          uint                      `json:"id"`
	MailboxID   uint                      `json:"mailbox_id"`
	Address     string                    `json:"address"`
	Members     []MailGroupMemberResponse `json:"members"`
	CreatedAt   string                    `json:"created_at"`
}

// ─── Shared validation ───────────────────────────────────────────────────────

// normalizeMailAccountAddress lowercases/trims an alias or group address and
// enforces the local@domain shape. An error carries the 400-ready message.
func normalizeMailAccountAddress(raw string) (string, error) {
	addr := strings.ToLower(strings.TrimSpace(raw))
	if addr == "" {
		return "", errors.New("address is required")
	}
	if len(addr) > 255 {
		return "", errors.New("address is too long")
	}
	at := strings.Index(addr, "@")
	if at <= 0 || at == len(addr)-1 || strings.Contains(addr[at+1:], "@") {
		return "", errors.New("address must be local@domain")
	}
	if strings.ContainsAny(addr, " \t\r\n") {
		return "", errors.New("address must not contain whitespace")
	}
	return addr, nil
}

// aliasConflict reports 409 when the address is already mapped — as an alias
// or a group, by ANY user (one address, one mapping, globally).
func (h *MailHandler) aliasGroupAddressTaken(addr string) (bool, error) {
	if h.aliases != nil {
		n, err := h.aliases.CountAddress(addr)
		if err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	if h.groups != nil {
		n, err := h.groups.CountAddress(addr)
		if err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

// validateGroupMembers checks the member mailbox set: each mailbox must be
// the session user's own (foreign or missing → mailAbort's 404), the group's
// own anchor is refused (a self-loop), and duplicates collapse. Returns the
// deduplicated member IDs.
func (h *MailHandler) validateGroupMembers(userID, wsID uint, memberIDs []uint, anchorMailboxID uint) ([]uint, error) {
	seen := make(map[uint]bool, len(memberIDs))
	out := make([]uint, 0, len(memberIDs))
	for _, id := range memberIDs {
		if id == 0 || id > math.MaxInt32 {
			return nil, errors.New("member mailbox id out of range")
		}
		if id == anchorMailboxID {
			return nil, errors.New("a group cannot contain its own mailbox")
		}
		if seen[id] {
			continue
		}
		if _, err := h.store.GetMailbox(id, userID, wsID); err != nil {
			return nil, err
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// mailboxNameFor resolves a mailbox display name (best-effort: a missing or
// foreign mailbox simply omits the name — the list must not fail over it).
func (h *MailHandler) mailboxNameFor(userID, wsID uint, mailboxID uint) string {
	if h.store == nil {
		return ""
	}
	mb, err := h.store.GetMailbox(mailboxID, userID, wsID)
	if err != nil {
		return ""
	}
	return mb.Name
}

// ─── Alias endpoints ─────────────────────────────────────────────────────────

// ListAliases returns the session user's aliases with their target names.
func (h *MailHandler) ListAliases(c *okapi.Context, _ *MailAliasListRequest) error {
	if h.store == nil || h.aliases == nil {
		return mailUnavailable(c)
	}
	userID, wsID := mailScope(c)
	aliases, err := h.aliases.ListByUser(userID)
	if err != nil {
		return mailInternal(c, "failed to list aliases", err)
	}
	out := make([]MailAliasResponse, 0, len(aliases))
	for i := range aliases {
		out = append(out, MailAliasResponse{
			ID:          aliases[i].ID,
			MailboxID:   aliases[i].MailboxID,
			MailboxName: h.mailboxNameFor(userID, wsID, aliases[i].MailboxID),
			Address:     aliases[i].Address,
			CreatedAt:   aliases[i].CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return ok(c, out)
}

// CreateAlias maps one address onto one of the session user's mailboxes.
func (h *MailHandler) CreateAlias(c *okapi.Context, req *MailAliasCreateRequest) error {
	if h.store == nil || h.aliases == nil {
		return mailUnavailable(c)
	}
	userID, wsID := mailScope(c)
	addr, err := normalizeMailAccountAddress(req.Body.Address)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	// A zero mailbox id is refused before the store lookup: the store's
	// primary-key lookup cannot distinguish "unset" from "row 0", and the
	// ownership check alone would then silently accept an arbitrary target.
	if req.Body.Mailbox == 0 || req.Body.Mailbox > math.MaxInt32 {
		return c.AbortBadRequest("mailbox is required")
	}
	mb, err := h.store.GetMailbox(req.Body.Mailbox, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	if strings.EqualFold(mb.Name, addr) {
		return c.AbortBadRequest("an alias cannot shadow its own mailbox's address")
	}
	taken, err := h.aliasGroupAddressTaken(addr)
	if err != nil {
		return mailInternal(c, "failed to check address availability", err)
	}
	if taken {
		return c.AbortConflict("address is already an alias or a group")
	}
	alias := &models.MailAlias{
		UserID:      userID,
		WorkspaceID: mailWsPtr(wsID),
		MailboxID:   mb.ID,
		Address:     addr,
	}
	if err := h.aliases.Create(alias); err != nil {
		return mailInternal(c, "failed to create alias", err)
	}
	return created(c, MailAliasResponse{
		ID:          alias.ID,
		MailboxID:   alias.MailboxID,
		MailboxName: mb.Name,
		Address:     alias.Address,
		CreatedAt:   alias.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

// DeleteAlias removes one of the session user's aliases; a foreign or
// missing alias reads as 404 like a missing one.
func (h *MailHandler) DeleteAlias(c *okapi.Context, req *MailAliasIDRequest) error {
	if h.aliases == nil {
		return mailUnavailable(c)
	}
	if req.ID <= 0 || req.ID > math.MaxInt32 {
		return c.AbortBadRequest("alias id is required")
	}
	userID, _ := mailScope(c)
	n, err := h.aliases.DeleteForUser(uint(req.ID), userID)
	if err != nil {
		return mailInternal(c, "failed to delete alias", err)
	}
	if n == 0 {
		return c.AbortNotFound("alias not found")
	}
	return ok(c, dto.MessageData{Message: "alias deleted"})
}

// ─── Group endpoints ─────────────────────────────────────────────────────────

// ListGroups returns the session user's groups with their member lists.
func (h *MailHandler) ListGroups(c *okapi.Context, _ *MailGroupListRequest) error {
	if h.store == nil || h.groups == nil {
		return mailUnavailable(c)
	}
	userID, wsID := mailScope(c)
	groups, err := h.groups.ListByUser(userID)
	if err != nil {
		return mailInternal(c, "failed to list groups", err)
	}
	out := make([]MailGroupResponse, 0, len(groups))
	for i := range groups {
		members, err := h.groups.MembersOf(groups[i].ID)
		if err != nil {
			return mailInternal(c, "failed to load group members", err)
		}
		out = append(out, MailGroupResponse{
			ID:        groups[i].ID,
			MailboxID: groups[i].MailboxID,
			Address:   groups[i].Address,
			Members:   h.memberResponses(userID, wsID, members),
			CreatedAt: groups[i].CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return ok(c, out)
}

func (h *MailHandler) memberResponses(userID, wsID uint, memberIDs []uint) []MailGroupMemberResponse {
	out := make([]MailGroupMemberResponse, 0, len(memberIDs))
	for _, id := range memberIDs {
		out = append(out, MailGroupMemberResponse{
			MailboxID:   id,
			MailboxName: h.mailboxNameFor(userID, wsID, id),
		})
	}
	return out
}

// CreateGroup provisions the group's anchor mailbox under the group address
// and stores the member list (each member must be the session user's own
// mailbox; the group's own anchor is refused).
func (h *MailHandler) CreateGroup(c *okapi.Context, req *MailGroupCreateRequest) error {
	if h.store == nil || h.groups == nil {
		return mailUnavailable(c)
	}
	userID, wsID := mailScope(c)
	addr, err := normalizeMailAccountAddress(req.Body.Address)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	taken, err := h.aliasGroupAddressTaken(addr)
	if err != nil {
		return mailInternal(c, "failed to check address availability", err)
	}
	if taken {
		return c.AbortConflict("address is already an alias or a group")
	}
	// The anchor mailbox: provision on first group create, reuse when a
	// mailbox under this address already exists (the unique (user_id, name)
	// identity makes the re-lookup the race backstop).
	anchor, err := h.store.FindMailboxByName(userID, addr)
	if errors.Is(err, mailstore.ErrMailboxNotFound) {
		anchor, err = h.store.CreateMailbox(userID, mailWsPtr(wsID), addr, 0)
	}
	if err != nil {
		return mailAbort(c, err)
	}
	members, err := h.validateGroupMembers(userID, wsID, req.Body.MemberMailboxes, anchor.ID)
	if err != nil {
		if errors.Is(err, mailstore.ErrMailboxNotFound) || errors.Is(err, mailstore.ErrNotMailboxOwner) {
			return mailAbort(c, err)
		}
		return c.AbortBadRequest(err.Error())
	}
	group := &models.MailGroup{
		UserID:      userID,
		WorkspaceID: mailWsPtr(wsID),
		MailboxID:   anchor.ID,
		Address:     addr,
	}
	if err := h.groups.Create(group, members); err != nil {
		return mailInternal(c, "failed to create group", err)
	}
	return created(c, MailGroupResponse{
		ID:        group.ID,
		MailboxID: group.MailboxID,
		Address:   group.Address,
		Members:   h.memberResponses(userID, wsID, members),
		CreatedAt: group.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

// PatchGroup replaces one of the session user's groups' member list; a
// foreign or missing group reads as 404 like a missing one.
func (h *MailHandler) PatchGroup(c *okapi.Context, req *MailGroupPatchRequest) error {
	if h.store == nil || h.groups == nil {
		return mailUnavailable(c)
	}
	if req.ID <= 0 || req.ID > math.MaxInt32 {
		return c.AbortBadRequest("group id is required")
	}
	userID, wsID := mailScope(c)
	group, err := h.groups.FindByIDForUser(uint(req.ID), userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.AbortNotFound("group not found")
		}
		return mailInternal(c, "failed to load group", err)
	}
	members, err := h.validateGroupMembers(userID, wsID, req.Body.MemberMailboxes, group.MailboxID)
	if err != nil {
		if errors.Is(err, mailstore.ErrMailboxNotFound) || errors.Is(err, mailstore.ErrNotMailboxOwner) {
			return mailAbort(c, err)
		}
		return c.AbortBadRequest(err.Error())
	}
	if err := h.groups.ReplaceMembers(group.ID, userID, members); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.AbortNotFound("group not found")
		}
		return mailInternal(c, "failed to update group members", err)
	}
	return ok(c, MailGroupResponse{
		ID:        group.ID,
		MailboxID: group.MailboxID,
		Address:   group.Address,
		Members:   h.memberResponses(userID, wsID, members),
		CreatedAt: group.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

// DeleteGroup removes one of the session user's groups with its memberships
// (the anchor mailbox stays — it is an ordinary mailbox the owner may keep
// using); a foreign or missing group reads as 404 like a missing one.
func (h *MailHandler) DeleteGroup(c *okapi.Context, req *MailGroupIDRequest) error {
	if h.groups == nil {
		return mailUnavailable(c)
	}
	if req.ID <= 0 || req.ID > math.MaxInt32 {
		return c.AbortBadRequest("group id is required")
	}
	userID, _ := mailScope(c)
	n, err := h.groups.DeleteForUser(uint(req.ID), userID)
	if err != nil {
		return mailInternal(c, "failed to delete group", err)
	}
	if n == 0 {
		return c.AbortNotFound("group not found")
	}
	return ok(c, dto.MessageData{Message: "group deleted"})
}
