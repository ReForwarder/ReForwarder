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

package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
)

// maxWebhookOutputBody matches the webhook client's response capture limit.
const maxWebhookOutputBody = 2048

// maxDelayDuration guards against duration overflow: a delay config larger than
// ten years is a builder mistake and would wrap time.Duration arithmetic.
const maxDelayDuration = 10 * 365 * 24 * time.Hour

// nodeResult is what one executor hands back to the engine: the branch taken
// (resolved against the workflow by NextNodeFrom), the JSON output recorded on
// the node run, and — for delay nodes — the park time.
type nodeResult struct {
	Branch      string
	Output      map[string]any
	ScheduledAt *time.Time
}

// nodeContext is the per-execution state the executors read.
type nodeContext struct {
	automation *models.Automation
	journey    *models.AutomationJourney
	subscriber *models.Subscriber
	workflow   *WorkflowDoc
	// properties are the journey's context document (last custom-event
	// properties and similar render data).
	properties map[string]any
}

// listCheckerFor returns the membership predicate the condition evaluator uses
// for in_list rules.
func (e *Engine) listCheckerFor(journey *models.AutomationJourney) func(listID uint) bool {
	return func(listID uint) bool {
		return e.lists.IsMember(listID, journey.SubscriberID)
	}
}

// executeNode dispatches one node to its executor. An error fails the node (and
// via the engine's error path, schedules a journey retry); a nil error always
// produces a result whose Branch + ScheduledAt the engine turns into the next
// cursor.
func (e *Engine) executeNode(ctx context.Context, nc *nodeContext, node *models.AutomationNode) (*nodeResult, error) {
	if node == nil {
		return nil, fmt.Errorf("node is nil")
	}
	switch node.Type {
	case NodeTypeTrigger:
		// Pass-through; the trigger's effects happened at enrollment.
		return &nodeResult{Output: map[string]any{}}, nil
	case NodeTypeDelay:
		return e.executeDelayNode(nc, node)
	case NodeTypeEmail:
		return e.executeEmailNode(ctx, nc, node)
	case NodeTypeFilter:
		return e.executeFilterNode(nc, node)
	case NodeTypeABTest:
		return e.executeABTestNode(nc, node)
	case NodeTypeListStatus:
		return e.executeListStatusNode(nc, node)
	case NodeTypeAddToList:
		return e.executeAddToListNode(nc, node)
	case NodeTypeRemoveFromList:
		return e.executeRemoveFromListNode(nc, node)
	case NodeTypeWebhook:
		return e.executeWebhookNode(ctx, nc, node)
	default:
		return nil, fmt.Errorf("unknown node type %q", node.Type)
	}
}

// executeDelayNode parks the journey: it only computes the wake-up time. The
// engine persists it as the journey's scheduled_at (with the cursor already
// advanced past this node) and stops the tick; the polling scheduler picks the
// journey back up when the delay elapses.
func (e *Engine) executeDelayNode(nc *nodeContext, node *models.AutomationNode) (*nodeResult, error) {
	cfg, err := nc.workflow.DelayConfig(node)
	if err != nil {
		return nil, fmt.Errorf("delay node %q: %w", node.ID, err)
	}
	if cfg.Duration <= 0 {
		return nil, fmt.Errorf("delay node %q: duration must be greater than 0", node.ID)
	}
	var unit time.Duration
	switch cfg.Unit {
	case models.AutomationDelayMinutes:
		unit = time.Minute
	case models.AutomationDelayHours:
		unit = time.Hour
	case models.AutomationDelayDays:
		unit = 24 * time.Hour
	case models.AutomationDelayWeeks:
		unit = 7 * 24 * time.Hour
	default:
		return nil, fmt.Errorf("delay node %q: unknown unit %q", node.ID, cfg.Unit)
	}
	duration := time.Duration(cfg.Duration) * unit
	if duration <= 0 || duration > maxDelayDuration {
		return nil, fmt.Errorf("delay node %q: duration %d %s is out of range", node.ID, cfg.Duration, cfg.Unit)
	}
	scheduledAt := e.now().Add(duration)
	output := map[string]any{"scheduled_at": scheduledAt.UTC().Format(time.RFC3339)}
	if cfg.Description != "" {
		output["description"] = cfg.Description
	}
	return &nodeResult{Output: output, ScheduledAt: &scheduledAt}, nil
}

// executeEmailNode renders and queues the automation email through the
// EmailSender (the Email row + bulk enqueue live there). A missing or
// unrenderable template is a node error: the journey retries and eventually
// fails, which the stats surface.
func (e *Engine) executeEmailNode(ctx context.Context, nc *nodeContext, node *models.AutomationNode) (*nodeResult, error) {
	cfg, err := nc.workflow.EmailConfig(node)
	if err != nil {
		return nil, fmt.Errorf("email node %q: %w", node.ID, err)
	}
	if cfg.TemplateID == 0 {
		return nil, fmt.Errorf("email node %q: template_id is missing", node.ID)
	}
	if e.emailSender == nil {
		return nil, fmt.Errorf("email node %q: the email sender is not configured", node.ID)
	}
	emailUUID, err := e.emailSender.SendAutomationEmail(
		ctx,
		nc.automation.ID,
		cfg.TemplateID,
		cfg.SubjectOverride,
		cfg.FromOverride,
		nc.journey.Email,
		e.buildEmailData(nc),
	)
	if err != nil {
		return nil, fmt.Errorf("email node %q: %w", node.ID, err)
	}
	return &nodeResult{Output: map[string]any{"email_uuid": emailUUID}}, nil
}

// buildEmailData assembles the template render data: contact fields, the
// subscriber's custom fields, the automation name, and the triggering event's
// properties (both flattened top-level for {{ order_id }}-style references and
// nested under "event").
func (e *Engine) buildEmailData(nc *nodeContext) map[string]any {
	data := map[string]any{
		"contact_email":   nc.journey.Email,
		"automation_name": nc.automation.Name,
		"automation_id":   nc.automation.ID,
		"event":           nc.properties,
	}
	for k, v := range nc.properties {
		if _, taken := data[k]; !taken {
			data[k] = v
		}
	}
	if nc.subscriber != nil {
		data["contact_name"] = nc.subscriber.Name
		data["contact_status"] = string(nc.subscriber.Status)
		custom := map[string]any{}
		for k, v := range nc.subscriber.CustomFields {
			custom[k] = v
		}
		data["custom_fields"] = custom
	}
	return data
}

// executeFilterNode evaluates the conditions and reports the branch. An empty
// continue/exit target resolves to "" in NextNodeFrom, which completes the
// journey at this node — matching the the reference workflow platform semantics (complete, not exit).
func (e *Engine) executeFilterNode(nc *nodeContext, node *models.AutomationNode) (*nodeResult, error) {
	cfg, err := nc.workflow.FilterConfig(node)
	if err != nil {
		return nil, fmt.Errorf("filter node %q: %w", node.ID, err)
	}
	matched := EvaluateConditions(cfg.Conditions, nc.subscriber, e.listCheckerFor(nc.journey))
	branch := BranchExit
	if matched {
		branch = BranchContinue
	}
	return &nodeResult{
		Branch: branch,
		Output: map[string]any{"branch": branch, "matched": matched},
	}, nil
}

// executeABTestNode picks a variant deterministically: FNV-1a 32 of the contact
// email + node id, rolled against the cumulative variant weights. The same
// contact always lands in the same variant of the same node.
func (e *Engine) executeABTestNode(nc *nodeContext, node *models.AutomationNode) (*nodeResult, error) {
	cfg, err := nc.workflow.ABTestConfig(node)
	if err != nil {
		return nil, fmt.Errorf("ab_test node %q: %w", node.ID, err)
	}
	if len(cfg.Variants) == 0 {
		return nil, fmt.Errorf("ab_test node %q: no variants configured", node.ID)
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(nc.journey.Email))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(node.ID))
	roll := int(h.Sum32() % 100)

	// Cumulative walk; if the weights are malformed (sum < 100 — validation
	// prevents, but never trust the wire), the last variant catches the rest.
	chosen := cfg.Variants[len(cfg.Variants)-1]
	cumulative := 0
	for _, variant := range cfg.Variants {
		cumulative += variant.Weight
		if roll < cumulative {
			chosen = variant
			break
		}
	}
	return &nodeResult{
		Branch: chosen.ID,
		Output: map[string]any{"branch": chosen.ID, "name": chosen.Name, "roll": roll},
	}, nil
}

// executeListStatusNode branches on the contact's standing in the configured
// list. Membership rows carry no status column, so the branches map onto the
// platform's data: a live membership row is active, a list-scoped opt-out
// (subscriber_list_unsubscribes) is non_active, and anything else is
// not_in_list.
func (e *Engine) executeListStatusNode(nc *nodeContext, node *models.AutomationNode) (*nodeResult, error) {
	cfg, err := nc.workflow.ListStatusConfig(node)
	if err != nil {
		return nil, fmt.Errorf("list_status node %q: %w", node.ID, err)
	}
	if cfg.ListID == 0 {
		return nil, fmt.Errorf("list_status node %q: list_id is missing", node.ID)
	}
	branch := BranchNotInList
	switch {
	case e.lists.IsMember(cfg.ListID, nc.journey.SubscriberID):
		branch = BranchActive
	case e.lists.IsSuppressed(cfg.ListID, nc.journey.SubscriberID):
		branch = BranchNonActive
	}
	return &nodeResult{
		Branch: branch,
		Output: map[string]any{"branch": branch, "list_id": cfg.ListID},
	}, nil
}

// executeAddToListNode adds the journey contact to the list. The insert is
// ON CONFLICT DO NOTHING, so re-entry is idempotent.
func (e *Engine) executeAddToListNode(nc *nodeContext, node *models.AutomationNode) (*nodeResult, error) {
	cfg, err := nc.workflow.AddToListConfig(node)
	if err != nil {
		return nil, fmt.Errorf("add_to_list node %q: %w", node.ID, err)
	}
	if cfg.ListID == 0 {
		return nil, fmt.Errorf("add_to_list node %q: list_id is missing", node.ID)
	}
	member := &models.SubscriberListMember{
		ListID:       cfg.ListID,
		SubscriberID: nc.journey.SubscriberID,
	}
	if err := e.lists.AddMember(member); err != nil {
		return nil, fmt.Errorf("add_to_list node %q: %w", node.ID, err)
	}
	return &nodeResult{Output: map[string]any{"list_id": cfg.ListID}}, nil
}

// executeRemoveFromListNode removes the journey contact from the list. Removing
// a non-member is a no-op, not an error.
func (e *Engine) executeRemoveFromListNode(nc *nodeContext, node *models.AutomationNode) (*nodeResult, error) {
	cfg, err := nc.workflow.RemoveFromListConfig(node)
	if err != nil {
		return nil, fmt.Errorf("remove_from_list node %q: %w", node.ID, err)
	}
	if cfg.ListID == 0 {
		return nil, fmt.Errorf("remove_from_list node %q: list_id is missing", node.ID)
	}
	if err := e.lists.RemoveMember(cfg.ListID, nc.journey.SubscriberID); err != nil {
		return nil, fmt.Errorf("remove_from_list node %q: %w", node.ID, err)
	}
	return &nodeResult{Output: map[string]any{"list_id": cfg.ListID}}, nil
}

// executeWebhookNode posts the webhook and records status code + response body
// (capped) in the output. A transport error or a non-2xx status is a node
// error — retryable through the standard backoff — with the status and a body
// snippet carried in the error message for the timeline.
func (e *Engine) executeWebhookNode(ctx context.Context, nc *nodeContext, node *models.AutomationNode) (*nodeResult, error) {
	cfg, err := nc.workflow.WebhookConfig(node)
	if err != nil {
		return nil, fmt.Errorf("webhook node %q: %w", node.ID, err)
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, fmt.Errorf("webhook node %q: url is missing", node.ID)
	}

	body := e.buildWebhookBody(cfg, nc)
	statusCode, respBody, err := e.webhook.Post(ctx, cfg.URL, cfg.Headers, body, cfg.Secret)
	snippet := respBody
	if len(snippet) > maxWebhookOutputBody {
		snippet = snippet[:maxWebhookOutputBody]
	}
	if err != nil {
		return nil, fmt.Errorf("webhook node %q: request failed: %w", node.ID, err)
	}
	if statusCode < 200 || statusCode > 299 {
		return nil, fmt.Errorf("webhook node %q: endpoint returned status %d: %s", node.ID, statusCode, snippet)
	}
	return &nodeResult{Output: map[string]any{"status_code": statusCode, "body": snippet}}, nil
}

// buildWebhookBody renders the request body: the config's {{ var }} template
// when the author supplied one, otherwise the default event document.
func (e *Engine) buildWebhookBody(cfg models.WebhookConfig, nc *nodeContext) []byte {
	if strings.TrimSpace(cfg.Body) != "" {
		return []byte(renderPlaceholders(cfg.Body, e.buildEmailData(nc)))
	}
	payload := map[string]any{
		"event":          "automation.webhook",
		"automation_id":  nc.automation.ID,
		"automation_name": nc.automation.Name,
		"journey_id":     nc.journey.ID,
		"contact_email":  nc.journey.Email,
		"data":           nc.properties,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		// The payload is built from plain scalars; marshaling cannot fail
		// except through a pathological journey context, which then degrades
		// to an empty object.
		return []byte("{}")
	}
	return b
}

// placeholderRe matches {{ identifier }} references (dotted paths allowed) in a
// webhook body template.
var placeholderRe = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}`)

// renderPlaceholders substitutes {{ key }} with the string form of data[key];
// unknown or nil keys render as the empty string.
func renderPlaceholders(tmpl string, data map[string]any) string {
	return placeholderRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		key := placeholderRe.FindStringSubmatch(match)[1]
		v, ok := data[key]
		if !ok || v == nil {
			return ""
		}
		return fmt.Sprint(v)
	})
}
