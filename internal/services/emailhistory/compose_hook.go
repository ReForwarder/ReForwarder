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
	"github.com/goposta/posta/internal/models"
)

// RecordComposed records the compose-acceptance event on a webmail compose's
// timeline (OB-5 wave W3, ADR-005 decision 2): the compose seam enqueued this
// message with provenance mail-compose, and metadata carries the Sent-folder
// linkage (mailbox id + minted Message-ID) so the webmail delivery-status view
// can tie the mailstore copy to this timeline from the very first event.
func RecordComposed(r *Recorder, em *models.Email, metadata map[string]any) {
	r.recordFromEmail(em, EventInput{
		Type:     models.EmailEventComposed,
		Metadata: metadata,
	})
}
