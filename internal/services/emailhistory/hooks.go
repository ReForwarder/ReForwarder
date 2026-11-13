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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package emailhistory

import (
	"context"

	"github.com/goposta/posta/internal/models"
)

// This file holds the capture helpers the send pipeline calls at its status
// transitions. Each helper takes the *Recorder as its first argument so a
// nil recorder (feature not wired) is a no-op, and each is a one-liner at the
// call site: recording must never interrupt sending.

// RecordSent records a successful send of em.
func RecordSent(r *Recorder, em *models.Email, metadata map[string]any) {
	r.recordFromEmail(em, EventInput{
		Type:     models.EmailEventSent,
		Metadata: metadata,
	})
}

// RecordFailed records a delivery failure of em with the given error message.
func RecordFailed(r *Recorder, em *models.Email, errMsg string, metadata map[string]any) {
	r.recordFromEmail(em, EventInput{
		Type:     models.EmailEventFailed,
		Error:    errMsg,
		Metadata: metadata,
	})
}

// RecordFailedDeduped records a delivery failure of em carrying its own
// provider-event id, so it coexists with the plain failure events on the same
// timeline instead of being swallowed by them: the
// (email_uuid, type, provider_event_id) unique index collapses replays per id
// rather than collapsing every failure of a message into one row.
func RecordFailedDeduped(r *Recorder, em *models.Email, errMsg, providerEventID string, metadata map[string]any) {
	r.recordFromEmail(em, EventInput{
		Type:            models.EmailEventFailed,
		Error:           errMsg,
		ProviderEventID: providerEventID,
		Metadata:        metadata,
	})
}

// RecordBounced records a hard bounce of em. providerEventID dedups provider
// replays of the same bounce report.
func RecordBounced(r *Recorder, em *models.Email, reason, providerEventID string, metadata map[string]any) {
	r.recordFromEmail(em, EventInput{
		Type:            models.EmailEventBounced,
		Error:           reason,
		ProviderEventID: providerEventID,
		Metadata:        metadata,
	})
}

// RecordForwarded records the forwarding provenance event on a system_forward
// copy's timeline (OB-2, ADR-002 decision 2): the inbound forwarding executor
// enqueued this message as a re-send of a durably stored inbound original.
func RecordForwarded(r *Recorder, em *models.Email, metadata map[string]any) {
	r.recordFromEmail(em, EventInput{
		Type:     models.EmailEventForwarded,
		Metadata: metadata,
	})
}

// providerTracking marks events recorded by the tracking pixel/redirect
// handlers rather than by an ESP integration.
const providerTracking = "tracking"

// RecordOpened records an open of em as seen by the tracking pixel.
func RecordOpened(r *Recorder, em *models.Email, ip, userAgent string) {
	r.recordFromEmail(em, EventInput{
		Type:      models.EmailEventOpened,
		IP:        ip,
		UserAgent: userAgent,
		Provider:  providerTracking,
	})
}

// RecordClicked records a click on em with the original destination URL.
func RecordClicked(r *Recorder, em *models.Email, clickedURL, ip, userAgent string) {
	r.recordFromEmail(em, EventInput{
		Type:      models.EmailEventClicked,
		URL:       clickedURL,
		IP:        ip,
		UserAgent: userAgent,
		Provider:  providerTracking,
	})
}

// RecordUnsubscribed records an unsubscribe of em. When recipient is empty the
// first recipient of the email row is used.
func RecordUnsubscribed(r *Recorder, em *models.Email, recipient string) {
	r.recordFromEmail(em, EventInput{
		Type:      models.EmailEventUnsubscribed,
		Recipient: recipient,
		Provider:  providerTracking,
	})
}

// recordFromEmail fills the denormalized fields of an event from the email row
// and hands it to Record. A nil email (event could not be resolved to a
// message) is dropped — every timeline row keys on a real message.
func (r *Recorder) recordFromEmail(em *models.Email, in EventInput) {
	if r == nil || r.db == nil || em == nil {
		return
	}
	in.EmailUUID = em.UUID
	in.WorkspaceID = em.WorkspaceID
	in.Provider = firstNonEmpty(in.Provider, em.Provider)
	if in.Recipient == "" && len(em.Recipients) > 0 {
		in.Recipient = em.Recipients[0]
	}
	r.Record(context.TODO(), in)
}

// firstNonEmpty returns the first argument that is not the empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
