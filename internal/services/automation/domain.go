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

// Package automation implements the automation workflow domain: parsing,
// validating and walking the node graph persisted on an Automation row, plus
// the subscriber condition evaluation shared by triggers and filter nodes.
//
// Wire format (Nodes JSONB): a flat array of {id, type, config, next_node_id,
// position} with no edges table. Linear nodes point at their successor via
// next_node_id; branch nodes carry their outgoing edges inside Config
// ("continue_node_id"/"exit_node_id" for filters, "variants[].next_node_id"
// for ab_tests, "active_node_id"/"non_active_node_id"/"not_in_list_node_id"
// for list_status). root_node_id references the single trigger node.
package automation

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/goposta/posta/internal/models"
)

// Node types (AutomationNode.Type), aliased to the canonical values in
// internal/models so the wire format has a single source of truth.
const (
	NodeTypeTrigger        = models.AutomationNodeTrigger
	NodeTypeDelay          = models.AutomationNodeDelay
	NodeTypeEmail          = models.AutomationNodeEmail
	NodeTypeFilter         = models.AutomationNodeFilter
	NodeTypeABTest         = models.AutomationNodeABTest
	NodeTypeListStatus     = models.AutomationNodeListStatus
	NodeTypeAddToList      = models.AutomationNodeAddToList
	NodeTypeRemoveFromList = models.AutomationNodeRemoveFromList
	NodeTypeWebhook        = models.AutomationNodeWebhook
)

// Automation row statuses (Automation.Status).
const (
	AutomationStatusDraft  = models.AutomationStatusDraft
	AutomationStatusLive   = models.AutomationStatusLive
	AutomationStatusPaused = models.AutomationStatusPaused
)

// Journey statuses (AutomationJourney.Status).
const (
	JourneyStatusActive    = models.AutomationJourneyStatusActive
	JourneyStatusCompleted = models.AutomationJourneyStatusCompleted
	JourneyStatusExited    = models.AutomationJourneyStatusExited
	JourneyStatusFailed    = models.AutomationJourneyStatusFailed
)

// Trigger event kinds (AutomationTriggerConfig.EventKind), aliased to the
// canonical values in internal/models.
//
// email.sent and email.delivered are deliberately NOT trigger kinds: email
// nodes inside an automation emit those events, so a workflow triggered by its
// own sends would enroll the contact again forever (loop guard).
const (
	EventKindListSubscribed      = models.AutomationEventListSubscribed
	EventKindContactCreated      = models.AutomationEventContactCreated
	EventKindContactUnsubscribed = models.AutomationEventUnsubscribed
	EventKindEmailOpened         = models.AutomationEventEmailOpened
	EventKindEmailClicked        = models.AutomationEventEmailClicked
	EventKindEmailBounced        = models.AutomationEventEmailBounced
	EventKindCustom              = models.AutomationEventCustom
)

// Trigger frequencies (AutomationTriggerConfig.Frequency).
const (
	FrequencyOnce      = models.AutomationFrequencyOnce
	FrequencyEveryTime = models.AutomationFrequencyEveryTime
)

// Branch names passed to WorkflowDoc.NextNodeFrom.
const (
	BranchContinue  = "continue"
	BranchExit      = "exit"
	BranchActive    = "active"
	BranchNonActive = "non_active"
	BranchNotInList = "not_in_list"
)

var (
	validNodeTypes = map[string]bool{
		NodeTypeTrigger:        true,
		NodeTypeDelay:          true,
		NodeTypeEmail:          true,
		NodeTypeFilter:         true,
		NodeTypeABTest:         true,
		NodeTypeListStatus:     true,
		NodeTypeAddToList:      true,
		NodeTypeRemoveFromList: true,
		NodeTypeWebhook:        true,
	}
	validEventKinds = map[string]bool{
		EventKindListSubscribed:      true,
		EventKindContactCreated:      true,
		EventKindContactUnsubscribed: true,
		EventKindEmailOpened:         true,
		EventKindEmailClicked:        true,
		EventKindEmailBounced:        true,
		EventKindCustom:              true,
	}
	validFrequencies = map[string]bool{
		FrequencyOnce:      true,
		FrequencyEveryTime: true,
	}
	validDelayUnits = map[string]bool{
		models.AutomationDelayMinutes: true,
		models.AutomationDelayHours:   true,
		models.AutomationDelayDays:    true,
		models.AutomationDelayWeeks:   true,
	}
	// Operators supported on the direct subscriber fields (email/name/status).
	directFieldOperators = map[string]bool{
		"equals":     true,
		"not_equals": true,
		"contains":   true,
	}
	// Operators supported on custom_fields.<key> rules.
	customFieldOperators = map[string]bool{
		"equals":     true,
		"not_equals": true,
		"contains":   true,
		"exists":     true,
		"not_exists": true,
	}
)

// WorkflowDoc is a parsed Automation.Nodes payload. It is a thin, read-only
// view: lookups, typed config access, validation and graph walking.
type WorkflowDoc struct {
	// RootNodeID is the Automation row's root_node_id; it must reference the
	// single trigger node.
	RootNodeID string
	Nodes      []*models.AutomationNode
}

// ParseWorkflow decodes the Nodes JSONB of an Automation row into a WorkflowDoc.
// Parsing is lenient (shape problems are reported by Validate); only malformed
// JSON fails here.
func ParseWorkflow(rootNodeID string, nodesJSON string) (*WorkflowDoc, error) {
	var nodes []*models.AutomationNode
	if err := json.Unmarshal([]byte(nodesJSON), &nodes); err != nil {
		return nil, fmt.Errorf("invalid automation nodes JSON: %w", err)
	}
	return &WorkflowDoc{RootNodeID: rootNodeID, Nodes: nodes}, nil
}

// Node returns the node with the given id, or nil when absent.
func (w *WorkflowDoc) Node(id string) *models.AutomationNode {
	if w == nil {
		return nil
	}
	for _, n := range w.Nodes {
		if n != nil && n.ID == id {
			return n
		}
	}
	return nil
}

// Trigger returns the first trigger node, or nil when the workflow has none.
// Validation enforces that there is exactly one.
func (w *WorkflowDoc) Trigger() *models.AutomationNode {
	if w == nil {
		return nil
	}
	for _, n := range w.Nodes {
		if n != nil && n.Type == NodeTypeTrigger {
			return n
		}
	}
	return nil
}

// parseNodeConfig round-trips a node's raw config map through JSON into a
// typed config struct, returning an error when the config has the wrong shape.
func parseNodeConfig[T any](node *models.AutomationNode) (*T, error) {
	if node == nil {
		return nil, fmt.Errorf("node is nil")
	}
	raw, err := json.Marshal(node.Config)
	if err != nil {
		return nil, fmt.Errorf("node %q: config is not JSON-serializable: %w", node.ID, err)
	}
	var cfg T
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("node %q: invalid %s config: %w", node.ID, node.Type, err)
	}
	return &cfg, nil
}

// TriggerConfig parses a trigger node's config into the trigger fields.
// Extra keys on the config (such as "description") are ignored.
func (w *WorkflowDoc) TriggerConfig(node *models.AutomationNode) (models.AutomationTriggerConfig, error) {
	cfg, err := parseNodeConfig[models.AutomationTriggerConfig](node)
	if err != nil {
		return models.AutomationTriggerConfig{}, err
	}
	return *cfg, nil
}

// TriggerNodeConfig parses the root trigger node's config including its
// builder-only Description field.
func (w *WorkflowDoc) TriggerNodeConfig(node *models.AutomationNode) (models.TriggerNodeConfig, error) {
	cfg, err := parseNodeConfig[models.TriggerNodeConfig](node)
	if err != nil {
		return models.TriggerNodeConfig{}, err
	}
	return *cfg, nil
}

// DelayConfig parses a delay node's config.
func (w *WorkflowDoc) DelayConfig(node *models.AutomationNode) (models.DelayConfig, error) {
	cfg, err := parseNodeConfig[models.DelayConfig](node)
	if err != nil {
		return models.DelayConfig{}, err
	}
	return *cfg, nil
}

// EmailConfig parses an email node's config.
func (w *WorkflowDoc) EmailConfig(node *models.AutomationNode) (models.EmailConfig, error) {
	cfg, err := parseNodeConfig[models.EmailConfig](node)
	if err != nil {
		return models.EmailConfig{}, err
	}
	return *cfg, nil
}

// FilterConfig parses a filter node's config.
func (w *WorkflowDoc) FilterConfig(node *models.AutomationNode) (models.FilterConfig, error) {
	cfg, err := parseNodeConfig[models.FilterConfig](node)
	if err != nil {
		return models.FilterConfig{}, err
	}
	return *cfg, nil
}

// ABTestConfig parses an ab_test node's config.
func (w *WorkflowDoc) ABTestConfig(node *models.AutomationNode) (models.ABTestConfig, error) {
	cfg, err := parseNodeConfig[models.ABTestConfig](node)
	if err != nil {
		return models.ABTestConfig{}, err
	}
	return *cfg, nil
}

// ListStatusConfig parses a list_status node's config.
func (w *WorkflowDoc) ListStatusConfig(node *models.AutomationNode) (models.ListStatusConfig, error) {
	cfg, err := parseNodeConfig[models.ListStatusConfig](node)
	if err != nil {
		return models.ListStatusConfig{}, err
	}
	return *cfg, nil
}

// AddToListConfig parses an add_to_list node's config.
func (w *WorkflowDoc) AddToListConfig(node *models.AutomationNode) (models.AddToListConfig, error) {
	cfg, err := parseNodeConfig[models.AddToListConfig](node)
	if err != nil {
		return models.AddToListConfig{}, err
	}
	return *cfg, nil
}

// RemoveFromListConfig parses a remove_from_list node's config.
func (w *WorkflowDoc) RemoveFromListConfig(node *models.AutomationNode) (models.RemoveFromListConfig, error) {
	cfg, err := parseNodeConfig[models.RemoveFromListConfig](node)
	if err != nil {
		return models.RemoveFromListConfig{}, err
	}
	return *cfg, nil
}

// WebhookConfig parses a webhook node's config.
func (w *WorkflowDoc) WebhookConfig(node *models.AutomationNode) (models.WebhookConfig, error) {
	cfg, err := parseNodeConfig[models.WebhookConfig](node)
	if err != nil {
		return models.WebhookConfig{}, err
	}
	return *cfg, nil
}

// NextNodeFrom resolves the successor of a node for the given branch.
//
//   - linear nodes (trigger, delay, email, add_to_list, remove_from_list,
//     webhook): the branch is ignored, NextNodeID is returned;
//   - filter: BranchContinue -> continue_node_id, BranchExit -> exit_node_id;
//   - ab_test: the branch is the variant id -> that variant's next_node_id;
//   - list_status: BranchActive/BranchNonActive/BranchNotInList -> the
//     matching config field.
//
// An empty result means the journey completes at this node. An unknown branch
// (or an unparseable config, which Validate catches) also returns "".
func (w *WorkflowDoc) NextNodeFrom(node *models.AutomationNode, branch string) string {
	if node == nil {
		return ""
	}
	switch node.Type {
	case NodeTypeFilter:
		switch branch {
		case BranchContinue:
			return cfgString(node.Config, "continue_node_id")
		case BranchExit:
			return cfgString(node.Config, "exit_node_id")
		}
		return ""
	case NodeTypeABTest:
		for _, raw := range cfgArray(node.Config, "variants") {
			variant, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if cfgString(variant, "id") == branch {
				return cfgString(variant, "next_node_id")
			}
		}
		return ""
	case NodeTypeListStatus:
		switch branch {
		case BranchActive:
			return cfgString(node.Config, "active_node_id")
		case BranchNonActive:
			return cfgString(node.Config, "non_active_node_id")
		case BranchNotInList:
			return cfgString(node.Config, "not_in_list_node_id")
		}
		return ""
	default:
		if node.NextNodeID != nil {
			return *node.NextNodeID
		}
		return ""
	}
}

// Validate checks the workflow against the builder/engine contract and returns
// human-readable problems; an empty result means the workflow is valid.
//
// listID is the Automation row's ListID: email nodes send to it, so it must be
// set when the workflow contains an email node. The list.subscribed trigger
// accepts its own config list_id or, failing that, the automation-level list.
func (w *WorkflowDoc) Validate(listID *uint) []string {
	if w == nil {
		return []string{"workflow is not initialized"}
	}
	if len(w.Nodes) == 0 {
		return []string{"workflow has no nodes: add a trigger node first"}
	}

	var errs []string

	// Node inventory: missing/duplicate ids and unknown types.
	ids := make(map[string]bool, len(w.Nodes))
	var triggers []*models.AutomationNode
	for _, n := range w.Nodes {
		if n == nil {
			errs = append(errs, "workflow contains an empty node")
			continue
		}
		if n.ID == "" {
			errs = append(errs, "a node is missing an id: every node needs a unique id")
			continue
		}
		if ids[n.ID] {
			errs = append(errs, fmt.Sprintf("node %q: duplicate node id: ids must be unique", n.ID))
			continue
		}
		ids[n.ID] = true
		if !validNodeTypes[n.Type] {
			errs = append(errs, fmt.Sprintf("node %q: unknown node type %q", n.ID, n.Type))
		}
		if n.Type == NodeTypeTrigger {
			triggers = append(triggers, n)
		}
	}

	// Exactly one trigger node and it must be the root.
	switch {
	case len(triggers) == 0:
		errs = append(errs, "workflow has no trigger node: add exactly one trigger node and make it the root")
	case len(triggers) > 1:
		errs = append(errs, fmt.Sprintf("workflow has %d trigger nodes: exactly one trigger node is allowed", len(triggers)))
	}
	root := w.Node(w.RootNodeID)
	switch {
	case w.RootNodeID == "":
		errs = append(errs, "root_node_id is empty: it must reference the trigger node")
	case root == nil:
		errs = append(errs, fmt.Sprintf("root_node_id %q does not reference an existing node", w.RootNodeID))
	case root.Type != NodeTypeTrigger:
		errs = append(errs, fmt.Sprintf("root_node_id %q references a %q node: the root must be the trigger node", w.RootNodeID, root.Type))
	}

	// Trigger config (event kind, list, frequency, conditions).
	if len(triggers) == 1 {
		errs = append(errs, w.validateTriggerNode(triggers[0], listID)...)
	}

	// Per-node config validation.
	for _, n := range w.Nodes {
		if n == nil || n.ID == "" || !validNodeTypes[n.Type] {
			continue
		}
		switch n.Type {
		case NodeTypeTrigger:
			// Validated above (its rules depend on the trigger/root counts).
		case NodeTypeDelay:
			if duration, ok := cfgNumber(n.Config, "duration"); !ok || duration <= 0 {
				errs = append(errs, fmt.Sprintf("delay node %q: duration must be a number greater than 0", n.ID))
			}
			if unit := cfgString(n.Config, "unit"); !validDelayUnits[unit] {
				errs = append(errs, fmt.Sprintf("delay node %q: unknown unit %q: use minutes, hours, days or weeks", n.ID, unit))
			}
		case NodeTypeEmail:
			if listID == nil || *listID == 0 {
				errs = append(errs, fmt.Sprintf("email node %q: the automation must have a list: email nodes send to the automation's list", n.ID))
			}
			if templateID, ok := cfgUint(n.Config, "template_id"); !ok || templateID == 0 {
				errs = append(errs, fmt.Sprintf("email node %q: template_id is required", n.ID))
			}
		case NodeTypeFilter:
			conditions := cfgArray(n.Config, "conditions")
			if len(conditions) == 0 {
				errs = append(errs, fmt.Sprintf("filter node %q: at least one condition is required", n.ID))
			}
			for i, raw := range conditions {
				rule, ok := raw.(map[string]any)
				if !ok {
					errs = append(errs, fmt.Sprintf("filter node %q: condition #%d is malformed", n.ID, i+1))
					continue
				}
				if problem := filterRuleProblem(cfgString(rule, "field"), cfgString(rule, "operator"), rule["value"]); problem != "" {
					errs = append(errs, fmt.Sprintf("filter node %q: condition #%d: %s", n.ID, i+1, problem))
				}
			}
		case NodeTypeABTest:
			errs = append(errs, w.validateABTestNode(n)...)
		case NodeTypeListStatus:
			if listIDValue, ok := cfgUint(n.Config, "list_id"); !ok || listIDValue == 0 {
				errs = append(errs, fmt.Sprintf("list_status node %q: list_id is required", n.ID))
			}
			targets := 0
			for _, key := range []string{"active_node_id", "non_active_node_id", "not_in_list_node_id"} {
				if cfgString(n.Config, key) != "" {
					targets++
				}
			}
			if targets == 0 {
				errs = append(errs, fmt.Sprintf("list_status node %q: at least one branch target is required (active_node_id, non_active_node_id or not_in_list_node_id)", n.ID))
			}
		case NodeTypeAddToList:
			if listIDValue, ok := cfgUint(n.Config, "list_id"); !ok || listIDValue == 0 {
				errs = append(errs, fmt.Sprintf("add_to_list node %q: list_id is required", n.ID))
			}
		case NodeTypeRemoveFromList:
			if listIDValue, ok := cfgUint(n.Config, "list_id"); !ok || listIDValue == 0 {
				errs = append(errs, fmt.Sprintf("remove_from_list node %q: list_id is required", n.ID))
			}
		case NodeTypeWebhook:
			rawURL := cfgString(n.Config, "url")
			if rawURL == "" {
				errs = append(errs, fmt.Sprintf("webhook node %q: url is required", n.ID))
				break
			}
			parsed, err := url.Parse(rawURL)
			if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				errs = append(errs, fmt.Sprintf("webhook node %q: url must be a valid http or https URL", n.ID))
			}
		}
	}

	// Every referenced target must resolve, and nothing may target the trigger.
	for _, n := range w.Nodes {
		if n == nil || n.ID == "" {
			continue
		}
		for _, target := range w.nodeTargets(n) {
			resolved := w.Node(target)
			if resolved == nil {
				errs = append(errs, fmt.Sprintf("node %q: references missing node %q", n.ID, target))
				continue
			}
			if resolved.Type == NodeTypeTrigger {
				errs = append(errs, fmt.Sprintf("node %q: cannot target the trigger node %q", n.ID, target))
			}
		}
	}

	// Reachability: every node must be reachable from the root.
	for _, orphan := range w.BFSOrphans() {
		errs = append(errs, fmt.Sprintf("node %q is not reachable from the trigger: connect it to the flow or remove it", orphan))
	}

	// Unbounded loops: an edge back into the walk's own path.
	if cycle := w.ReachableCycle(); len(cycle) > 0 {
		errs = append(errs, fmt.Sprintf("workflow contains an infinite loop: %s", strings.Join(cycle, " -> ")))
	}

	if errs == nil {
		errs = []string{}
	}
	return errs
}

// validateTriggerNode checks the trigger config: event kind, list requirement,
// frequency and condition shapes.
func (w *WorkflowDoc) validateTriggerNode(node *models.AutomationNode, listID *uint) []string {
	cfg, err := w.TriggerConfig(node)
	if err != nil {
		return []string{fmt.Sprintf("trigger node %q: %v", node.ID, err)}
	}
	var errs []string
	if !validEventKinds[cfg.EventKind] {
		errs = append(errs, fmt.Sprintf("trigger node %q: unknown event_kind %q", node.ID, cfg.EventKind))
	}
	switch cfg.EventKind {
	case EventKindListSubscribed:
		if (cfg.ListID == nil || *cfg.ListID == 0) && (listID == nil || *listID == 0) {
			errs = append(errs, fmt.Sprintf("trigger node %q: event_kind %q requires a list_id", node.ID, EventKindListSubscribed))
		}
	case EventKindCustom:
		if strings.TrimSpace(cfg.CustomEventName) == "" {
			errs = append(errs, fmt.Sprintf("trigger node %q: event_kind %q requires a custom_event_name", node.ID, EventKindCustom))
		}
	}
	if !validFrequencies[cfg.Frequency] {
		errs = append(errs, fmt.Sprintf("trigger node %q: unknown frequency %q: use %q or %q", node.ID, cfg.Frequency, FrequencyOnce, FrequencyEveryTime))
	}
	for i, rule := range cfg.Conditions {
		if problem := filterRuleProblem(rule.Field, rule.Operator, rule.Value); problem != "" {
			errs = append(errs, fmt.Sprintf("trigger node %q: condition #%d: %s", node.ID, i+1, problem))
		}
	}
	return errs
}

// validateABTestNode checks variant count, unique ids, numeric in-range
// weights summing to exactly 100, and that every variant points at a node.
func (w *WorkflowDoc) validateABTestNode(node *models.AutomationNode) []string {
	variants := cfgArray(node.Config, "variants")
	var errs []string
	if len(variants) < 2 {
		errs = append(errs, fmt.Sprintf("ab_test node %q: at least 2 variants are required", node.ID))
	}
	sum := 0.0
	weightsOK := len(variants) > 0
	seenIDs := map[string]bool{}
	for i, raw := range variants {
		variant, ok := raw.(map[string]any)
		if !ok {
			errs = append(errs, fmt.Sprintf("ab_test node %q: variant #%d is malformed", node.ID, i+1))
			weightsOK = false
			continue
		}
		id := cfgString(variant, "id")
		label := id
		if label == "" {
			label = fmt.Sprintf("#%d", i+1)
		}
		if id == "" {
			errs = append(errs, fmt.Sprintf("ab_test node %q: variant %s is missing an id", node.ID, label))
		} else if seenIDs[id] {
			errs = append(errs, fmt.Sprintf("ab_test node %q: duplicate variant id %q", node.ID, id))
		} else {
			seenIDs[id] = true
		}
		if weight, ok := cfgNumber(variant, "weight"); !ok {
			errs = append(errs, fmt.Sprintf("ab_test node %q: variant %s is missing a numeric weight", node.ID, label))
			weightsOK = false
		} else {
			if weight < 0 || weight > 100 {
				errs = append(errs, fmt.Sprintf("ab_test node %q: variant %s weight must be between 0 and 100", node.ID, label))
				weightsOK = false
			}
			sum += weight
		}
		if cfgString(variant, "next_node_id") == "" {
			errs = append(errs, fmt.Sprintf("ab_test node %q: variant %s has no next_node_id: every variant must point at a node", node.ID, label))
		}
	}
	if weightsOK && len(variants) >= 2 && math.Abs(sum-100) > 1e-9 {
		errs = append(errs, fmt.Sprintf("ab_test node %q: variant weights must sum to exactly 100 (got %s)", node.ID, strconv.FormatFloat(sum, 'f', -1, 64)))
	}
	return errs
}

// filterRuleProblem returns a human-readable problem with a single filter
// rule, or "" when the rule is well-formed. It mirrors exactly the operators
// EvaluateConditions supports, so the builder cannot save rules that would
// silently never match.
func filterRuleProblem(field, operator string, value any) string {
	field = strings.TrimSpace(field)
	operator = strings.TrimSpace(operator)
	if field == "" {
		return "field is required"
	}
	if operator == "" {
		return "operator is required"
	}
	switch field {
	case "email", "name", "status":
		if !directFieldOperators[operator] {
			return fmt.Sprintf("operator %q is not supported for field %q: use equals, not_equals or contains", operator, field)
		}
		return ""
	case "in_list":
		// The operator is irrelevant for membership; the value must be a list id.
		if _, ok := coerceUint(value); !ok {
			return "in_list needs a numeric list id as its value"
		}
		return ""
	}
	if key, ok := strings.CutPrefix(field, "custom_fields."); ok && key != "" {
		if !customFieldOperators[operator] {
			return fmt.Sprintf("operator %q is not supported for custom fields: use equals, not_equals, contains, exists or not_exists", operator)
		}
		return ""
	}
	return fmt.Sprintf("unknown field %q", field)
}

// cfgString reads a string-valued key from a raw node config map.
func cfgString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	if s, ok := cfg[key].(string); ok {
		return s
	}
	return ""
}

// cfgNumber reads a number-valued key from a raw node config map (JSON
// decodes numbers as float64).
func cfgNumber(cfg map[string]any, key string) (float64, bool) {
	if cfg == nil {
		return 0, false
	}
	switch t := cfg[key].(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

// cfgUint reads an unsigned integer-valued key from a raw node config map.
func cfgUint(cfg map[string]any, key string) (uint, bool) {
	if cfg == nil {
		return 0, false
	}
	v, ok := cfg[key]
	if !ok {
		return 0, false
	}
	return coerceUint(v)
}

// cfgArray reads an array-valued key from a raw node config map.
func cfgArray(cfg map[string]any, key string) []any {
	if cfg == nil {
		return nil
	}
	if a, ok := cfg[key].([]any); ok {
		return a
	}
	return nil
}
