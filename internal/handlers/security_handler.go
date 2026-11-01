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
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/security"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// SecurityHandler serves the admin security endpoints: the ban list, detection
// events, the tunable rules and the allowlist. It reads through the security
// repositories and mutates through the engine's BanService, so every change is
// applied to the database, the in-memory ban list and the Redis propagation
// channel in one step. The engine may be nil (POSTA_SECURITY_ENABLED builds
// without wiring it): read endpoints still work off the database, while ban
// mutations answer 503 and allowlist changes persist database-only.
type SecurityHandler struct {
	db          *gorm.DB
	eng         *security.Engine
	bus         *eventbus.EventBus
	banRepo     *repositories.SecurityBanRepository
	eventRepo   *repositories.SecurityEventRepository
	allowRepo   *repositories.SecurityAllowlistRepository
	settingRepo *repositories.SettingRepository
}

// NewSecurityHandler returns a SecurityHandler over the security repositories.
// The settings repository is injected separately via SetSettingRepo, following
// the AdminHandler setter convention.
func NewSecurityHandler(db *gorm.DB, eng *security.Engine, bus *eventbus.EventBus) *SecurityHandler {
	return &SecurityHandler{
		db:        db,
		eng:       eng,
		bus:       bus,
		banRepo:   repositories.NewSecurityBanRepository(db),
		eventRepo: repositories.NewSecurityEventRepository(db),
		allowRepo: repositories.NewSecurityAllowlistRepository(db),
	}
}

// SetSettingRepo wires the settings repository backing the rules endpoints.
func (h *SecurityHandler) SetSettingRepo(repo *repositories.SettingRepository) {
	h.settingRepo = repo
}

// ---------- Requests ----------

type ListSecurityBansRequest struct {
	Page   int    `query:"page" default:"1"`
	Size   int    `query:"size" default:"20"`
	Search string `query:"search"`
	Filter string `query:"filter"` // active (default) | expired | all
}

type CreateSecurityBanRequest struct {
	Body struct {
		IPAddress     string `json:"ip_address" required:"true"`
		Description   string `json:"description"`
		Permanent     bool   `json:"permanent"`
		DurationHours int    `json:"duration_hours"`
		ReasonCode    string `json:"reason_code"`
	} `json:"body"`
}

type SecurityBanRequest struct {
	ID int `param:"id"`
}

type ExtendSecurityBanRequest struct {
	ID   int `param:"id"`
	Body struct {
		Hours int `json:"hours" required:"true"`
	} `json:"body"`
}

type ListSecurityEventsRequest struct {
	Page      int    `query:"page" default:"1"`
	Size      int    `query:"size" default:"20"`
	EventType string `query:"event_type"`
	Severity  string `query:"severity"`
}

type ListSecurityAllowlistRequest struct {
	Page int `query:"page" default:"1"`
	Size int `query:"size" default:"20"`
}

type CreateSecurityAllowlistRequest struct {
	Body struct {
		IPCidr      string `json:"ip_cidr" required:"true"`
		Description string `json:"description"`
	} `json:"body"`
}

type SecurityAllowlistRequest struct {
	ID int `param:"id"`
}

type UpdateSecurityRulesRequest struct {
	Body struct {
		Settings []SettingInput `json:"settings" required:"true"`
	} `json:"body"`
}

// ---------- Overview ----------

// SecurityOverview is the aggregate payload behind the security landing page.
type SecurityOverview struct {
	ActiveBans          int64                             `json:"active_bans"`
	AutomaticBans       int64                             `json:"automatic_bans"`
	ManualBans          int64                             `json:"manual_bans"`
	BansLast24h         int64                             `json:"bans_last_24h"`
	EventsLast24h       int64                             `json:"events_last_24h"`
	PortScansDetected   int64                             `json:"port_scans_detected"`
	AuthAttacksDetected int64                             `json:"auth_attacks_detected"`
	Trend               []repositories.SecurityDailyCount `json:"trend"`
}

// Overview returns aggregate ban and detection counts plus the 14-day trend.
// Count queries are best-effort, matching the admin metrics handler.
func (h *SecurityHandler) Overview(c *okapi.Context) error {
	o := SecurityOverview{}

	o.ActiveBans, _ = h.banRepo.CountActive()
	o.AutomaticBans, _ = h.banRepo.CountByType(security.BanTypeAutomatic)
	o.ManualBans, _ = h.banRepo.CountByType(security.BanTypeManual)
	since := time.Now().Add(-24 * time.Hour)
	o.BansLast24h, _ = h.banRepo.CountSince(since)
	o.EventsLast24h, _ = h.eventRepo.CountSince(since)

	// Reason-code breakdowns have no dedicated repository method and the
	// repositories are owned elsewhere: count inline instead.
	h.db.Model(&models.SecurityBan{}).
		Where("reason_code = ?", string(security.ReasonPortScan)).
		Count(&o.PortScansDetected)
	h.db.Model(&models.SecurityBan{}).
		Where("reason_code = ?", string(security.ReasonAuthBruteForce)).
		Count(&o.AuthAttacksDetected)

	if trend, err := h.eventRepo.TrendCounts(14); err == nil {
		o.Trend = trend
	} else {
		o.Trend = []repositories.SecurityDailyCount{}
	}

	return ok(c, o)
}

// ---------- Bans ----------

// ListBans returns a page of bans, newest first, using the project-wide
// 0-based pagination envelope.
func (h *SecurityHandler) ListBans(c *okapi.Context, req *ListSecurityBansRequest) error {
	page, repoPage, size := securityPage(req.Page, req.Size)

	bans, total, err := h.banRepo.List(repoPage, size, req.Search, req.Filter)
	if err != nil {
		return c.AbortInternalServerError("failed to list bans")
	}

	return paginated(c, bans, total, page, size)
}

// CreateBan places a manual ban on an IP address or CIDR block: permanent, or
// for a number of hours. The address is validated here so the engine never
// sees an unparseable value.
func (h *SecurityHandler) CreateBan(c *okapi.Context, req *CreateSecurityBanRequest) error {
	if h.eng == nil {
		return securityEngineDisabled(c)
	}

	value := strings.TrimSpace(req.Body.IPAddress)
	var input security.BanInput
	if addr, err := netip.ParseAddr(value); err == nil {
		input.IP = addr
	} else if prefix, err := netip.ParsePrefix(value); err == nil {
		input.CIDR = &prefix
	} else {
		return c.AbortBadRequest("ip_address must be a valid IP address or CIDR")
	}

	if !req.Body.Permanent && req.Body.DurationHours <= 0 {
		return c.AbortBadRequest("a manual ban requires permanent=true or a positive duration_hours")
	}

	reason := security.ReasonManual
	if req.Body.ReasonCode != "" {
		parsed, known := securityReasonByName(req.Body.ReasonCode)
		if !known {
			return c.AbortBadRequest("unknown reason_code")
		}
		reason = parsed
	}

	input.Reason = reason
	input.Description = req.Body.Description
	input.BanType = security.BanTypeManual
	input.CreatedBy = adminActor(c)

	ban, err := h.eng.Bans.ManualBan(c.Request().Context(), input, req.Body.Permanent, req.Body.DurationHours)
	if err != nil {
		return c.AbortInternalServerError("failed to place ban", err)
	}

	// A nil ban means the address is allowlisted and the placement was
	// suppressed deliberately (the allowlist always wins).
	if ban == nil {
		return ok(c, okapi.M{
			"message":    "Address is allowlisted; no ban was placed",
			"suppressed": true,
		})
	}

	h.auditSecurity(c, "security.ban_created",
		fmt.Sprintf("Manual ban placed for %s", ban.IPAddress),
		map[string]any{"ban_id": ban.ID, "ip_address": ban.IPAddress, "reason_code": ban.ReasonCode, "permanent": req.Body.Permanent})

	return created(c, ban)
}

// Unban lifts a ban: the row is expired in place and kept as offense history.
func (h *SecurityHandler) Unban(c *okapi.Context, req *SecurityBanRequest) error {
	if h.eng == nil {
		return securityEngineDisabled(c)
	}

	ban, err := h.banRepo.FindByID(uint(req.ID))
	if err != nil {
		return c.AbortNotFound("ban not found")
	}

	if err := h.eng.Bans.Unban(c.Request().Context(), ban.ID, adminActor(c)); err != nil {
		return c.AbortInternalServerError("failed to lift ban", err)
	}

	h.auditSecurity(c, "security.ban_lifted",
		fmt.Sprintf("Ban lifted for %s", ban.IPAddress),
		map[string]any{"ban_id": ban.ID, "ip_address": ban.IPAddress})

	return ok(c, dto.MessageData{Message: "Ban lifted"})
}

// ExtendBan pushes a timed ban's expiry out by the given number of hours.
// Extending a permanent ban is a no-op.
func (h *SecurityHandler) ExtendBan(c *okapi.Context, req *ExtendSecurityBanRequest) error {
	if h.eng == nil {
		return securityEngineDisabled(c)
	}

	if req.Body.Hours <= 0 {
		return c.AbortBadRequest("hours must be a positive number")
	}

	ban, err := h.banRepo.FindByID(uint(req.ID))
	if err != nil {
		return c.AbortNotFound("ban not found")
	}

	if err := h.eng.Bans.ExtendBan(c.Request().Context(), ban.ID, req.Body.Hours, adminActor(c)); err != nil {
		return c.AbortInternalServerError("failed to extend ban", err)
	}

	h.auditSecurity(c, "security.ban_extended",
		fmt.Sprintf("Ban for %s extended by %d hours", ban.IPAddress, req.Body.Hours),
		map[string]any{"ban_id": ban.ID, "ip_address": ban.IPAddress, "hours": req.Body.Hours})

	return ok(c, dto.MessageData{Message: fmt.Sprintf("Ban extended by %d hours", req.Body.Hours)})
}

// ---------- Detection events ----------

// ListEvents returns a page of security events, newest first, filterable by
// event type and severity.
func (h *SecurityHandler) ListEvents(c *okapi.Context, req *ListSecurityEventsRequest) error {
	page, repoPage, size := securityPage(req.Page, req.Size)

	events, total, err := h.eventRepo.List(repoPage, size, req.EventType, req.Severity)
	if err != nil {
		return c.AbortInternalServerError("failed to list security events")
	}

	return paginated(c, events, total, page, size)
}

// securityStreamTick is how often the live events stream polls for new rows.
const securityStreamTick = 2 * time.Second

// securityStreamPingInterval keeps intermediaries from closing an idle stream,
// mirroring the push stream's ping cadence.
const securityStreamPingInterval = 30 * time.Second

// EventsStream sends new security events over SSE, polling the database on a
// 2-second ticker (the same ticker style as the admin metrics stream). Each
// row goes out as a security.event; ban lifecycle rows are additionally named
// ban.created / ban.lifted so the dashboard can react to them specifically —
// those names come from the row's event_type, which the recorder stamps
// alongside action_taken (action_taken=banned only ever marks ban.created
// rows, so event_type is the discriminating field). The stream opens at the
// newest existing event so clients receive only what happens after they
// connect; history is served by the plain events list.
func (h *SecurityHandler) EventsStream(c *okapi.Context) error {
	ctx := c.Request().Context()

	w := c.ResponseWriter()

	var lastID uint
	h.db.Model(&models.SecurityEvent{}).
		Select("COALESCE(MAX(id), 0)").
		Scan(&lastID)

	send := func() error {
		events, err := h.eventRepo.FindSinceID(lastID, 100)
		if err != nil {
			return nil // transient database hiccup: keep the stream alive
		}
		for i := range events {
			evt := events[i]
			lastID = evt.ID

			msg := okapi.Message{
				Event:      "security.event",
				Data:       evt,
				Serializer: &okapi.JSONSerializer{},
			}
			if _, err := msg.Send(w); err != nil {
				return err
			}

			switch evt.EventType {
			case security.EventBanCreated:
				banMsg := okapi.Message{
					Event:      "ban.created",
					Data:       evt,
					Serializer: &okapi.JSONSerializer{},
				}
				if _, err := banMsg.Send(w); err != nil {
					return err
				}
			case security.EventBanLifted:
				liftedMsg := okapi.Message{
					Event:      "ban.lifted",
					Data:       evt,
					Serializer: &okapi.JSONSerializer{},
				}
				if _, err := liftedMsg.Send(w); err != nil {
					return err
				}
			}
		}
		return nil
	}

	// Nothing queued yet — the stream opens at the newest event.
	if err := send(); err != nil {
		return nil
	}

	ticker := time.NewTicker(securityStreamTick)
	defer ticker.Stop()
	ping := time.NewTicker(securityStreamPingInterval)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := send(); err != nil {
				return nil // the client went away
			}
		case <-ping.C:
			pingMsg := okapi.Message{
				Event:      "ping",
				Data:       okapi.M{"time": time.Now().UTC()},
				Serializer: &okapi.JSONSerializer{},
			}
			if _, err := pingMsg.Send(w); err != nil {
				return nil
			}
		}
	}
}

// ---------- Rules ----------

// securityRuleGroup names the rule groups rendered by the dashboard.
const (
	securityRuleGroupGeneral    = "general"
	securityRuleGroupSignals    = "signals"
	securityRuleGroupWeights    = "weights"
	securityRuleGroupEscalation = "escalation"
	securityRuleGroupRetention  = "retention"
)

// SecurityRule is one tunable security setting as presented to the dashboard.
type SecurityRule struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Value       any    `json:"value"`
	Type        string `json:"type"` // "int" | "bool"
	Min         *int   `json:"min,omitempty"`
	Max         *int   `json:"max,omitempty"`
	Group       string `json:"group"`
}

// securityRuleDef is the server-side definition of one tunable: its whitelist
// membership, presentation metadata, default and bounds.
type securityRuleDef struct {
	Key         string
	Label       string
	Description string
	Type        string // models.SettingTypeInt | models.SettingTypeBool
	Group       string
	Default     int // int value; for bool, 1 = true and 0 = false
	Min         int
	Max         int // 0 = unbounded
}

// securityRuleDefs is the whitelist of security_* settings the rules endpoints
// expose. It covers exactly the tunables the settings seeder plants (the
// enabled/fail-open master switches are POSTA_SECURITY_* environment variables
// and are deliberately not editable here).
var securityRuleDefs = []securityRuleDef{
	// General scoring
	{Key: "security_risk_threshold", Label: "Risk threshold", Description: "Combined signal weight required to ban an IP address", Type: models.SettingTypeInt, Group: securityRuleGroupGeneral, Default: 40, Min: 1},
	{Key: "security_instant_ban_ratio", Label: "Instant ban ratio", Description: "A single signal reaching the threshold times this ratio bans immediately", Type: models.SettingTypeInt, Group: securityRuleGroupGeneral, Default: 2, Min: 1},

	// Per-signal thresholds and sliding windows
	{Key: "security_sig_distinct_ports_threshold", Label: "Distinct ports threshold", Description: "Distinct service ports contacted before the port scan signal fires", Type: models.SettingTypeInt, Group: securityRuleGroupSignals, Default: 5, Min: 1},
	{Key: "security_sig_distinct_ports_window_seconds", Label: "Distinct ports window", Description: "Sliding window in seconds for the distinct ports counter", Type: models.SettingTypeInt, Group: securityRuleGroupSignals, Default: 60, Min: 1},
	{Key: "security_sig_new_connections_threshold", Label: "New connections threshold", Description: "New connections before the connection burst signal fires", Type: models.SettingTypeInt, Group: securityRuleGroupSignals, Default: 30, Min: 1},
	{Key: "security_sig_new_connections_window_seconds", Label: "New connections window", Description: "Sliding window in seconds for the new connections counter", Type: models.SettingTypeInt, Group: securityRuleGroupSignals, Default: 60, Min: 1},
	{Key: "security_sig_incomplete_handshakes_threshold", Label: "Incomplete handshakes threshold", Description: "Incomplete handshakes before the handshake abuse signal fires", Type: models.SettingTypeInt, Group: securityRuleGroupSignals, Default: 10, Min: 1},
	{Key: "security_sig_incomplete_handshakes_window_seconds", Label: "Incomplete handshakes window", Description: "Sliding window in seconds for the incomplete handshakes counter", Type: models.SettingTypeInt, Group: securityRuleGroupSignals, Default: 60, Min: 1},
	{Key: "security_sig_connect_aborts_threshold", Label: "Connect aborts threshold", Description: "Connections dropped before any command before the connection abuse signal fires", Type: models.SettingTypeInt, Group: securityRuleGroupSignals, Default: 12, Min: 1},
	{Key: "security_sig_connect_aborts_window_seconds", Label: "Connect aborts window", Description: "Sliding window in seconds for the connect aborts counter", Type: models.SettingTypeInt, Group: securityRuleGroupSignals, Default: 60, Min: 1},
	{Key: "security_sig_malformed_preauth_threshold", Label: "Malformed pre-auth threshold", Description: "Malformed pre-auth messages before the protocol abuse signal fires", Type: models.SettingTypeInt, Group: securityRuleGroupSignals, Default: 15, Min: 1},
	{Key: "security_sig_malformed_preauth_window_seconds", Label: "Malformed pre-auth window", Description: "Sliding window in seconds for the malformed pre-auth counter", Type: models.SettingTypeInt, Group: securityRuleGroupSignals, Default: 300, Min: 1},
	{Key: "security_sig_suspicious_paths_threshold", Label: "Suspicious paths threshold", Description: "Suspicious URL paths probed before the reconnaissance signal fires", Type: models.SettingTypeInt, Group: securityRuleGroupSignals, Default: 5, Min: 1},
	{Key: "security_sig_suspicious_paths_window_seconds", Label: "Suspicious paths window", Description: "Sliding window in seconds for the suspicious paths counter", Type: models.SettingTypeInt, Group: securityRuleGroupSignals, Default: 60, Min: 1},
	{Key: "security_sig_auth_failures_threshold", Label: "Auth failures threshold", Description: "Failed authentication attempts before the brute force signal fires", Type: models.SettingTypeInt, Group: securityRuleGroupSignals, Default: 10, Min: 1},
	{Key: "security_sig_auth_failures_window_seconds", Label: "Auth failures window", Description: "Sliding window in seconds for the auth failures counter", Type: models.SettingTypeInt, Group: securityRuleGroupSignals, Default: 300, Min: 1},

	// Per-signal weights toward the risk score
	{Key: "security_weight_distinct_ports", Label: "Weight: distinct ports", Description: "Risk score contributed when the distinct ports signal is exceeded", Type: models.SettingTypeInt, Group: securityRuleGroupWeights, Default: 35, Min: 0},
	{Key: "security_weight_new_connections", Label: "Weight: new connections", Description: "Risk score contributed when the new connections signal is exceeded", Type: models.SettingTypeInt, Group: securityRuleGroupWeights, Default: 20, Min: 0},
	{Key: "security_weight_incomplete_handshakes", Label: "Weight: incomplete handshakes", Description: "Risk score contributed when the incomplete handshakes signal is exceeded", Type: models.SettingTypeInt, Group: securityRuleGroupWeights, Default: 20, Min: 0},
	{Key: "security_weight_connect_aborts", Label: "Weight: connect aborts", Description: "Risk score contributed when the connect aborts signal is exceeded", Type: models.SettingTypeInt, Group: securityRuleGroupWeights, Default: 15, Min: 0},
	{Key: "security_weight_malformed_preauth", Label: "Weight: malformed pre-auth", Description: "Risk score contributed when the malformed pre-auth signal is exceeded", Type: models.SettingTypeInt, Group: securityRuleGroupWeights, Default: 15, Min: 0},
	{Key: "security_weight_suspicious_paths", Label: "Weight: suspicious paths", Description: "Risk score contributed when the suspicious paths signal is exceeded", Type: models.SettingTypeInt, Group: securityRuleGroupWeights, Default: 25, Min: 0},
	{Key: "security_weight_auth_failures", Label: "Weight: auth failures", Description: "Risk score contributed when the auth failures signal is exceeded", Type: models.SettingTypeInt, Group: securityRuleGroupWeights, Default: 20, Min: 0},

	// Automatic ban escalation by offense count
	{Key: "security_ban_tier_1_hours", Label: "Ban tier 1", Description: "Automatic ban duration in hours for a first offense", Type: models.SettingTypeInt, Group: securityRuleGroupEscalation, Default: 1, Min: 1},
	{Key: "security_ban_tier_2_hours", Label: "Ban tier 2", Description: "Automatic ban duration in hours for a second offense", Type: models.SettingTypeInt, Group: securityRuleGroupEscalation, Default: 24, Min: 1},
	{Key: "security_ban_tier_3_hours", Label: "Ban tier 3", Description: "Automatic ban duration in hours for a third offense", Type: models.SettingTypeInt, Group: securityRuleGroupEscalation, Default: 168, Min: 1},
	{Key: "security_ban_tier_4_hours", Label: "Ban tier 4", Description: "Automatic ban duration in hours from the fourth offense onward", Type: models.SettingTypeInt, Group: securityRuleGroupEscalation, Default: 720, Min: 1},

	// Retention
	{Key: "security_events_retention_days", Label: "Event retention", Description: "Days to keep detection events before cleanup", Type: models.SettingTypeInt, Group: securityRuleGroupRetention, Default: 30, Min: 1},
}

// toRule renders one definition against the stored settings, falling back to
// the documented default for missing or unparseable values.
func (d securityRuleDef) toRule(values map[string]models.Setting) SecurityRule {
	rule := SecurityRule{
		Key:         d.Key,
		Label:       d.Label,
		Description: d.Description,
		Type:        d.Type,
		Group:       d.Group,
	}
	if d.Min > 0 {
		min := d.Min
		rule.Min = &min
	}
	if d.Max > 0 {
		max := d.Max
		rule.Max = &max
	}

	rule.Value = d.defaultValue()
	setting, found := values[d.Key]
	if !found {
		return rule
	}

	switch d.Type {
	case models.SettingTypeBool:
		if parsed, err := strconv.ParseBool(strings.TrimSpace(setting.Value)); err == nil {
			rule.Value = parsed
		}
	default: // int
		if parsed, err := strconv.Atoi(strings.TrimSpace(setting.Value)); err == nil {
			rule.Value = parsed
		}
	}
	return rule
}

// defaultValue returns the definition's default as the JSON value its type
// calls for: a bool for bool settings, an int for everything else.
func (d securityRuleDef) defaultValue() any {
	if d.Type == models.SettingTypeBool {
		return d.Default != 0
	}
	return d.Default
}

// parseValue validates and canonicalizes a submitted rule value; the return
// value is the string stored in the settings table.
func (d securityRuleDef) parseValue(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	switch d.Type {
	case models.SettingTypeBool:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return "", fmt.Errorf("expected true or false")
		}
		if parsed {
			return models.SettingTrue, nil
		}
		return models.SettingFalse, nil
	default: // int
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return "", fmt.Errorf("expected a whole number")
		}
		if d.Min > 0 && parsed < d.Min {
			return "", fmt.Errorf("must be at least %d", d.Min)
		}
		if d.Max > 0 && parsed > d.Max {
			return "", fmt.Errorf("must be at most %d", d.Max)
		}
		return strconv.Itoa(parsed), nil
	}
}

// GetRules returns every tunable security setting with its label, current
// value, type and bounds.
func (h *SecurityHandler) GetRules(c *okapi.Context) error {
	if h.settingRepo == nil {
		return c.AbortServiceUnavailable("settings repository unavailable")
	}
	settings, err := h.settingRepo.FindAll()
	if err != nil {
		return c.AbortInternalServerError("failed to load settings")
	}
	return ok(c, h.buildRules(settings))
}

// UpdateRules upserts a batch of security_* settings. Keys outside the
// whitelist are ignored (matching the platform settings handler's treatment of
// reserved keys); submitted values are validated against the rule type and
// bounds. The updated rule list is returned.
func (h *SecurityHandler) UpdateRules(c *okapi.Context, req *UpdateSecurityRulesRequest) error {
	if h.settingRepo == nil {
		return c.AbortServiceUnavailable("settings repository unavailable")
	}

	whitelist := make(map[string]securityRuleDef, len(securityRuleDefs))
	for _, def := range securityRuleDefs {
		whitelist[def.Key] = def
	}

	updates := make([]models.Setting, 0, len(req.Body.Settings))
	for _, in := range req.Body.Settings {
		def, allowed := whitelist[strings.TrimSpace(in.Key)]
		if !allowed {
			continue
		}
		value, err := def.parseValue(in.Value)
		if err != nil {
			return c.AbortBadRequest(fmt.Sprintf("%s: %v", def.Key, err))
		}
		updates = append(updates, models.Setting{Key: def.Key, Value: value, Type: def.Type})
	}

	if len(updates) > 0 {
		if err := h.settingRepo.BulkUpsert(updates); err != nil {
			return c.AbortInternalServerError("failed to update security rules")
		}
		h.auditSecurity(c, "security.rules_updated",
			fmt.Sprintf("%d security rule(s) updated", len(updates)),
			map[string]any{"keys": settingKeys(updates)})
	}

	settings, err := h.settingRepo.FindAll()
	if err != nil {
		return c.AbortInternalServerError("failed to load settings")
	}
	return ok(c, h.buildRules(settings))
}

// buildRules renders the full whitelist against the stored settings.
func (h *SecurityHandler) buildRules(settings []models.Setting) []SecurityRule {
	values := make(map[string]models.Setting, len(settings))
	for _, s := range settings {
		values[s.Key] = s
	}
	rules := make([]SecurityRule, 0, len(securityRuleDefs))
	for i := range securityRuleDefs {
		rules = append(rules, securityRuleDefs[i].toRule(values))
	}
	return rules
}

// ---------- Allowlist ----------

// ListAllowlist returns a page of allowlist entries, newest first.
func (h *SecurityHandler) ListAllowlist(c *okapi.Context, req *ListSecurityAllowlistRequest) error {
	page, repoPage, size := securityPage(req.Page, req.Size)

	entries, total, err := h.allowRepo.List(repoPage, size)
	if err != nil {
		return c.AbortInternalServerError("failed to list allowlist")
	}

	return paginated(c, entries, total, page, size)
}

// CreateAllowlist adds an IP address or CIDR block to the allowlist. With the
// engine wired, BanService.Allow persists the entry and refreshes plus
// propagates the ban list; without it the row is written database-only and
// reaches the ban list on the next resync.
func (h *SecurityHandler) CreateAllowlist(c *okapi.Context, req *CreateSecurityAllowlistRequest) error {
	value, err := normalizeAllowValue(strings.TrimSpace(req.Body.IPCidr))
	if err != nil {
		return c.AbortBadRequest("ip_cidr must be a valid IP address or CIDR")
	}

	if existing, err := h.allowRepo.FindByCIDR(value); err == nil && existing != nil {
		return c.AbortConflict("address is already allowlisted")
	}

	entry := models.SecurityAllowlist{
		IPCidr:      value,
		Description: req.Body.Description,
		CreatedBy:   adminActor(c),
	}

	if h.eng != nil {
		if err := h.eng.Bans.Allow(c.Request().Context(), entry); err != nil {
			return c.AbortInternalServerError("failed to save allowlist entry", err)
		}
		// Allow persists by value; re-read for the assigned ID.
		stored, err := h.allowRepo.FindByCIDR(value)
		if err == nil && stored != nil {
			entry = *stored
		}
	} else if err := h.allowRepo.Create(&entry); err != nil {
		return c.AbortInternalServerError("failed to save allowlist entry", err)
	}

	h.auditSecurity(c, "security.allowlist_added",
		fmt.Sprintf("Allowlist entry added for %s", entry.IPCidr),
		map[string]any{"allowlist_id": entry.ID, "ip_cidr": entry.IPCidr})

	return created(c, entry)
}

// DeleteAllowlist removes an allowlist entry; the address becomes subject to
// detection and existing bans again.
func (h *SecurityHandler) DeleteAllowlist(c *okapi.Context, req *SecurityAllowlistRequest) error {
	entry, err := h.allowRepo.FindByID(uint(req.ID))
	if err != nil {
		return c.AbortNotFound("allowlist entry not found")
	}

	if h.eng != nil {
		if err := h.eng.Bans.Disallow(c.Request().Context(), entry.ID); err != nil {
			return c.AbortInternalServerError("failed to remove allowlist entry", err)
		}
	} else if err := h.allowRepo.Delete(entry.ID); err != nil {
		return c.AbortInternalServerError("failed to remove allowlist entry", err)
	}

	h.auditSecurity(c, "security.allowlist_removed",
		fmt.Sprintf("Allowlist entry removed for %s", entry.IPCidr),
		map[string]any{"allowlist_id": entry.ID, "ip_cidr": entry.IPCidr})

	return ok(c, dto.MessageData{Message: "Allowlist entry removed"})
}

// normalizeAllowValue canonicalizes an allowlist value: single addresses are
// stored unmapped, CIDR blocks masked, so lookups and the ban list's parser
// always see one spelling per entry.
func normalizeAllowValue(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("allowlist value is empty")
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		return addr.Unmap().String(), nil
	}
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Masked().String(), nil
	}
	return "", fmt.Errorf("%q is not a valid IP address or CIDR", value)
}

// ---------- Shared helpers ----------

// securityPage normalizes 0-based pagination — the project-wide API
// convention shared with normalizePageParams — into the 1-based form the
// security repositories expect. It returns the 0-based page for the
// pagination envelope, the 1-based page for the repository call, and the
// clamped size.
func securityPage(page, size int) (int, int, int) {
	if page < 0 {
		page = 0
	}
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, page + 1, size
}

// adminActor returns the acting admin's identity for ban bookkeeping
// (SecurityBan.CreatedBy) and audit events.
func adminActor(c *okapi.Context) string {
	if email := c.GetString("email"); email != "" {
		return email
	}
	return "admin"
}

// securityReasons lists the reason codes a manual ban may carry, keyed by
// their stored form.
var securityReasons = map[string]security.Reason{
	string(security.ReasonPortScan):       security.ReasonPortScan,
	string(security.ReasonRapidConn):      security.ReasonRapidConn,
	string(security.ReasonConnAbuse):      security.ReasonConnAbuse,
	string(security.ReasonIncompleteHS):   security.ReasonIncompleteHS,
	string(security.ReasonTLSProbing):     security.ReasonTLSProbing,
	string(security.ReasonSMTPAbuse):      security.ReasonSMTPAbuse,
	string(security.ReasonHTTPRecon):      security.ReasonHTTPRecon,
	string(security.ReasonMalformedProto): security.ReasonMalformedProto,
	string(security.ReasonAuthBruteForce): security.ReasonAuthBruteForce,
	string(security.ReasonManual):         security.ReasonManual,
}

// securityReasonByName looks up a submitted reason code.
func securityReasonByName(name string) (security.Reason, bool) {
	reason, ok := securityReasons[name]
	return reason, ok
}

// securityEngineDisabled answers 503 when the security engine is not wired
// into the server, so ban mutations cannot be applied.
func securityEngineDisabled(c *okapi.Context) error {
	return c.AbortServiceUnavailable("security engine disabled")
}

// auditSecurity publishes an admin audit event for a security change when the
// event bus is available.
func (h *SecurityHandler) auditSecurity(c *okapi.Context, eventType, message string, meta map[string]any) {
	if h.bus == nil {
		return
	}
	adminID := uint(c.GetInt("user_id"))
	h.bus.PublishSimple(models.EventCategoryAudit, eventType, &adminID, c.GetString("email"), c.RealIP(), message, meta)
}

// settingKeys extracts the keys from a settings batch, for audit metadata.
func settingKeys(settings []models.Setting) []string {
	keys := make([]string, 0, len(settings))
	for _, s := range settings {
		keys = append(keys, s.Key)
	}
	return keys
}
