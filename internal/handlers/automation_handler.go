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
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/automation"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// AutomationHandler serves the automation workflow endpoints: the builder CRUD
// behind /automations, the activate/pause/duplicate lifecycle, the execution
// inspector (journeys + node-run timelines) and the custom-event ingest. It
// validates workflows through the automation domain package and reads through
// the automation repositories; enrollment is delegated to the
// automation.AutomationTrigger (the engine), which may be nil — endpoints that
// only record then answer 202 {enrolled:false} instead of failing.
type AutomationHandler struct {
	db             *gorm.DB
	automationRepo *repositories.AutomationRepository
	journeyRepo    *repositories.AutomationJourneyRepository
	runRepo        *repositories.AutomationRunRepository
	listRepo       *repositories.SubscriberListRepository
	subscriberRepo *repositories.SubscriberRepository
	trigger        automation.AutomationTrigger
}

// NewAutomationHandler returns an AutomationHandler over the automation
// repositories. The enrollment trigger is injected separately via
// SetAutomationTrigger, following the handler setter convention.
func NewAutomationHandler(db *gorm.DB) *AutomationHandler {
	return &AutomationHandler{
		db:             db,
		automationRepo: repositories.NewAutomationRepository(db),
		journeyRepo:    repositories.NewAutomationJourneyRepository(db),
		runRepo:        repositories.NewAutomationRunRepository(db),
		listRepo:       repositories.NewSubscriberListRepository(db),
		subscriberRepo: repositories.NewSubscriberRepository(db),
	}
}

// SetAutomationTrigger wires the enrollment engine behind the custom-event
// ingest. Nil (the default) makes the ingest answer 202 {enrolled:false}.
func (h *AutomationHandler) SetAutomationTrigger(at automation.AutomationTrigger) {
	h.trigger = at
}

// notifyAutomation fires one trigger event into the automation engine. It is
// the single nil-guarded hook the subscriber, subscriber-list and tracking
// handlers call: a nil trigger (engine not wired) or an engine error is logged
// and swallowed — an automation failure must never break the triggering
// action, and an unwired engine must turn every hook into a no-op. (The
// engine's Enroll additionally recovers panics and never fails, so the error
// branch is belt-and-braces.)
func notifyAutomation(ctx context.Context, at automation.AutomationTrigger, ev automation.TriggerEvent) {
	if at == nil {
		return
	}
	if err := at.Enroll(ctx, ev); err != nil {
		logger.Warn("automation trigger dropped", "kind", ev.Kind, "error", err)
	}
}

// ---------- Requests ----------

type ListAutomationsRequest struct {
	Page   int    `query:"page" default:"0"`
	Size   int    `query:"size" default:"20"`
	Status string `query:"status" doc:"Filter by status: draft, live or paused"`
}

type AutomationRequest struct {
	ID int `param:"id"`
}

// SaveAutomationRequest is the builder document behind POST /automations and
// PUT /automations/{id}: a full workflow (nodes + root) plus the automation
// metadata. trigger_config is optional — when absent it is derived from the
// trigger node's config so the engine-matching column can never drift from the
// graph. The ID field is only bound on the PUT route.
type SaveAutomationRequest struct {
	ID   int `param:"id"`
	Body struct {
		Name          string          `json:"name" required:"true"`
		ListID        *uint           `json:"list_id" doc:"Subscriber list the automation sends to; required by email nodes"`
		ExitOnReply   bool            `json:"exit_on_reply" doc:"Reserved for v1.1"`
		TriggerConfig json.RawMessage `json:"trigger_config" doc:"Row-level trigger match document; derived from the trigger node when omitted"`
		Nodes         json.RawMessage `json:"nodes" required:"true" doc:"Workflow graph: [{id, type, config, next_node_id, position}]"`
		RootNodeID    string          `json:"root_node_id" required:"true" doc:"ID of the trigger node"`
	} `json:"body"`
}

type ListAutomationExecutionsRequest struct {
	ID      int    `param:"id"`
	Page    int    `query:"page" default:"0"`
	Size    int    `query:"size" default:"20"`
	Status  string `query:"status" doc:"Filter by status: active, completed, exited or failed"`
	Contact string `query:"contact" doc:"Filter by contact email substring"`
}

type AutomationExecutionRequest struct {
	ID        int `param:"id"`
	JourneyID int `param:"journeyId"`
}

// AutomationEventRequest is the custom-event ingest payload. The event name is
// matched against live automations whose trigger is a custom event of the same
// name; an unknown event is accepted (202) and silently matches nothing.
type AutomationEventRequest struct {
	Body struct {
		Event        string         `json:"event" required:"true" doc:"Custom event name"`
		ContactEmail string         `json:"contact_email" required:"true" format:"email"`
		Properties   map[string]any `json:"properties" doc:"Free-form event payload carried on the journey"`
	} `json:"body"`
}

// ---------- Responses ----------

// AutomationResponse is the API shape of an automation: the JSONB columns
// (trigger_config, nodes, stats) are inlined as parsed JSON instead of the
// stored strings, so the builder never re-parses on the client.
type AutomationResponse struct {
	ID            uint                 `json:"id"`
	UUID          string               `json:"uuid"`
	UserID        uint                 `json:"user_id,omitempty"`
	WorkspaceID   *uint                `json:"workspace_id,omitempty"`
	Name          string               `json:"name"`
	Status        string               `json:"status"`
	ListID        *uint                `json:"list_id,omitempty"`
	ExitOnReply   bool                 `json:"exit_on_reply"`
	TriggerConfig json.RawMessage      `json:"trigger_config"`
	Nodes         json.RawMessage      `json:"nodes"`
	RootNodeID    string               `json:"root_node_id"`
	Stats         models.WorkflowStats `json:"stats"`
	Version       int                  `json:"version"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
}

// AutomationExecutionDetail is one journey with its node-run timeline, oldest
// run first.
type AutomationExecutionDetail struct {
	Journey  *models.AutomationJourney  `json:"journey"`
	Timeline []models.AutomationNodeRun `json:"timeline"`
}

// AutomationStatsResponse aggregates an automation's counters: the row stats
// (incremented atomically by the engine) flattened, plus the per-node
// entered/completed/failed breakdown behind the stats canvas.
type AutomationStatsResponse struct {
	Enrolled  int64                      `json:"enrolled"`
	Completed int64                      `json:"completed"`
	Exited    int64                      `json:"exited"`
	Failed    int64                      `json:"failed"`
	NodeStats []repositories.NodeStatRow `json:"node_stats"`
}

// AutomationEventResponse answers the custom-event ingest. Accepted is always
// true (the event was received); Enrolled says whether a matching live
// automation actually enrolled the contact.
type AutomationEventResponse struct {
	Accepted     bool   `json:"accepted"`
	Enrolled     bool   `json:"enrolled"`
	SubscriberID uint   `json:"subscriber_id,omitempty"`
	Email        string `json:"email,omitempty"`
	Reason       string `json:"reason,omitempty" doc:"Why the event did not enroll: unknown_subscriber, trigger_unavailable or enroll_failed"`
}

// ---------- Helpers ----------

// toAutomationResponse renders one automation row for the API.
func toAutomationResponse(a *models.Automation) AutomationResponse {
	return AutomationResponse{
		ID:            a.ID,
		UUID:          a.UUID,
		UserID:        a.UserID,
		WorkspaceID:   a.WorkspaceID,
		Name:          a.Name,
		Status:        a.Status,
		ListID:        a.ListID,
		ExitOnReply:   a.ExitOnReply,
		TriggerConfig: rawJSON(a.TriggerConfig, "{}"),
		Nodes:         rawJSON(a.Nodes, "[]"),
		RootNodeID:    a.RootNodeID,
		Stats:         models.ParseWorkflowStats(a.Stats),
		Version:       a.Version,
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
	}
}

// rawJSON converts a stored JSON string column into a RawMessage, falling back
// to the structural default when the value is empty or malformed.
func rawJSON(value, fallback string) json.RawMessage {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || !json.Valid([]byte(trimmed)) {
		return json.RawMessage(fallback)
	}
	return json.RawMessage(trimmed)
}

// validateAutomationWorkflow parses and validates a workflow graph. It returns
// the parsed document (nil when parsing failed) and the problems; an empty
// problem list means the workflow is valid.
func validateAutomationWorkflow(nodesJSON, rootNodeID string, listID *uint) (*automation.WorkflowDoc, []string) {
	doc, err := automation.ParseWorkflow(rootNodeID, nodesJSON)
	if err != nil {
		return nil, []string{err.Error()}
	}
	return doc, doc.Validate(listID)
}

// validationFailed answers the builder's 400: the error message plus the
// verbatim problem list, one entry per violated rule.
func validationFailed(c *okapi.Context, errs []string) error {
	return c.JSON(http.StatusBadRequest, okapi.M{
		"success":           false,
		"error":             "automation validation failed",
		"validation_errors": errs,
	})
}

// resolveTriggerConfig returns the JSON string persisted on
// Automation.TriggerConfig — the column the engine matches live automations
// against. The builder keeps the canonical trigger on the trigger node's
// config, so when the request does not carry a usable row-level
// trigger_config (an object with a non-empty event_kind), one is derived from
// the workflow's trigger node instead and the two can never drift apart.
func resolveTriggerConfig(provided json.RawMessage, nodesJSON, rootNodeID string) string {
	if cfg := triggerConfigFromRaw(provided); cfg != "" {
		return cfg
	}
	doc, err := automation.ParseWorkflow(rootNodeID, nodesJSON)
	if err != nil {
		return "{}"
	}
	trigger := doc.Trigger()
	if trigger == nil {
		return "{}"
	}
	cfg, err := doc.TriggerConfig(trigger)
	if err != nil {
		return "{}"
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// triggerConfigFromRaw normalizes a request-provided trigger_config: an object
// carrying an event_kind is used verbatim; anything else yields "" so the
// config is derived from the trigger node instead.
func triggerConfigFromRaw(provided json.RawMessage) string {
	trimmed := strings.TrimSpace(string(provided))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var cfg models.AutomationTriggerConfig
	if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil || strings.TrimSpace(cfg.EventKind) == "" {
		return ""
	}
	return trimmed
}

// checkListInScope answers 400 unless listID references a subscriber list in
// the caller's scope — email nodes send to the automation's list, so a
// cross-workspace list must never be accepted.
//
// okapi's Abort helpers write the response and return a nil error, so these
// helpers pair the abort with a real sentinel error — callers must treat any
// error as "response already written" and return nil.
var (
	errAutomationNotFound = errors.New("automation not found")
	errListOutOfScope     = errors.New("list_id does not reference a subscriber list in this workspace")
)

func (h *AutomationHandler) checkListInScope(c *okapi.Context, listID uint) error {
	list, err := h.listRepo.FindByID(listID)
	if err != nil || !ownsResource(c, list.UserID, list.WorkspaceID) {
		c.AbortBadRequest("list_id does not reference a subscriber list in this workspace")
		return errListOutOfScope
	}
	return nil
}

// getAutomation loads the automation for the request and answers 404 unless it
// belongs to the caller's scope.
func (h *AutomationHandler) getAutomation(c *okapi.Context, id int) (*models.Automation, error) {
	a, err := h.automationRepo.GetByID(uint(id))
	if err != nil || !ownsResource(c, a.UserID, a.WorkspaceID) {
		c.AbortNotFound("automation not found")
		return nil, errAutomationNotFound
	}
	return a, nil
}

// ---------- Automations ----------

// List returns a page of the scope's automations, newest first, with parsed
// stats. status filters draft|live|paused when non-empty.
func (h *AutomationHandler) List(c *okapi.Context, req *ListAutomationsRequest) error {
	page, repoPage, size := securityPage(req.Page, req.Size)
	scope := getScope(c)

	automations, total, err := h.automationRepo.ListByScope(repoPage, size, scope.UserID, scope.WorkspaceID, req.Status)
	if err != nil {
		return c.AbortInternalServerError("failed to list automations")
	}

	items := make([]AutomationResponse, 0, len(automations))
	for i := range automations {
		items = append(items, toAutomationResponse(&automations[i]))
	}
	return paginated(c, items, total, page, size)
}

// Create validates and persists a new automation draft.
func (h *AutomationHandler) Create(c *okapi.Context, req *SaveAutomationRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("Insufficient workspace permissions", err)
	}
	scope := getScope(c)

	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return c.AbortBadRequest("name is required")
	}
	nodesJSON := strings.TrimSpace(string(req.Body.Nodes))
	if nodesJSON == "" {
		return validationFailed(c, []string{"nodes is required: add a trigger node first"})
	}
	if _, errs := validateAutomationWorkflow(nodesJSON, req.Body.RootNodeID, req.Body.ListID); len(errs) > 0 {
		return validationFailed(c, errs)
	}
	if req.Body.ListID != nil && *req.Body.ListID != 0 {
		if err := h.checkListInScope(c, *req.Body.ListID); err != nil {
			return nil
		}
	}

	a := &models.Automation{
		UserID:        scope.UserID,
		WorkspaceID:   scope.WorkspaceID,
		Name:          name,
		Status:        models.AutomationStatusDraft,
		ListID:        req.Body.ListID,
		ExitOnReply:   req.Body.ExitOnReply,
		TriggerConfig: resolveTriggerConfig(req.Body.TriggerConfig, nodesJSON, req.Body.RootNodeID),
		Nodes:         nodesJSON,
		RootNodeID:    req.Body.RootNodeID,
		Stats:         models.DefaultWorkflowStatsJSON,
		Version:       1,
	}
	if err := h.automationRepo.Create(a); err != nil {
		return c.AbortInternalServerError("failed to create automation")
	}
	return created(c, toAutomationResponse(a))
}

// Get returns one automation with parsed trigger_config, nodes and stats.
func (h *AutomationHandler) Get(c *okapi.Context, req *AutomationRequest) error {
	a, err := h.getAutomation(c, req.ID)
	if err != nil {
		return nil
	}
	return ok(c, toAutomationResponse(a))
}

// Update replaces the builder document. A live automation picks the new
// workflow up on its next node tick: the engine re-reads the automation row at
// the start of every ExecuteJourney, so there is no cache to invalidate and no
// run to restart — contacts already parked inside a delay keep the node they
// were parked at, and the graph around them is simply the new one.
func (h *AutomationHandler) Update(c *okapi.Context, req *SaveAutomationRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("Insufficient workspace permissions", err)
	}
	a, err := h.getAutomation(c, req.ID)
	if err != nil {
		return nil
	}

	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return c.AbortBadRequest("name is required")
	}
	nodesJSON := strings.TrimSpace(string(req.Body.Nodes))
	if nodesJSON == "" {
		return validationFailed(c, []string{"nodes is required: add a trigger node first"})
	}
	if _, errs := validateAutomationWorkflow(nodesJSON, req.Body.RootNodeID, req.Body.ListID); len(errs) > 0 {
		return validationFailed(c, errs)
	}
	if req.Body.ListID != nil && *req.Body.ListID != 0 {
		if err := h.checkListInScope(c, *req.Body.ListID); err != nil {
			return nil
		}
	}

	a.Name = name
	a.ListID = req.Body.ListID
	a.ExitOnReply = req.Body.ExitOnReply
	a.Nodes = nodesJSON
	a.RootNodeID = req.Body.RootNodeID
	a.TriggerConfig = resolveTriggerConfig(req.Body.TriggerConfig, nodesJSON, req.Body.RootNodeID)
	a.Version++

	if err := h.automationRepo.Update(a); err != nil {
		return c.AbortInternalServerError("failed to update automation")
	}
	return ok(c, toAutomationResponse(a))
}

// Delete refuses to delete a live automation (pause it first) and otherwise
// soft-deletes the row; the same transaction exits its active journeys, so
// paused or parked contacts never linger as zombies.
func (h *AutomationHandler) Delete(c *okapi.Context, req *AutomationRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("Insufficient workspace permissions", err)
	}
	a, err := h.getAutomation(c, req.ID)
	if err != nil {
		return nil
	}
	if a.Status == models.AutomationStatusLive {
		return c.AbortConflict("automation is live: pause it before deleting")
	}
	if err := h.automationRepo.SoftDelete(a.ID); err != nil {
		return c.AbortInternalServerError("failed to delete automation")
	}
	return ok(c, dto.MessageData{Message: "Automation deleted"})
}

// Activate flips the stored workflow live. The workflow is re-validated from
// the stored row (not the request), so only a valid graph can ever go live;
// the transition is a compare-and-swap, so two racing activations (or a
// concurrent pause) cannot both win.
func (h *AutomationHandler) Activate(c *okapi.Context, req *AutomationRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("Insufficient workspace permissions", err)
	}
	a, err := h.getAutomation(c, req.ID)
	if err != nil {
		return nil
	}

	if _, errs := validateAutomationWorkflow(a.Nodes, a.RootNodeID, a.ListID); len(errs) > 0 {
		return validationFailed(c, errs)
	}

	updated, err := h.automationRepo.UpdateIfStatus(a.ID, "", models.AutomationStatusLive, repositories.AutomationUpdate{})
	if err != nil {
		return c.AbortInternalServerError("failed to activate automation")
	}
	if !updated {
		return c.AbortConflict("automation status changed concurrently; reload and retry")
	}
	a.Status = models.AutomationStatusLive
	return ok(c, toAutomationResponse(a))
}

// Pause flips a live automation back to paused under compare-and-swap. Active
// journeys stay parked where they are; the engine simply stops claiming them
// while the automation is not live.
func (h *AutomationHandler) Pause(c *okapi.Context, req *AutomationRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("Insufficient workspace permissions", err)
	}
	a, err := h.getAutomation(c, req.ID)
	if err != nil {
		return nil
	}

	updated, err := h.automationRepo.UpdateIfStatus(a.ID, models.AutomationStatusLive, models.AutomationStatusPaused, repositories.AutomationUpdate{})
	if err != nil {
		return c.AbortInternalServerError("failed to pause automation")
	}
	if !updated {
		return c.AbortConflict("automation is not live")
	}
	a.Status = models.AutomationStatusPaused
	return ok(c, toAutomationResponse(a))
}

// Duplicate copies the automation as a fresh draft named "<name> (copy)": the
// graph, trigger and list carry over, counters reset to zero.
func (h *AutomationHandler) Duplicate(c *okapi.Context, req *AutomationRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("Insufficient workspace permissions", err)
	}
	a, err := h.getAutomation(c, req.ID)
	if err != nil {
		return nil
	}

	copyName := a.Name + " (copy)"
	if runes := []rune(copyName); len(runes) > 255 {
		copyName = string(runes[:255])
	}
	clone := *a
	clone.ID = 0
	clone.UUID = ""
	clone.Name = copyName
	clone.Status = models.AutomationStatusDraft
	clone.Stats = models.DefaultWorkflowStatsJSON
	clone.Version = 1
	clone.CreatedAt = time.Time{}
	clone.UpdatedAt = time.Time{}
	clone.DeletedAt = gorm.DeletedAt{}

	if err := h.automationRepo.Create(&clone); err != nil {
		return c.AbortInternalServerError("failed to duplicate automation")
	}
	return created(c, toAutomationResponse(&clone))
}

// ---------- Executions ----------

// ListExecutions returns a page of the automation's journeys (one per
// contact enrollment), newest first, filterable by status and contact email
// substring.
func (h *AutomationHandler) ListExecutions(c *okapi.Context, req *ListAutomationExecutionsRequest) error {
	a, err := h.getAutomation(c, req.ID)
	if err != nil {
		return nil
	}

	page, repoPage, size := securityPage(req.Page, req.Size)
	journeys, total, err := h.journeyRepo.ListByAutomation(a.ID, repoPage, size, req.Status, req.Contact)
	if err != nil {
		return c.AbortInternalServerError("failed to list executions")
	}
	return paginated(c, journeys, total, page, size)
}

// GetExecution returns one journey with its node-run timeline, oldest run
// first. The journey must belong to the automation in the path.
func (h *AutomationHandler) GetExecution(c *okapi.Context, req *AutomationExecutionRequest) error {
	a, err := h.getAutomation(c, req.ID)
	if err != nil {
		return nil
	}

	journey, err := h.journeyRepo.GetByID(uint(req.JourneyID))
	if err != nil || journey.AutomationID != a.ID {
		return c.AbortNotFound("execution not found")
	}

	runs, err := h.runRepo.ListByJourney(journey.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to load execution timeline")
	}
	if runs == nil {
		runs = []models.AutomationNodeRun{}
	}
	return ok(c, AutomationExecutionDetail{Journey: journey, Timeline: runs})
}

// Stats returns the automation's aggregate counters plus the per-node
// entered/completed/failed breakdown.
func (h *AutomationHandler) Stats(c *okapi.Context, req *AutomationRequest) error {
	a, err := h.getAutomation(c, req.ID)
	if err != nil {
		return nil
	}

	stats := models.ParseWorkflowStats(a.Stats)
	nodeStats, err := h.runRepo.NodeStats(a.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to load node stats")
	}
	if nodeStats == nil {
		nodeStats = []repositories.NodeStatRow{}
	}
	return ok(c, AutomationStatsResponse{
		Enrolled:  stats.Enrolled,
		Completed: stats.Completed,
		Exited:    stats.Exited,
		Failed:    stats.Failed,
		NodeStats: nodeStats,
	})
}

// ---------- Custom-event ingest ----------

// CustomEvent ingests a custom event for a contact: API key or dashboard
// session auth, workspace-scoped. The subscriber is resolved by email within
// the scope and the event handed to the automation engine, which matches it
// against live automations with a custom trigger of the same name. The
// endpoint always answers 202 once the payload is well-formed — an unknown
// subscriber, an unwired engine or an unknown event name are accepted and
// dropped (or no-op) rather than failed, matching the webhook semantics.
func (h *AutomationHandler) CustomEvent(c *okapi.Context, req *AutomationEventRequest) error {
	event := strings.TrimSpace(req.Body.Event)
	if event == "" {
		return c.AbortBadRequest("event is required")
	}
	email := strings.ToLower(strings.TrimSpace(req.Body.ContactEmail))
	if email == "" {
		return c.AbortBadRequest("contact_email is required")
	}

	accepted := func(enrolled bool, reason string, sub *models.Subscriber) error {
		resp := AutomationEventResponse{Accepted: true, Enrolled: enrolled, Reason: reason}
		if sub != nil {
			resp.SubscriberID = sub.ID
			resp.Email = sub.Email
		}
		return c.JSON(http.StatusAccepted, dto.Response[AutomationEventResponse]{
			Success: true,
			Data:    resp,
		})
	}

	sub, err := h.subscriberRepo.FindByEmail(getScope(c), email)
	if err != nil || sub == nil {
		logger.Warn("automation custom event: unknown subscriber", "event", event, "email", email)
		return accepted(false, "unknown_subscriber", nil)
	}

	if h.trigger == nil {
		return accepted(false, "trigger_unavailable", sub)
	}

	err = h.trigger.Enroll(c.Request().Context(), automation.TriggerEvent{
		Kind:            models.AutomationEventCustom,
		CustomEventName: event,
		Subscriber:      sub,
		Properties:      req.Body.Properties,
	})
	if err != nil {
		// The event was accepted; a failed enrollment is logged, not failed.
		logger.Error("automation custom event: enrollment failed", "event", event, "email", email, "error", err)
		return accepted(false, "enroll_failed", sub)
	}
	return accepted(true, "", sub)
}
