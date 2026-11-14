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

package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/automation"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

type SubscriberHandler struct {
	repo              *repositories.SubscriberRepository
	listRepo          *repositories.SubscriberListRepository
	doiMailer         email.DOIConfirmationSender
	doiLinks          DOIConfirmLinkGenerator
	doiFrom           string
	automationTrigger automation.AutomationTrigger
}

func NewSubscriberHandler(repo *repositories.SubscriberRepository) *SubscriberHandler {
	return &SubscriberHandler{repo: repo}
}

// SetAutomationTrigger wires the automation engine behind the creation hooks:
// every created contact enrolls into live contact.created automations. Nil
// (the default) leaves the hooks inert.
func (h *SubscriberHandler) SetAutomationTrigger(at automation.AutomationTrigger) {
	h.automationTrigger = at
}

// SetListDOIImport wires the import-side double-opt-in seams (RF-F075): the
// list repo for membership creation, the confirmation mailer and link
// generator, and the transactional From identity. Unwired (the default),
// list targeting on imports is rejected rather than half-applied.
func (h *SubscriberHandler) SetListDOIImport(listRepo *repositories.SubscriberListRepository, mailer email.DOIConfirmationSender, links DOIConfirmLinkGenerator, from string) {
	h.listRepo = listRepo
	h.doiMailer = mailer
	h.doiLinks = links
	h.doiFrom = strings.TrimSpace(from)
}

type CreateSubscriberRequest struct {
	Body struct {
		Email        string                 `json:"email" required:"true" format:"email"`
		Name         string                 `json:"name"`
		Status       string                 `json:"status"`
		CustomFields map[string]interface{} `json:"custom_fields"`
		Timezone     string                 `json:"timezone" format:"timezone" maxLength:"50" doc:"IANA timezone name; empty for none"`
		Language     string                 `json:"language"`
	} `json:"body"`
}

type UpdateSubscriberRequest struct {
	ID   int `param:"id"`
	Body struct {
		Name         string                 `json:"name"`
		Status       string                 `json:"status"`
		CustomFields map[string]interface{} `json:"custom_fields"`
		Timezone     string                 `json:"timezone" format:"timezone" maxLength:"50" doc:"IANA timezone name; empty for none"`
		Language     string                 `json:"language"`
	} `json:"body"`
}

type DeleteSubscriberRequest struct {
	ID int `param:"id"`
}

type GetSubscriberRequest struct {
	ID int `param:"id"`
}

type ListSubscribersRequest struct {
	Page   int    `query:"page" default:"0"`
	Size   int    `query:"size" default:"20"`
	Search string `query:"search"`
	Status string `query:"status"`
}

type BulkImportSubscribersRequest struct {
	Body struct {
		Subscribers []BulkSubscriberEntry `json:"subscribers" required:"true"`
		// ListID (RF-F075): optional target static list. When set, every
		// imported contact joins it — pending plus a confirmation email when
		// the list requires double opt-in, confirmed otherwise. 0 = no list.
		ListID uint `json:"list_id"`
	} `json:"body"`
}

type BulkSubscriberEntry struct {
	Email        string                 `json:"email"`
	Name         string                 `json:"name"`
	Timezone     string                 `json:"timezone"`
	Language     string                 `json:"language"`
	CustomFields map[string]interface{} `json:"custom_fields"`
}

type BulkImportResult struct {
	Created int `json:"created"`
	Skipped int `json:"skipped"`
	Total   int `json:"total"`
	// InvalidTimezones counts imported subscribers whose timezone was not an
	// IANA name. They are still imported, with no timezone.
	InvalidTimezones int `json:"invalid_timezones"`
	// MembersAdded / ConfirmationsSent (RF-F075) appear when the import
	// targeted a list: memberships created and double-opt-in confirmation
	// emails enqueued for them.
	MembersAdded      int `json:"members_added,omitempty"`
	ConfirmationsSent int `json:"confirmations_sent,omitempty"`
}

// subscriberTimezone returns tz when it is an IANA name (the check okapi's
// format:"timezone" applies), and "" otherwise. Imports and restores use it
// so one bad value drops the field instead of failing the whole batch.
func subscriberTimezone(tz string) (string, bool) {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return "", true
	}
	if tz == "Local" || len(tz) > 50 {
		return "", false
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", false
	}
	return tz, true
}

func (h *SubscriberHandler) Create(c *okapi.Context, req *CreateSubscriberRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("Insufficient workspace permissions", err)
	}
	scope := getScope(c)

	email := strings.ToLower(strings.TrimSpace(req.Body.Email))
	if email == "" {
		return c.AbortBadRequest("email is required")
	}

	status := models.SubscriberStatus(req.Body.Status)
	if status == "" {
		status = models.SubscriberStatusSubscribed
	}

	now := time.Now()
	s := &models.Subscriber{
		UserID:       scope.UserID,
		WorkspaceID:  scope.WorkspaceID,
		Email:        email,
		Name:         strings.TrimSpace(req.Body.Name),
		Status:       status,
		CustomFields: req.Body.CustomFields,
		Timezone:     strings.TrimSpace(req.Body.Timezone),
		Language:     strings.TrimSpace(req.Body.Language),
		SubscribedAt: &now,
	}

	if err := h.repo.Create(s); err != nil {
		return c.AbortConflict("subscriber already exists")
	}
	notifyAutomation(c.Request().Context(), h.automationTrigger, automation.TriggerEvent{
		Kind:       models.AutomationEventContactCreated,
		Subscriber: s,
	})
	return created(c, s)
}

func (h *SubscriberHandler) List(c *okapi.Context, req *ListSubscribersRequest) error {
	page, size, offset := normalizePageParams(req.Page, req.Size)
	items, total, err := h.repo.FindByScope(getScope(c), req.Search, req.Status, size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list subscribers")
	}
	return paginated(c, items, total, page, size)
}

func (h *SubscriberHandler) Get(c *okapi.Context, req *GetSubscriberRequest) error {
	s, err := h.repo.FindByID(uint(req.ID))
	if err != nil || !ownsResource(c, s.UserID, s.WorkspaceID) {
		return c.AbortNotFound("subscriber not found")
	}
	return ok(c, s)
}

func (h *SubscriberHandler) Update(c *okapi.Context, req *UpdateSubscriberRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("Insufficient workspace permissions", err)
	}
	s, err := h.repo.FindByID(uint(req.ID))
	if err != nil || !ownsResource(c, s.UserID, s.WorkspaceID) {
		return c.AbortNotFound("subscriber not found")
	}

	if req.Body.Name != "" {
		s.Name = req.Body.Name
	}
	newlyUnsubscribed := false
	if req.Body.Status != "" {
		newStatus := models.SubscriberStatus(req.Body.Status)
		newlyUnsubscribed = newStatus == models.SubscriberStatusUnsubscribed && s.Status != models.SubscriberStatusUnsubscribed
		if newlyUnsubscribed {
			now := time.Now()
			s.UnsubscribedAt = &now
		}
		s.Status = newStatus
	}
	if req.Body.CustomFields != nil {
		s.CustomFields = req.Body.CustomFields
	}
	if req.Body.Timezone != "" {
		s.Timezone = req.Body.Timezone
	}
	if req.Body.Language != "" {
		s.Language = req.Body.Language
	}
	now := time.Now()
	s.UpdatedAt = &now

	if err := h.repo.Update(s); err != nil {
		return c.AbortInternalServerError("failed to update subscriber")
	}
	if newlyUnsubscribed {
		notifyAutomation(c.Request().Context(), h.automationTrigger, automation.TriggerEvent{
			Kind:       models.AutomationEventUnsubscribed,
			Subscriber: s,
		})
	}
	return ok(c, s)
}

func (h *SubscriberHandler) Delete(c *okapi.Context, req *DeleteSubscriberRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("Insufficient workspace permissions", err)
	}
	s, err := h.repo.FindByID(uint(req.ID))
	if err != nil || !ownsResource(c, s.UserID, s.WorkspaceID) {
		return c.AbortNotFound("subscriber not found")
	}
	if err := h.repo.Delete(s.ID); err != nil {
		return c.AbortInternalServerError("failed to delete subscriber")
	}
	return noContent(c)
}

func (h *SubscriberHandler) BulkImportJSON(c *okapi.Context, req *BulkImportSubscribersRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("Insufficient workspace permissions", err)
	}
	scope := getScope(c)

	// RF-F075: validate the optional list target before any contact is
	// created, so a bad list_id aborts with no side effects. okapi's Abort
	// helpers write the response and return a nil error, so the second
	// return value — not the error — carries the abort.
	targetList, targetOK := h.resolveImportList(c, req.Body.ListID)
	if !targetOK {
		return nil
	}

	now := time.Now()
	var subscribers []models.Subscriber
	invalidTZ := 0
	for _, entry := range req.Body.Subscribers {
		email := strings.ToLower(strings.TrimSpace(entry.Email))
		if email == "" {
			continue
		}
		tz, valid := subscriberTimezone(entry.Timezone)
		if !valid {
			invalidTZ++
		}
		subscribers = append(subscribers, models.Subscriber{
			UserID:       scope.UserID,
			WorkspaceID:  scope.WorkspaceID,
			Email:        email,
			Name:         strings.TrimSpace(entry.Name),
			Timezone:     tz,
			Language:     strings.TrimSpace(entry.Language),
			Status:       models.SubscriberStatusSubscribed,
			CustomFields: entry.CustomFields,
			SubscribedAt: &now,
		})
	}

	created, skipped, err := h.repo.BulkCreate(subscribers)
	if err != nil {
		return c.AbortInternalServerError("import failed: " + err.Error())
	}
	// BulkCreate fills the IDs of the inserted rows in place; the skipped
	// duplicates keep a zero ID. Only genuinely new contacts enroll.
	h.notifyCreated(subscribers)

	result := BulkImportResult{
		Created:          created,
		Skipped:          skipped,
		Total:            len(subscribers),
		InvalidTimezones: invalidTZ,
	}
	if targetList != nil {
		h.attachImportToList(c.Request().Context(), scope, targetList, subscribers, &result)
	}
	return ok(c, result)
}

func (h *SubscriberHandler) BulkImportCSV(c *okapi.Context) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("Insufficient workspace permissions", err)
	}
	scope := getScope(c)

	// RF-F075: optional list_id form field, validated before any contact is
	// created so a bad value aborts with no side effects. okapi's Abort
	// helpers write the response and return a nil error, so the second
	// return value — not the error — carries the abort.
	listID := uint(0)
	if raw := strings.TrimSpace(c.Request().FormValue("list_id")); raw != "" {
		parsed, perr := strconv.ParseUint(raw, 10, 64)
		if perr != nil {
			return c.AbortBadRequest("list_id must be a positive integer")
		}
		listID = uint(parsed)
	}
	targetList, targetOK := h.resolveImportList(c, listID)
	if !targetOK {
		return nil
	}

	file, _, err := c.Request().FormFile("file")
	if err != nil {
		return c.AbortBadRequest("file is required")
	}
	defer func() { _ = file.Close() }()

	// Parse optional column mapping: {"0":"email","1":"name","2":"custom_fields.company"}
	mappingStr := c.Request().FormValue("column_mapping")
	columnMap := map[int]string{0: "email", 1: "name"} // defaults
	if mappingStr != "" {
		var raw map[string]string
		if err := json.Unmarshal([]byte(mappingStr), &raw); err == nil {
			columnMap = make(map[int]string)
			for k, v := range raw {
				idx, err := strconv.Atoi(k)
				if err != nil {
					continue
				}
				columnMap[idx] = v
			}
		}
	}

	reader := csv.NewReader(file)
	reader.TrimLeadingSpace = true

	// Skip header row
	if _, err := reader.Read(); err != nil {
		return c.AbortBadRequest("failed to read CSV header")
	}

	now := time.Now()
	var subscribers []models.Subscriber
	invalidTZ := 0
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue // skip bad rows
		}

		s := models.Subscriber{
			UserID:       scope.UserID,
			WorkspaceID:  scope.WorkspaceID,
			Status:       models.SubscriberStatusSubscribed,
			SubscribedAt: &now,
			CustomFields: make(models.CustomFields),
		}

		for idx, field := range columnMap {
			if idx >= len(record) {
				continue
			}
			val := strings.TrimSpace(record[idx])
			switch field {
			case "email":
				s.Email = strings.ToLower(val)
			case "name":
				s.Name = val
			case "status":
				s.Status = models.SubscriberStatus(val)
			case "language":
				s.Language = val
			case "timezone":
				tz, valid := subscriberTimezone(val)
				if !valid {
					invalidTZ++
				}
				s.Timezone = tz
			default:
				if strings.HasPrefix(field, "custom_fields.") {
					key := strings.TrimPrefix(field, "custom_fields.")
					s.CustomFields[key] = val
				}
			}
		}

		if s.Email == "" {
			continue
		}
		subscribers = append(subscribers, s)
	}

	createdCount, skipped, err := h.repo.BulkCreate(subscribers)
	if err != nil {
		return c.AbortInternalServerError("import failed: " + err.Error())
	}
	// Same enrollment as the JSON import: only rows BulkCreate inserted (the
	// ones that came back with an ID) fire contact.created.
	h.notifyCreated(subscribers)

	result := BulkImportResult{
		Created:          createdCount,
		Skipped:          skipped,
		Total:            len(subscribers),
		InvalidTimezones: invalidTZ,
	}
	if targetList != nil {
		h.attachImportToList(c.Request().Context(), scope, targetList, subscribers, &result)
	}
	return ok(c, result)
}

// resolveImportList validates the optional list target of a bulk import
// (RF-F075): it must exist, be in scope, and be static. The boolean is false
// when the request was aborted (okapi's Abort helpers write the response and
// return a nil error, so the caller must stop on !ok, not on err) and when
// no list was requested.
func (h *SubscriberHandler) resolveImportList(c *okapi.Context, listID uint) (*models.SubscriberList, bool) {
	if listID == 0 {
		return nil, true
	}
	if h.listRepo == nil {
		c.AbortBadRequest("list targeting is not available on this import endpoint")
		return nil, false
	}
	list, err := h.listRepo.FindByID(listID)
	if err != nil || !ownsResource(c, list.UserID, list.WorkspaceID) {
		c.AbortNotFound("target list not found")
		return nil, false
	}
	if list.Type != models.SubscriberListTypeStatic {
		c.AbortBadRequest("cannot import into a dynamic list")
		return nil, false
	}
	return list, true
}

// attachImportToList applies an import's list targeting (RF-F075): every
// imported contact joins the list — pending plus a confirmation email when
// the list requires double opt-in, confirmed otherwise. Duplicates of
// existing contacts join too, mirroring the subscribe endpoint's
// find-or-create posture. The result counters are updated in place; a
// membership or email that fails is skipped, not fatal (the import itself
// already succeeded).
func (h *SubscriberHandler) attachImportToList(ctx context.Context, scope repositories.ResourceScope, list *models.SubscriberList, subscribers []models.Subscriber, res *BulkImportResult) {
	for i := range subscribers {
		s := &subscribers[i]
		if s.Email == "" {
			continue
		}
		id := s.ID
		// confirmSub is the subscriber the confirmation email is bound to:
		// for a duplicate email the freshly built s keeps a zero ID, and a
		// token minted for subscriber 0 could never be redeemed (the
		// confirmation must carry the EXISTING contact's id).
		confirmSub := s
		if id == 0 {
			// BulkCreate skipped this email as a duplicate of an existing
			// contact; that contact joins the list as well.
			existing, err := h.repo.FindByEmail(scope, s.Email)
			if err != nil || existing == nil {
				continue
			}
			id = existing.ID
			confirmSub = existing
		}
		if h.listRepo.IsMember(list.ID, id) {
			continue
		}
		status := models.MemberStatusConfirmed
		if list.DoubleOptIn {
			status = models.MemberStatusPending
		}
		if err := h.listRepo.AddMember(&models.SubscriberListMember{
			ListID:       list.ID,
			SubscriberID: id,
			Status:       status,
		}); err != nil {
			continue
		}
		res.MembersAdded++
		if status == models.MemberStatusPending {
			if sendDOIConfirmation(ctx, h.doiMailer, h.doiLinks, h.doiFrom, scope, list, confirmSub) {
				res.ConfirmationsSent++
			}
		}
	}
}

// notifyCreated enrolls every freshly inserted subscriber of a bulk import
// into live contact.created automations. Rows BulkCreate skipped as
// duplicates keep a zero ID and are left alone. The loop runs on one
// background goroutine — an import can insert thousands of rows and the
// response must not wait on enrollment.
func (h *SubscriberHandler) notifyCreated(subscribers []models.Subscriber) {
	if h.automationTrigger == nil {
		return
	}
	go func() {
		for i := range subscribers {
			if subscribers[i].ID == 0 {
				continue
			}
			notifyAutomation(context.Background(), h.automationTrigger, automation.TriggerEvent{
				Kind:       models.AutomationEventContactCreated,
				Subscriber: &subscribers[i],
			})
		}
	}()
}
