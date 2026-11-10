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

// Package mailcompose is the webmail compose seam (OB-5 wave W3, ADR-005
// decision 2 + ADR-006 scan hook). It glues the two mail stores together for
// user-authored mail:
//
//   - the hosted-mailbox store (services/mailstore) receives the Sent-folder
//     and Drafts-folder copies, through the same quota-enforced Append path
//     every other message takes;
//   - the outbound pipeline (services/email, ADR-001 direct/relay per domain,
//     DKIM-signed, attempts recorded) receives the transport copy, stamped
//     with provenance mail-compose;
//   - the spamfilter clamd client (ADR-006 decision 2) scans every attachment
//     at upload, before the ref may ever reach a send; positives are refused
//     with the signature and the configured fail mode is honored.
//
// The two stores share no rows (models/mailbox.go): their only link is the
// X-Posta-Email-Uuid header the compose service bakes into the Sent copy and
// the outbound row's headers — that UUID keys the email_events timeline the
// delivery-status view reads back.
package mailcompose

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/services/spamfilter"
)

// Typed outcomes the HTTP layer maps to responses. Expected failures are
// typed so the handler never string-matches errors.
var (
	// ErrNoMailbox: auto-provision is disabled and the user owns no mailbox
	// for the sending address (map: 409).
	ErrNoMailbox = errors.New("mailcompose: no mailbox to compose from")
	// ErrNotMailboxOwner: the addressed mailbox/folder/message belongs to
	// another user (map: 404 — never confirm existence to a foreign caller).
	ErrNotMailboxOwner = errors.New("mailcompose: mailbox belongs to another user")
	// ErrAttachmentNotStaged: a send/draft referenced an attachment ref this
	// user never staged (or one whose scan did not pass) (map: 400).
	ErrAttachmentNotStaged = errors.New("mailcompose: attachment reference is not staged")
	// ErrScannerUnavailable: the malware scanner could not be reached and the
	// fail mode is closed (ADR-006) (map: 503).
	ErrScannerUnavailable = errors.New("mailcompose: malware scanner unavailable")
	// ErrNothingToSend: no recipient and no content at all.
	ErrNothingToSend = errors.New("mailcompose: message has no recipients")
	// ErrMessageTooLarge / attachment caps.
	ErrAttachmentTooLarge = errors.New("mailcompose: attachment exceeds the size limit")
)

// AttachmentInfectedError is the ADR-006 positive verdict: the upload is
// refused and the clamd signature is carried to the caller (map: 409).
type AttachmentInfectedError struct {
	Name      string
	Signature string
}

func (e *AttachmentInfectedError) Error() string {
	return fmt.Sprintf("mailcompose: attachment %q refused: malware signature %s", e.Name, e.Signature)
}

// Mailstore is the slice of the mailstore service the compose seam consumes.
// *mailstore.Service implements it (compile-time asserted below).
type Mailstore interface {
	FindMailboxByName(userID uint, name string) (*models.Mailbox, error)
	CreateMailbox(userID uint, workspaceID *uint, name string, quotaBytes int64) (*models.Mailbox, error)
	GetMailbox(mailboxID, userID, workspaceID uint) (*models.Mailbox, error)
	GetFolderByPath(mailboxID, userID, workspaceID uint, path string) (*models.MailFolder, error)
	CreateFolder(mailboxID, userID, workspaceID uint, parentPath, name string) (*models.MailFolder, error)
	Append(ctx context.Context, userID, workspaceID uint, in mailstore.AppendIn) (*mailstore.AppendResult, error)
	GetMessageByUID(folderID uint, uid uint32, userID, workspaceID uint) (*models.MailMessage, error)
	GetMessageBody(ctx context.Context, messageID, userID, workspaceID uint) ([]byte, error)
	MarkDeleted(userID, workspaceID uint, messageID uint) error
	Expunge(folderID, userID, workspaceID uint) (*mailstore.ExpungeResult, error)
	ReserveBlob(ctx context.Context, content []byte) (mailstore.BlobRef, error)
	ReleaseBlob(ctx context.Context, hash string) error
	ReadBlob(ctx context.Context, hash string) ([]byte, error)
	SweepStaleAttachmentUploads(ctx context.Context, userID uint, maxAge time.Duration) (int64, error)
}

var _ Mailstore = (*mailstore.Service)(nil)

// Scanner is the ADR-006 malware-scan seam; *spamfilter.ClamdClient implements
// it. Tests stand in with fakes or the real client against an in-process clamd.
type Scanner interface {
	Scan(ctx context.Context, sample []byte) (spamfilter.ScanResult, error)
	Enabled() bool
	FailMode() spamfilter.ClamFailMode
}

var _ Scanner = (*spamfilter.ClamdClient)(nil)

// Sender pushes the transport copy into the outbound pipeline.
type Sender interface {
	SendMailCompose(ctx context.Context, req *email.MailComposeRequest) (*email.SendResponse, error)
}

// StagingStore persists the per-user attachment staging rows.
type StagingStore interface {
	FindByUserAndHash(userID uint, hash string) (*models.MailAttachmentUpload, error)
	Save(u *models.MailAttachmentUpload) error
	DeleteByUserAndHashes(userID uint, hashes []string) (int64, error)
}

// DeliveryStore reads the outbound timeline back for the delivery-status view.
type DeliveryStore interface {
	// FindEmailByUUIDForUser returns the outbound row only when it exists AND
	// belongs to userID — the compose surface must never leak another user's
	// message timeline, even for a UUID that was guessed.
	FindEmailByUUIDForUser(uuid string, userID uint) (*models.Email, error)
	ListEventsByEmailUUID(uuid string) ([]models.EmailEvent, error)
}

// Config carries the compose seam's deployment knobs.
type Config struct {
	// AutoProvision creates the sender's mailbox (named after the From
	// address) on first compose, mirroring the inbound seam's provisioning
	// rule; false refuses with ErrNoMailbox (POSTA_MAILSTORE_AUTO_PROVISION).
	AutoProvision bool
	// MaxAttachmentBytes caps one uploaded attachment (default 25 MiB, the
	// clamd INSTREAM default — a larger sample could never be scanned).
	MaxAttachmentBytes int64
	// MaxTotalAttachmentBytes caps the per-message attachment total
	// (default 25 MiB, matching the transactional send's total cap).
	MaxTotalAttachmentBytes int64
	// UploadTTL is how long an unreferenced staging row (and its blob
	// reservation) survives before the opportunistic sweep releases it.
	UploadTTL time.Duration
}

// Service is the compose seam.
type Service struct {
	store    Mailstore
	sender   Sender
	scanner  Scanner
	staging  StagingStore
	delivery DeliveryStore
	cfg      Config
}

// New wires the compose seam. A nil scanner disables scanning entirely
// (state unavailable, fail-open by configuration — ADR-006 is config-gated);
// production wiring always passes the deployment's ClamdClient.
func New(store Mailstore, sender Sender, scanner Scanner, staging StagingStore, delivery DeliveryStore, cfg Config) *Service {
	if cfg.MaxAttachmentBytes <= 0 {
		cfg.MaxAttachmentBytes = 25 * 1024 * 1024
	}
	if cfg.MaxTotalAttachmentBytes <= 0 {
		cfg.MaxTotalAttachmentBytes = 25 * 1024 * 1024
	}
	if cfg.UploadTTL <= 0 {
		cfg.UploadTTL = 7 * 24 * time.Hour
	}
	return &Service{store: store, sender: sender, scanner: scanner, staging: staging, delivery: delivery, cfg: cfg}
}

// AttachmentRef names one staged attachment inside a compose/draft request:
// the ref returned by the attachment-upload endpoint.
type AttachmentRef struct {
	BlobHash    string `json:"blob_hash"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
}

// MessageInput is the shared body of send and draft requests (ADR-005
// decision 2: to/cc/bcc + subject + body + attachment refs).
type MessageInput struct {
	// MailboxID selects the hosted mailbox the message belongs to; 0 resolves
	// (or provisions) the mailbox named after the From address.
	MailboxID   uint            `json:"mailbox_id,omitempty"`
	From        string          `json:"from" example:"Ada <ada@example.com>"`
	To          []string        `json:"to"`
	Cc          []string        `json:"cc,omitempty"`
	Bcc         []string        `json:"bcc,omitempty"`
	Subject     string          `json:"subject"`
	Text        string          `json:"text,omitempty"`
	HTML        string          `json:"html,omitempty"`
	Attachments []AttachmentRef `json:"attachments,omitempty"`
}

// envelopeRecipients returns the deduplicated, validated envelope recipients:
// to + cc + bcc. Bcc is envelope-only — it never appears on the stored or
// transported headers, only in the recipient list the MTA delivers to.
func envelopeRecipients(in MessageInput) ([]string, []string, []string, error) {
	if err := validateAddress(in.From); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid from address: %w", err)
	}
	to, err := parseAddressList(in.To)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid to address: %w", err)
	}
	cc, err := parseAddressList(in.Cc)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid cc address: %w", err)
	}
	bcc, err := parseAddressList(in.Bcc)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid bcc address: %w", err)
	}
	if len(to)+len(cc)+len(bcc) == 0 && strings.TrimSpace(in.Subject) == "" &&
		strings.TrimSpace(in.Text) == "" && strings.TrimSpace(in.HTML) == "" {
		return nil, nil, nil, ErrNothingToSend
	}
	return to, cc, bcc, nil
}

// resolveMailbox finds the mailbox a compose request operates on: the explicit
// mailbox when named (ownership-checked here, the same scoping every mailstore
// read applies), otherwise the mailbox named after the sending address,
// provisioned on first use when auto-provision is on.
func (s *Service) resolveMailbox(ctx context.Context, userID, workspaceID uint, in MessageInput) (*models.Mailbox, error) {
	if in.MailboxID != 0 {
		mb, err := s.store.GetMailbox(in.MailboxID, userID, workspaceID)
		if err != nil {
			if errors.Is(err, mailstore.ErrNotMailboxOwner) || errors.Is(err, mailstore.ErrMailboxNotFound) {
				return nil, ErrNotMailboxOwner
			}
			return nil, fmt.Errorf("resolve mailbox %d: %w", in.MailboxID, err)
		}
		return mb, nil
	}
	return s.resolveMailboxByName(ctx, userID, workspaceID, strings.ToLower(addressOnly(in.From)))
}

// resolveMailboxByName implements resolveMailbox's address-named path.
func (s *Service) resolveMailboxByName(ctx context.Context, userID, workspaceID uint, addr string) (*models.Mailbox, error) {
	mb, err := s.store.FindMailboxByName(userID, addr)
	if err == nil {
		return mb, nil
	}
	if !errors.Is(err, mailstore.ErrMailboxNotFound) {
		return nil, fmt.Errorf("find mailbox %q: %w", addr, err)
	}
	if !s.cfg.AutoProvision {
		return nil, ErrNoMailbox
	}
	var wsPtr *uint
	if workspaceID != 0 {
		ws := workspaceID
		wsPtr = &ws
	}
	mb, err = s.store.CreateMailbox(userID, wsPtr, addr, 0)
	if err == nil {
		return mb, nil
	}
	// Lost a provisioning race (or any create failure): re-lookup once — the
	// (user_id, name) unique index means a concurrent creator's mailbox is
	// findable now (same policy as the inbound seam).
	mb, findErr := s.store.FindMailboxByName(userID, addr)
	if findErr == nil {
		return mb, nil
	}
	return nil, err
}

// UploadAttachment scans (ADR-006) and stages one compose attachment into the
// blob layer, returning the ref compose requests reference. Positives are
// refused with the signature and nothing is stored; an unavailable scanner is
// refused under a closed fail mode and accepted unscanned under an open one
// (the row records which).
func (s *Service) UploadAttachment(ctx context.Context, userID, workspaceID uint, name, contentType string, content []byte) (*models.MailAttachmentUpload, error) {
	if len(content) == 0 {
		return nil, errors.New("mailcompose: attachment content is empty")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "unnamed"
	}
	if int64(len(content)) > s.cfg.MaxAttachmentBytes {
		return nil, ErrAttachmentTooLarge
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	scanState := string(models.MailAttachmentScanUnavailable)
	if s.scanner.Enabled() {
		res, err := s.scanner.Scan(ctx, content)
		if err != nil {
			if s.scanner.FailMode() == spamfilter.ClamFailClosed {
				return nil, fmt.Errorf("%w: %v", ErrScannerUnavailable, err)
			}
			// Fail-open (ADR-006 default): an unavailable scanner must not
			// stop the mail flow; the upload is accepted unscanned.
		} else if res.Infected {
			return nil, &AttachmentInfectedError{Name: name, Signature: res.Signature}
		} else {
			scanState = string(models.MailAttachmentScanClean)
		}
	}

	// Opportunistic housekeeping: release this user's long-abandoned staging
	// reservations so unreferenced blobs stay collectable. Failures are
	// non-fatal (best-effort).
	_, _ = s.store.SweepStaleAttachmentUploads(ctx, userID, s.cfg.UploadTTL)

	// Content-addressed dedup: identical content from the same user stages
	// once; a re-upload refreshes the metadata and keeps one reservation.
	if existing, err := s.staging.FindByUserAndHash(userID, hashOf(content)); err == nil && existing != nil {
		existing.Name = name
		existing.ContentType = contentType
		if err := s.staging.Save(existing); err != nil {
			return nil, fmt.Errorf("save staging row: %w", err)
		}
		return existing, nil
	}

	ref, err := s.store.ReserveBlob(ctx, content)
	if err != nil {
		return nil, fmt.Errorf("reserve staged attachment: %w", err)
	}
	row := &models.MailAttachmentUpload{
		UserID:      userID,
		WorkspaceID: workspacePtr(workspaceID),
		BlobHash:    ref.Hash,
		Name:        name,
		ContentType: contentType,
		Size:        ref.Size,
		ScanState:   scanState,
	}
	if err := s.staging.Save(row); err != nil {
		// The reservation would leak; release it best-effort.
		_ = s.store.ReleaseBlob(ctx, ref.Hash)
		return nil, fmt.Errorf("save staging row: %w", err)
	}
	return row, nil
}

// gateAttachments verifies every referenced attachment was staged by THIS user
// and passed the ADR-006 scan (or was accepted unscanned under an open fail
// mode). It returns the staging rows in request order.
func (s *Service) gateAttachments(userID uint, refs []AttachmentRef) ([]*models.MailAttachmentUpload, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	var total int64
	rows := make([]*models.MailAttachmentUpload, 0, len(refs))
	for _, r := range refs {
		if r.BlobHash == "" {
			return nil, ErrAttachmentNotStaged
		}
		row, err := s.staging.FindByUserAndHash(userID, r.BlobHash)
		if err != nil || row == nil {
			// A foreign or unknown hash: never confirm which.
			return nil, ErrAttachmentNotStaged
		}
		total += row.Size
		if total > s.cfg.MaxTotalAttachmentBytes {
			return nil, ErrAttachmentTooLarge
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// consumeStaging drops the staging rows (and their reservations) of the refs a
// stored message now covers: the Append reserved its own references, so the
// staging reservation must go or the blob would be pinned forever after the
// rows are gone. Best-effort: a failed release leaves an over-counted ref
// (a leak the GC cannot collect, never a correctness hole) and is returned as
// an error the caller logs.
func (s *Service) consumeStaging(ctx context.Context, userID uint, rows []*models.MailAttachmentUpload) {
	if len(rows) == 0 {
		return
	}
	hashes := make([]string, 0, len(rows))
	for _, r := range rows {
		hashes = append(hashes, r.BlobHash)
	}
	if _, err := s.staging.DeleteByUserAndHashes(userID, hashes); err != nil {
		return
	}
	for _, h := range hashes {
		_ = s.store.ReleaseBlob(ctx, h)
	}
}
