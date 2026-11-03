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

package models

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// Automation lifecycle statuses.
type AutomationStatus string

// Journey lifecycle statuses.
type AutomationJourneyStatus string

// Node-run actions recorded on a journey's timeline.
type AutomationNodeAction string

// The status and action constants are untyped strings so they bind directly to
// the plain string fields of the automation models without conversion.
const (
	AutomationStatusDraft  = "draft"
	AutomationStatusLive   = "live"
	AutomationStatusPaused = "paused"
)

const (
	AutomationJourneyStatusActive    = "active"
	AutomationJourneyStatusCompleted = "completed"
	AutomationJourneyStatusExited    = "exited"
	AutomationJourneyStatusFailed    = "failed"
)

const (
	AutomationRunActionEntered    = "entered"
	AutomationRunActionProcessing = "processing"
	AutomationRunActionCompleted  = "completed"
	AutomationRunActionFailed     = "failed"
)

// Node types of the workflow graph. The trigger node is the root every journey
// starts at; the remaining eight are the executor nodes.
const (
	AutomationNodeTrigger        = "trigger"
	AutomationNodeDelay          = "delay"
	AutomationNodeEmail          = "email"
	AutomationNodeFilter         = "filter"
	AutomationNodeABTest         = "ab_test"
	AutomationNodeListStatus     = "list_status"
	AutomationNodeAddToList      = "add_to_list"
	AutomationNodeRemoveFromList = "remove_from_list"
	AutomationNodeWebhook        = "webhook"
)

// Trigger event kinds matched against AutomationTriggerConfig.EventKind.
// Custom ingested events use AutomationEventCustom; the specific name is
// carried in CustomEventName.
const (
	AutomationEventListSubscribed = "list.subscribed"
	AutomationEventContactCreated = "contact.created"
	AutomationEventUnsubscribed   = "contact.unsubscribed"
	AutomationEventEmailOpened    = "email.opened"
	AutomationEventEmailClicked   = "email.clicked"
	AutomationEventEmailBounced   = "email.bounced"
	AutomationEventCustom         = "custom"
)

// Trigger frequency: once dedups per (automation, subscriber) through the
// trigger log; every_time fires on every matching event.
const (
	AutomationFrequencyOnce      = "once"
	AutomationFrequencyEveryTime = "every_time"
)

// Delay duration units accepted by DelayConfig.
const (
	AutomationDelayMinutes = "minutes"
	AutomationDelayHours   = "hours"
	AutomationDelayDays    = "days"
	AutomationDelayWeeks   = "weeks"
)

// Default HTTP method for webhook nodes whose config leaves Method empty.
const AutomationWebhookMethodPost = "POST"

// Journey exit reasons. Journeys may exit for further reasons; these are the
// ones the engine itself produces.
const (
	AutomationExitReasonContactMissing    = "contact_missing"
	AutomationExitReasonNodeDeleted       = "automation_node_deleted"
	AutomationExitReasonAutomationDeleted = "automation_deleted"
)

// Automation is a workflow builder document: a trigger, a JSONB node graph and
// aggregate counters. Validation of the graph lives in the automation service;
// the model only stores it.
type Automation struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	UUID        string `json:"uuid" gorm:"type:uuid;default:gen_random_uuid();uniqueIndex"`
	UserID      uint   `json:"user_id" gorm:"index;not null"`
	WorkspaceID *uint  `json:"workspace_id,omitempty" gorm:"index"`
	Name        string `json:"name" gorm:"size:255;not null"`
	Status      string `json:"status" gorm:"size:12;not null;default:draft;index"` // draft|live|paused
	ListID      *uint  `json:"list_id,omitempty" gorm:"index"`
	// ExitOnReply is reserved for v1.1 (no inbound threading yet).
	ExitOnReply   bool   `json:"exit_on_reply" gorm:"default:false"`
	TriggerConfig string `json:"trigger_config" gorm:"type:jsonb;not null;default:'{}'"`
	// Nodes holds the workflow graph as a JSONB array of AutomationNode.
	Nodes string `json:"nodes" gorm:"type:jsonb;not null;default:'[]'"`
	// RootNodeID is the trigger node every journey starts from.
	RootNodeID string `json:"root_node_id" gorm:"size:120"`
	Stats      string `json:"stats" gorm:"type:jsonb;not null;default:'{\"enrolled\":0,\"completed\":0,\"exited\":0,\"failed\":0}'"`
	Version    int    `json:"version" gorm:"default:1"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName overrides GORM's default table naming.
func (Automation) TableName() string { return "automations" }

// AutomationNode is the wire shape of one node inside Automation.Nodes JSONB.
// It has no table of its own; the typed config structs below decode its
// Config map per node type.
type AutomationNode struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	Config map[string]any `json:"config,omitempty"`
	// NextNodeID is the linear successor; branch nodes (filter, ab_test,
	// list_status) carry their targets inside Config instead and leave this
	// empty. An empty NextNodeID on a linear node means "journey completes".
	NextNodeID *string `json:"next_node_id,omitempty"`
	Position   struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	} `json:"position"`
}

// AutomationTriggerConfig is the shape marshaled into Automation.TriggerConfig.
type AutomationTriggerConfig struct {
	// EventKind is one of the AutomationEvent* kinds.
	EventKind string `json:"event_kind"`
	ListID    *uint  `json:"list_id,omitempty"`
	// CustomEventName matches the ingest event name when EventKind is a
	// custom-event kind.
	CustomEventName string       `json:"custom_event_name,omitempty"`
	Frequency       string       `json:"frequency"`            // once|every_time
	Conditions      []FilterRule `json:"conditions,omitempty"` // AND-combined vs the Subscriber
}

// TriggerNodeConfig is the Config of the root trigger node; it mirrors
// AutomationTriggerConfig so the builder shows the trigger on the canvas.
type TriggerNodeConfig struct {
	Description     string       `json:"description,omitempty"`
	EventKind       string       `json:"event_kind,omitempty"`
	ListID          *uint        `json:"list_id,omitempty"`
	CustomEventName string       `json:"custom_event_name,omitempty"`
	Frequency       string       `json:"frequency,omitempty"`
	Conditions      []FilterRule `json:"conditions,omitempty"`
}

// DelayConfig parks a journey for a fixed duration before advancing.
type DelayConfig struct {
	Duration    int    `json:"duration"`
	Unit        string `json:"unit"` // minutes|hours|days|weeks
	Description string `json:"description,omitempty"`
}

// EmailConfig sends a template email to the journey contact. Both overrides are
// honored in ReForwarder.
type EmailConfig struct {
	TemplateID      uint   `json:"template_id"`
	SubjectOverride string `json:"subject_override,omitempty"`
	FromOverride    string `json:"from_override,omitempty"`
}

// FilterConfig branches on conditions: continue_node_id when all match,
// exit_node_id otherwise. An empty target means the journey completes there.
type FilterConfig struct {
	Conditions     []FilterRule `json:"conditions"`
	ContinueNodeID string       `json:"continue_node_id,omitempty"`
	ExitNodeID     string       `json:"exit_node_id,omitempty"`
}

// ABVariant is one weighted branch of an ab_test node.
type ABVariant struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Weight     int    `json:"weight"`
	NextNodeID string `json:"next_node_id,omitempty"`
}

// ABTestConfig splits journeys across weighted variants; weights sum to 100.
type ABTestConfig struct {
	Variants []ABVariant `json:"variants"`
}

// ListStatusConfig branches on the contact's membership and status in a list.
type ListStatusConfig struct {
	ListID          uint   `json:"list_id"`
	ActiveNodeID    string `json:"active_node_id,omitempty"`
	NonActiveNodeID string `json:"non_active_node_id,omitempty"`
	NotInListNodeID string `json:"not_in_list_node_id,omitempty"`
}

// AddToListConfig adds the journey contact to a list.
type AddToListConfig struct {
	ListID uint `json:"list_id"`
}

// RemoveFromListConfig removes the journey contact from a list.
type RemoveFromListConfig struct {
	ListID uint `json:"list_id"`
}

// WebhookConfig calls an outbound HTTP endpoint. Body is a JSON template with
// {{contact_email}}-style placeholders; an empty Method means POST.
type WebhookConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"` // default POST
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Secret  string            `json:"secret,omitempty"`
}

// WorkflowStats is the parsed shape of Automation.Stats.
type WorkflowStats struct {
	Enrolled  int64 `json:"enrolled"`
	Completed int64 `json:"completed"`
	Exited    int64 `json:"exited"`
	Failed    int64 `json:"failed"`
}

// DefaultWorkflowStatsJSON is the zero-valued stats document persisted with a
// new automation.
const DefaultWorkflowStatsJSON = `{"enrolled":0,"completed":0,"exited":0,"failed":0}`

// ParseWorkflowStats decodes an Automation.Stats JSON string, falling back to
// the zero document for empty or malformed values.
func ParseWorkflowStats(statsJSON string) WorkflowStats {
	stats := WorkflowStats{}
	if statsJSON == "" {
		return stats
	}
	if err := json.Unmarshal([]byte(statsJSON), &stats); err != nil {
		return WorkflowStats{}
	}
	return stats
}
