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
	"fmt"
	"strings"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// EmailSender is the engine's outbound email seam. The adapter below implements
// it over email.Service; tests substitute a fake.
type EmailSender interface {
	SendAutomationEmail(ctx context.Context, automationID uint, tmplID uint, subjectOverride, fromOverride, toEmail string, data map[string]any) (emailUUID string, err error)
}

// The email service renders a template once per message with the reserved
// {{ posta_* }} variables set to internal sentinels, and swaps the sentinels
// for real per-message URLs only when it still finds them in the final content
// (email.HasSystemSentinels / SubstituteSystemLink Sentinels live in
// internal/services/email/system_vars.go and are unexported). The preview
// renderer (Service.RenderTemplate) rewrites the sentinels to the bare variable
// names instead, so the subject-override path below has to swap the names back
// before handing the content to Service.Send. These constants must mirror
// sentinelWebView / sentinelUnsubscribe there; if those ever change, so must
// these.
const (
	sentinelWebViewRestore     = "/__posta_web_view__"
	sentinelUnsubscribeRestore = "/__posta_unsubscribe__"
)

// defaultFromAddress mirrors the email package's default sender; template sends
// without a From fall back to it (and then fail address validation just like
// the platform default does, surfacing a clear node error).
const defaultFromAddress = "noreply@localhost"

// EmailSenderAdapter bridges the automation engine to the platform email
// pipeline (email.Service): it resolves the automation for its ownership scope,
// renders the template, honors the node's subject/from overrides and stamps the
// created Email row with the automation reference so the logs UI can correlate
// automation sends.
type EmailSenderAdapter struct {
	svc            *email.Service
	automationRepo *repositories.AutomationRepository
	emailRepo      *repositories.EmailRepository
}

var _ EmailSender = (*EmailSenderAdapter)(nil)

// NewEmailSenderAdapter builds the adapter over an email.Service. db backs the
// two repositories the adapter needs for scope resolution and reference
// stamping; the engine constructor sites own both.
func NewEmailSenderAdapter(svc *email.Service, db *gorm.DB) EmailSender {
	return &EmailSenderAdapter{
		svc:            svc,
		automationRepo: repositories.NewAutomationRepository(db),
		emailRepo:      repositories.NewEmailRepository(db),
	}
}

// SendAutomationEmail renders template tmplID for toEmail and queues the send
// through email.Service, returning the created email's UUID.
func (a *EmailSenderAdapter) SendAutomationEmail(ctx context.Context, automationID uint, tmplID uint, subjectOverride, fromOverride, toEmail string, data map[string]any) (string, error) {
	if a == nil || a.svc == nil {
		return "", fmt.Errorf("email sender is not configured")
	}
	auto, err := a.automationRepo.GetByID(automationID)
	if err != nil {
		return "", fmt.Errorf("automation %d not found: %w", automationID, err)
	}
	if tmplID == 0 {
		return "", fmt.Errorf("email node has no template: template_id is required")
	}

	var resp *email.SendResponse
	if strings.TrimSpace(subjectOverride) == "" {
		resp, err = a.svc.SendWithTemplate(ctx, auto.UserID, 0, auto.WorkspaceID, toEmail, &email.SendTemplateRequest{
			TemplateID:   &tmplID,
			From:         fromOverride,
			To:           []string{toEmail},
			TemplateData: data,
		})
	} else {
		resp, err = a.sendWithSubjectOverride(ctx, auto, tmplID, subjectOverride, fromOverride, toEmail, data)
	}
	if err != nil {
		return "", err
	}
	if resp == nil || resp.ID == "" {
		return "", fmt.Errorf("email service returned no message id")
	}

	a.stampAutomationRefs(resp.ID, automationID)
	return resp.ID, nil
}

// sendWithSubjectOverride renders the template body via the preview renderer
// and sends it with the operator's literal subject. RenderTemplate is the only
// exported render path, and it is preview-mode: it swaps the per-message link
// sentinels for the bare variable names, so they are swapped back here before
// Service.Send (which substitutes the real URLs when it sees the sentinels).
func (a *EmailSenderAdapter) sendWithSubjectOverride(ctx context.Context, auto *models.Automation, tmplID uint, subjectOverride, fromOverride, toEmail string, data map[string]any) (*email.SendResponse, error) {
	rendered, err := a.svc.RenderTemplate(auto.UserID, auto.WorkspaceID, &tmplID, "", "", data)
	if err != nil {
		return nil, fmt.Errorf("failed to render template %d: %w", tmplID, err)
	}
	from := fromOverride
	if strings.TrimSpace(from) == "" {
		from = defaultFromAddress
	}
	return a.svc.Send(ctx, auto.UserID, 0, auto.WorkspaceID, toEmail, &email.SendRequest{
		From:         from,
		To:           []string{toEmail},
		Subject:      subjectOverride,
		HTML:         restoreSystemLinkSentinels(rendered.HTML),
		Text:         restoreSystemLinkSentinels(rendered.Text),
		TemplateData: data,
		ExternalID:   automationExternalID(auto.ID),
	})
}

// stampAutomationRefs records the automation origin on the Email row. The
// exported send paths carry neither AutomationID nor (for the template path)
// an ExternalID, so the row is patched right after creation, before the send
// worker picks it up. A lost race only costs the metadata, never the message.
func (a *EmailSenderAdapter) stampAutomationRefs(emailUUID string, automationID uint) {
	if a.emailRepo == nil {
		return
	}
	em, err := a.emailRepo.FindByUUID(emailUUID)
	if err != nil || em == nil {
		logger.Debug("automation: could not load email for reference stamping", "email_uuid", emailUUID)
		return
	}
	changed := false
	if em.AutomationID == nil {
		id := automationID
		em.AutomationID = &id
		changed = true
	}
	if em.ExternalID == "" {
		em.ExternalID = automationExternalID(automationID)
		changed = true
	}
	if !changed {
		return
	}
	if err := a.emailRepo.Update(em); err != nil {
		logger.Warn("automation: failed to stamp email references", "email_uuid", emailUUID, "error", err)
	}
}

func automationExternalID(automationID uint) string {
	return fmt.Sprintf("automation:%d", automationID)
}

// restoreSystemLinkSentinels swaps the reserved variable names back to the
// per-message sentinels the real send path substitutes (see the constants
// above). A no-op for content that uses no reserved variables.
func restoreSystemLinkSentinels(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, email.VarWebViewAlias, sentinelWebViewRestore)
	s = strings.ReplaceAll(s, email.VarWebView, sentinelWebViewRestore)
	s = strings.ReplaceAll(s, email.VarUnsubscribe, sentinelUnsubscribeRestore)
	return s
}
