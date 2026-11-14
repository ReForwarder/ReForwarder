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
	"sort"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/conditions"
)

// NodesByType returns every node of the given type, in stored order.
func (w *WorkflowDoc) NodesByType(nodeType string) []*models.AutomationNode {
	if w == nil {
		return nil
	}
	var out []*models.AutomationNode
	for _, n := range w.Nodes {
		if n != nil && n.Type == nodeType {
			out = append(out, n)
		}
	}
	return out
}

// nodeTargets returns the deduplicated outgoing edge targets of a node, in a
// fixed order, reading branch targets out of the raw config map. Linear nodes
// use NextNodeID; branch nodes route via config fields only, so their
// NextNodeID is not an edge here either (matching NextNodeFrom at runtime).
func (w *WorkflowDoc) nodeTargets(node *models.AutomationNode) []string {
	if node == nil {
		return nil
	}
	var targets []string
	switch node.Type {
	case NodeTypeFilter:
		targets = append(targets,
			cfgString(node.Config, "continue_node_id"),
			cfgString(node.Config, "exit_node_id"))
	case NodeTypeABTest:
		for _, raw := range cfgArray(node.Config, "variants") {
			variant, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			targets = append(targets, cfgString(variant, "next_node_id"))
		}
	case NodeTypeListStatus:
		targets = append(targets,
			cfgString(node.Config, "active_node_id"),
			cfgString(node.Config, "non_active_node_id"),
			cfgString(node.Config, "not_in_list_node_id"))
	default:
		if node.NextNodeID != nil {
			targets = append(targets, *node.NextNodeID)
		}
	}
	seen := make(map[string]bool, len(targets))
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// reachableFromRoot returns the set of node ids reachable from the root via
// outgoing edges. Nodes referenced but missing are not followed.
func (w *WorkflowDoc) reachableFromRoot() map[string]bool {
	reach := map[string]bool{}
	if w == nil {
		return reach
	}
	if w.RootNodeID == "" || w.Node(w.RootNodeID) == nil {
		return reach
	}
	reach[w.RootNodeID] = true
	queue := []string{w.RootNodeID}
	for len(queue) > 0 {
		node := w.Node(queue[0])
		queue = queue[1:]
		if node == nil {
			continue
		}
		for _, target := range w.nodeTargets(node) {
			if target == "" || reach[target] || w.Node(target) == nil {
				continue
			}
			reach[target] = true
			queue = append(queue, target)
		}
	}
	return reach
}

// BFSOrphans returns the ids of nodes NOT reachable from the root, sorted.
func (w *WorkflowDoc) BFSOrphans() []string {
	if w == nil {
		return nil
	}
	reach := w.reachableFromRoot()
	var orphans []string
	for _, n := range w.Nodes {
		if n == nil || n.ID == "" {
			continue
		}
		if !reach[n.ID] {
			orphans = append(orphans, n.ID)
		}
	}
	sort.Strings(orphans)
	return orphans
}

// ReachableCycle returns the first cycle reachable from the root, as the node
// ids along the loop with the entry node repeated at the end (e.g.
// "b -> c -> d -> b"), or nil when the reachable subgraph is acyclic.
//
// Branches that converge are legitimate: only an edge back to a node that is
// already on the current walk's path counts as a cycle (DFS with an on-path
// set), i.e. a walk that can revisit a node forever with no exit.
func (w *WorkflowDoc) ReachableCycle() []string {
	if w == nil {
		return nil
	}
	if w.RootNodeID == "" || w.Node(w.RootNodeID) == nil {
		return nil
	}
	visited := map[string]bool{}
	onPath := map[string]int{}
	var path []string
	var cycle []string

	var dfs func(id string) bool
	dfs = func(id string) bool {
		visited[id] = true
		onPath[id] = len(path)
		path = append(path, id)
		if node := w.Node(id); node != nil {
			for _, target := range w.nodeTargets(node) {
				if target == "" || w.Node(target) == nil {
					continue
				}
				if start, onCurrentPath := onPath[target]; onCurrentPath {
					cycle = append(append([]string{}, path[start:]...), target)
					return true
				}
				if !visited[target] && dfs(target) {
					return true
				}
			}
		}
		delete(onPath, id)
		path = path[:len(path)-1]
		return false
	}
	dfs(w.RootNodeID)
	return cycle
}

// RequiredListIDs returns the deduplicated list ids the workflow depends on:
// the trigger's list_id, the list_status / add_to_list / remove_from_list
// nodes' list_ids, and a list_id carried on an email node config when present.
// Email nodes normally send to Automation.ListID (no per-node list), so the
// caller must validate that separately.
func (w *WorkflowDoc) RequiredListIDs() []uint {
	if w == nil {
		return nil
	}
	seen := map[uint]bool{}
	var out []uint
	add := func(id uint) {
		if id == 0 || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	if trigger := w.Trigger(); trigger != nil {
		if id, ok := cfgUint(trigger.Config, "list_id"); ok {
			add(id)
		}
	}
	for _, n := range w.Nodes {
		if n == nil {
			continue
		}
		switch n.Type {
		case NodeTypeEmail, NodeTypeListStatus, NodeTypeAddToList, NodeTypeRemoveFromList:
			if id, ok := cfgUint(n.Config, "list_id"); ok {
				add(id)
			}
		}
	}
	return out
}

// DefaultNodeConfig returns the initial config the builder writes when a node
// of the given type is dropped onto the canvas. Unknown types return nil.
func DefaultNodeConfig(nodeType string) map[string]any {
	switch nodeType {
	case NodeTypeTrigger:
		return map[string]any{
			"description": "",
			"event_kind":  EventKindListSubscribed,
			"list_id":     nil,
			"frequency":   FrequencyOnce,
			"conditions":  []any{},
		}
	case NodeTypeDelay:
		return map[string]any{
			"duration":    1,
			"unit":        "hours",
			"description": "",
		}
	case NodeTypeEmail:
		// template_id must be chosen explicitly; Validate requires it.
		return map[string]any{}
	case NodeTypeFilter:
		return map[string]any{
			"conditions":       []any{},
			"continue_node_id": "",
			"exit_node_id":     "",
		}
	case NodeTypeABTest:
		return map[string]any{
			"variants": []any{
				map[string]any{"id": "a", "name": "A", "weight": 50, "next_node_id": ""},
				map[string]any{"id": "b", "name": "B", "weight": 50, "next_node_id": ""},
			},
		}
	case NodeTypeListStatus:
		return map[string]any{
			"list_id":             nil,
			"active_node_id":      "",
			"non_active_node_id":  "",
			"not_in_list_node_id": "",
		}
	case NodeTypeAddToList:
		return map[string]any{"list_id": nil}
	case NodeTypeRemoveFromList:
		return map[string]any{"list_id": nil}
	case NodeTypeWebhook:
		return map[string]any{
			"url":     "",
			"method":  "POST",
			"headers": map[string]any{},
			"body":    `{"contact_email":"{{contact_email}}"}`,
			"secret":  "",
		}
	default:
		return nil
	}
}

// EvaluateConditions reports whether a subscriber satisfies all conditions
// (AND-combined) — the SF-103 unified-surface delegate: the implementation
// moved verbatim to the conditions package (EvaluateFilterRules) and this
// wrapper keeps the automation package's own name (and its tests) working
// unchanged. See internal/services/conditions/memory.go for the contract.
func EvaluateConditions(ruleSet []models.FilterRule, sub *models.Subscriber, listChecker func(listID uint) bool) bool {
	return conditions.EvaluateFilterRules(ruleSet, sub, listChecker)
}
