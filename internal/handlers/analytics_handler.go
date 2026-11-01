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
	"time"

	"github.com/goposta/posta/internal/services/cache"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

type AnalyticsHandler struct {
	repo  *repositories.AnalyticsRepository
	cache *cache.Cache
}

func NewAnalyticsHandler(repo *repositories.AnalyticsRepository, c *cache.Cache) *AnalyticsHandler {
	return &AnalyticsHandler{repo: repo, cache: c}
}

type AnalyticsRequest struct {
	From   string `query:"from"`
	To     string `query:"to"`
	Status string `query:"status"`
}

type AnalyticsResponse struct {
	DailyCounts     []repositories.DailyCount      `json:"daily_counts"`
	StatusBreakdown []repositories.StatusBreakdown `json:"status_breakdown"`
}

type DashboardAnalyticsRequest struct {
	From string `query:"from"`
	To   string `query:"to"`
}

type DashboardAnalyticsResponse struct {
	DeliveryRateTrends []repositories.DeliveryRatePoint `json:"delivery_rate_trends"`
	BounceRateTrends   []repositories.BounceRatePoint   `json:"bounce_rate_trends"`
	LatencyPercentiles *repositories.LatencyPercentiles `json:"latency_percentiles"`
}

func (h *AnalyticsHandler) UserAnalytics(c *okapi.Context, req *AnalyticsRequest) error {
	scope := getScope(c)
	ctx := c.Request().Context()

	// Try cache first
	scopeKey := int(scope.UserID)
	if scope.WorkspaceID != nil {
		scopeKey = int(*scope.WorkspaceID) + 1000000
	}
	cacheKey := cache.UserAnalyticsKey(scopeKey, req.From, req.To, req.Status)
	var resp AnalyticsResponse
	if h.cache.Get(ctx, cacheKey, &resp) {
		return ok(c, resp)
	}

	if scope.WorkspaceID == nil {
		return c.AbortBadRequest("no active workspace")
	}
	from, to, err := parseTimeRange(req.From, req.To)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}

	daily, err := h.repo.WorkspaceDailyCounts(*scope.WorkspaceID, from, to, req.Status)
	if err != nil {
		return c.AbortInternalServerError("failed to fetch analytics")
	}
	breakdown, err := h.repo.WorkspaceStatusBreakdown(*scope.WorkspaceID, from, to)
	if err != nil {
		return c.AbortInternalServerError("failed to fetch analytics")
	}

	resp = AnalyticsResponse{
		DailyCounts:     daily,
		StatusBreakdown: breakdown,
	}

	h.cache.Set(ctx, cacheKey, resp, cache.AnalyticsTTL)

	return ok(c, resp)
}

func (h *AnalyticsHandler) AdminAnalytics(c *okapi.Context, req *AnalyticsRequest) error {
	ctx := c.Request().Context()

	// Try cache first
	cacheKey := cache.AdminAnalyticsKey(req.From, req.To, req.Status)
	var resp AnalyticsResponse
	if h.cache.Get(ctx, cacheKey, &resp) {
		return ok(c, resp)
	}

	from, to, err := parseTimeRange(req.From, req.To)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}

	daily, err := h.repo.AdminDailyCounts(from, to, req.Status)
	if err != nil {
		return c.AbortInternalServerError("failed to fetch analytics")
	}
	breakdown, err := h.repo.AdminStatusBreakdown(from, to)
	if err != nil {
		return c.AbortInternalServerError("failed to fetch analytics")
	}

	resp = AnalyticsResponse{
		DailyCounts:     daily,
		StatusBreakdown: breakdown,
	}

	h.cache.Set(ctx, cacheKey, resp, cache.AnalyticsTTL)

	return ok(c, resp)
}

func (h *AnalyticsHandler) UserDashboardAnalytics(c *okapi.Context, req *DashboardAnalyticsRequest) error {
	scope := getScope(c)
	ctx := c.Request().Context()

	scopeKey := int(scope.UserID)
	if scope.WorkspaceID != nil {
		scopeKey = int(*scope.WorkspaceID) + 1000000
	}
	cacheKey := cache.DashboardAnalyticsKey(scopeKey, req.From, req.To)
	var resp DashboardAnalyticsResponse
	if h.cache.Get(ctx, cacheKey, &resp) {
		return ok(c, resp)
	}

	if scope.WorkspaceID == nil {
		return c.AbortBadRequest("no active workspace")
	}
	from, to, err := parseTimeRange(req.From, req.To)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}

	delivery, err := h.repo.WorkspaceDeliveryRateTrends(*scope.WorkspaceID, from, to)
	if err != nil {
		return c.AbortInternalServerError("failed to fetch delivery rate trends")
	}
	bouncePoints, err := h.repo.WorkspaceBounceRateTrends(*scope.WorkspaceID, from, to)
	if err != nil {
		return c.AbortInternalServerError("failed to fetch bounce rate trends")
	}
	latency, err := h.repo.WorkspaceLatencyPercentiles(*scope.WorkspaceID, from, to)
	if err != nil {
		return c.AbortInternalServerError("failed to fetch latency percentiles")
	}

	resp = DashboardAnalyticsResponse{
		DeliveryRateTrends: delivery,
		BounceRateTrends:   bouncePoints,
		LatencyPercentiles: latency,
	}

	h.cache.Set(ctx, cacheKey, resp, cache.AnalyticsTTL)
	return ok(c, resp)
}

type ProviderBreakdownRequest struct {
	From string `query:"from"`
	To   string `query:"to"`
}

type ProviderBreakdownResponse struct {
	Providers []repositories.ProviderBreakdownPoint `json:"providers"`
}

// UserProviderBreakdown returns delivery counts grouped by recipient mailbox
// provider (Gmail, Outlook, Yahoo, ...) for the authenticated scope.
func (h *AnalyticsHandler) UserProviderBreakdown(c *okapi.Context, req *ProviderBreakdownRequest) error {
	scope := getScope(c)
	ctx := c.Request().Context()

	scopeKey := int(scope.UserID)
	if scope.WorkspaceID != nil {
		scopeKey = int(*scope.WorkspaceID) + 1000000
	}
	cacheKey := cache.UserProviderBreakdownKey(scopeKey, req.From, req.To)
	var resp ProviderBreakdownResponse
	if h.cache.Get(ctx, cacheKey, &resp) {
		return ok(c, resp)
	}

	if scope.WorkspaceID == nil {
		return c.AbortBadRequest("no active workspace")
	}
	from, to, err := parseTimeRange(req.From, req.To)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}

	rows, err := h.repo.WorkspaceProviderBreakdown(*scope.WorkspaceID, from, to)
	if err != nil {
		return c.AbortInternalServerError("failed to fetch provider breakdown")
	}

	resp = ProviderBreakdownResponse{Providers: rows}
	h.cache.Set(ctx, cacheKey, resp, cache.AnalyticsTTL)
	return ok(c, resp)
}

// AdminProviderBreakdown returns provider breakdown across all users.
func (h *AnalyticsHandler) AdminProviderBreakdown(c *okapi.Context, req *ProviderBreakdownRequest) error {
	ctx := c.Request().Context()

	cacheKey := cache.AdminProviderBreakdownKey(req.From, req.To)
	var resp ProviderBreakdownResponse
	if h.cache.Get(ctx, cacheKey, &resp) {
		return ok(c, resp)
	}

	from, to, err := parseTimeRange(req.From, req.To)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	rows, err := h.repo.AdminProviderBreakdown(from, to)
	if err != nil {
		return c.AbortInternalServerError("failed to fetch provider breakdown")
	}

	resp = ProviderBreakdownResponse{Providers: rows}
	h.cache.Set(ctx, cacheKey, resp, cache.AnalyticsTTL)
	return ok(c, resp)
}

func (h *AnalyticsHandler) AdminDashboardAnalytics(c *okapi.Context, req *DashboardAnalyticsRequest) error {
	ctx := c.Request().Context()

	cacheKey := cache.AdminDashboardAnalyticsKey(req.From, req.To)
	var resp DashboardAnalyticsResponse
	if h.cache.Get(ctx, cacheKey, &resp) {
		return ok(c, resp)
	}

	from, to, err := parseTimeRange(req.From, req.To)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}

	delivery, err := h.repo.AdminDeliveryRateTrends(from, to)
	if err != nil {
		return c.AbortInternalServerError("failed to fetch delivery rate trends")
	}
	bounces, err := h.repo.AdminBounceRateTrends(from, to)
	if err != nil {
		return c.AbortInternalServerError("failed to fetch bounce rate trends")
	}
	latency, err := h.repo.AdminLatencyPercentiles(from, to)
	if err != nil {
		return c.AbortInternalServerError("failed to fetch latency percentiles")
	}

	resp = DashboardAnalyticsResponse{
		DeliveryRateTrends: delivery,
		BounceRateTrends:   bounces,
		LatencyPercentiles: latency,
	}

	h.cache.Set(ctx, cacheKey, resp, cache.AnalyticsTTL)
	return ok(c, resp)
}

// maxAnalyticsRangeDays caps how far a single report may reach. The series is
// materialised one entry per day in memory and in the response, so without a cap
// a caller asking for 1970..2099 would have the server build a 47,000-point
// array per chart.
const maxAnalyticsRangeDays = 400

// parseTimeRange resolves the requested window to a UTC half-open-ish range:
// midnight on the from date through the last instant of the to date.
//
// Everything is UTC on purpose. The queries bucket rows with
// `AT TIME ZONE 'UTC'`, so the boundaries have to agree or the first and last
// day of a report would be cut at the wrong hour.
func parseTimeRange(fromStr, toStr string) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	to := endOfUTCDay(now)
	from := startOfUTCDay(now.AddDate(0, 0, -defaultAnalyticsRangeDays+1))

	if fromStr != "" {
		t, err := time.ParseInLocation("2006-01-02", fromStr, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("from must be a date like 2026-01-31")
		}
		from = startOfUTCDay(t)
	}
	if toStr != "" {
		t, err := time.ParseInLocation("2006-01-02", toStr, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("to must be a date like 2026-01-31")
		}
		to = endOfUTCDay(t)
	}

	if to.Before(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("from must not be later than to")
	}
	// Counted on the calendar, not by dividing the duration: `to` is the last
	// instant of its day, and Duration.Hours() is a float64 that rounds
	// 9599h59m59.999999999s up to exactly 9600, which put the count a day over
	// and rejected ranges that sat exactly on the cap.
	if days := int(startOfUTCDay(to).Sub(from)/(24*time.Hour)) + 1; days > maxAnalyticsRangeDays {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"the range covers %d days; %d is the maximum", days, maxAnalyticsRangeDays)
	}

	return from, to, nil
}

const defaultAnalyticsRangeDays = 30

func startOfUTCDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// endOfUTCDay is the last representable instant of the day, so a range stated
// inclusively really does include everything sent that day. The previous
// implementation stopped a second early and quietly dropped anything accepted in
// the final second.
func endOfUTCDay(t time.Time) time.Time {
	return startOfUTCDay(t).AddDate(0, 0, 1).Add(-time.Nanosecond)
}
