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

package inbound

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
)

// OB-3 wave W5 (flow F13, ADR-003 + ADR-002): the ingestion seam between the
// routing engine and the hosted-mailbox store. A route action=deliver keeps
// today's durable archive behavior and ADDITIONALLY files the message into
// the recipient's INBOX through mailstore.Append — quota-enforced, with the
// typed over-quota outcome quarantined per ADR-002 (the message is already
// durably archived, so a refused append never loses it).
//
// Provisioning rule (the ask, ADR-003 decision 1): a mailbox is auto-
// provisioned for the tenant user on first delivery to a verified domain
// bound to that user, gated by MailboxDeliveryConfig.AutoProvision
// (POSTA_MAILSTORE_AUTO_PROVISION, default true). The mailbox is named by the
// full recipient address (lowercased) — (user_id, name) is the unique mailbox
// identity, so one hosted address maps to one mailbox of the domain's owner.
// No mailbox (auto-provision off, or provisioning refused) → behave exactly
// as today: archive only.
//
// Binding rules:
//   - The message is delivered from the ALREADY durably persisted record —
//     the seam runs after repo.Create (ADR-002 non-negotiable), so a failed
//     append can only fall back to the archive, never lose the message.
//   - Routing resolves only through tenant-verified domains: the seam
//     re-checks the record domain's OwnershipVerified (fail-closed) before
//     touching the mailstore, and only recipients on that domain are
//     delivered to.
//   - Over-quota is the ONE append failure mapped to quarantine (typed reason
//     prefix QuarantineReasonMailboxQuota): the copy must be recoverable by
//     the review/release path, which re-runs routing. Every other append
//     failure is logged and falls back to archive-only — the owner's mail is
//     not silently held for an infrastructure fault.
//   - The transactional Email model, subscribers and every marketing path
//     are untouched: the seam reads nothing and writes nothing outside the
//     mailstore tables and the ADR-002 quarantine seam.

// QuarantineReasonMailboxQuota is the stable, typed reason prefix written to
// inbound_quarantine.reason when a delivery is held because the recipient
// mailbox is over quota (ADR-003 decision 5 via ADR-002 §3). The remainder of
// the reason carries the structured *mailstore.QuotaError fields (scope,
// used, incoming, limit).
const QuarantineReasonMailboxQuota = "mailbox_quota_exceeded"

// MailboxDeliverer is the seam the route-action executor calls for a matched
// action=deliver. Implemented by *MailboxDelivery; a nil seam (the default)
// keeps the exact pre-OB-3 behavior: archive only.
type MailboxDeliverer interface {
	// DeliverToMailbox files the durably persisted message into the
	// recipient mailbox(es)' INBOX and reports what was filed. It never
	// mutates the inbound record itself (status flips are the executor's
	// job) and returns a *mailstore.QuotaError (match with errors.As /
	// errors.Is(err, mailstore.ErrQuotaExceeded)) when a recipient mailbox
	// refused the append for quota.
	DeliverToMailbox(ctx context.Context, rec *models.InboundEmail, raw []byte) (*MailboxDeliveryResult, error)
}

// MailboxDeliveryResult summarizes one deliver action's mailstore outcomes.
type MailboxDeliveryResult struct {
	// Mailboxes lists the mailbox names (lowercased recipient addresses) the
	// message was appended into.
	Mailboxes []string
}

// MailboxDeliveryConfig carries the ingestion seam's deployment knobs.
type MailboxDeliveryConfig struct {
	// AutoProvision creates the recipient mailbox on first delivery to a
	// verified domain bound to the tenant user (POSTA_MAILSTORE_AUTO_PROVISION,
	// default true). When false, only an already-provisioned mailbox receives;
	// otherwise the message stays archive-only.
	AutoProvision bool
}

// MailboxDelivery implements MailboxDeliverer over the mailstore service.
type MailboxDelivery struct {
	store   *mailstore.Service
	domains *repositories.DomainRepository
	cfg     MailboxDeliveryConfig
	// aliases is the SF-65 alias/group expansion seam; nil (the default until
	// wired) keeps the exact pre-SF-65 behavior: direct resolution only.
	aliases AliasGroupResolver
}

// NewMailboxDelivery builds the seam over a mailstore service and the domain
// repository used for the verified-domain re-check.
func NewMailboxDelivery(store *mailstore.Service, domains *repositories.DomainRepository, cfg MailboxDeliveryConfig) *MailboxDelivery {
	return &MailboxDelivery{store: store, domains: domains, cfg: cfg}
}

// AliasGroupResolver is the SF-65 expansion seam: the mail-account alias and
// group mappings (owner-scoped mail_aliases/mail_groups tables) the deliver
// action expands recipients through. The repositories satisfy it; nil (the
// default) disables expansion — direct delivery only, byte-for-byte the
// pre-SF-65 behavior. Every method resolves through the RECORD OWNER's
// mappings only: another user's alias or group for the same address never
// resolves (the tenant boundary).
type AliasGroupResolver interface {
	// ResolveAliasAddress returns the target mailbox ID for the owner's alias
	// at address, ok=false when none.
	ResolveAliasAddress(userID uint, address string) (mailboxID uint, ok bool)
	// ResolveGroupAddress returns the member mailbox IDs of the owner's group
	// at address, ok=false when none.
	ResolveGroupAddress(userID uint, address string) (members []uint, ok bool)
	// ResolveGroupByMailbox returns the member mailbox IDs when the mailbox
	// anchors one of the owner's groups (list-of-lists expansion), ok=false
	// otherwise.
	ResolveGroupByMailbox(userID, mailboxID uint) (members []uint, ok bool)
}

// SetAliasGroups attaches the SF-65 alias/group expansion seam (nil = none).
func (d *MailboxDelivery) SetAliasGroups(r AliasGroupResolver) { d.aliases = r }

// maxGroupExpansionDepth bounds list-of-lists expansion: groups may include
// other groups' anchor mailboxes, and the recursion stops at this depth even
// for a (pathological) acyclic deep nesting. Cycles are cut earlier by the
// expanded set.
const maxGroupExpansionDepth = 5

// expansionState carries one DeliverToMailbox call's fan-out bookkeeping.
type expansionState struct {
	// delivered holds mailbox IDs that already received an append attempt —
	// the idempotency set: one copy per mailbox however many paths reach it
	// (direct address, alias target, group membership).
	delivered map[uint]bool
	// expanded holds group ANCHOR mailbox IDs whose member list was already
	// expanded — the cycle cut for nested/cyclic group configurations (it is
	// separate from delivered: an anchor itself receives no copy from its
	// group's expansion, but a directly addressed anchor must still get one).
	expanded map[uint]bool
	res      *MailboxDeliveryResult
	quotaErr error
	otherErr error
}

// DeliverToMailbox implements MailboxDeliverer. See the package-file comment
// for the binding rules. Best-effort per recipient: every addressed mailbox
// on the record's verified domain gets an append attempt; the first over-
// quota error wins the return value (quarantine dominates), then the first
// other error; success returns the delivered mailbox names. SF-65: an
// address that is one of the owner's aliases delivers to the alias's target
// mailbox; an address that is one of the owner's groups fans out to every
// member mailbox idempotently (with nested groups expanded, cycles cut, and
// the depth bounded).
func (d *MailboxDelivery) DeliverToMailbox(ctx context.Context, rec *models.InboundEmail, raw []byte) (*MailboxDeliveryResult, error) {
	if d == nil || d.store == nil || rec == nil {
		return nil, nil
	}
	if len(raw) == 0 {
		// Nothing to file — the record's durable archive copy is the
		// fallback, exactly as before this seam existed.
		return nil, nil
	}

	// Fail-closed verified-domain re-check (defense in depth on top of the
	// ingest-time resolveDomain and the release path's check): the mailstore
	// only ever receives mail that arrived on a tenant-verified domain. The
	// domain also names the only recipients this record can deliver to.
	if d.domains == nil {
		logger.Warn("mailbox delivery seam has no domain repository; skipping", "inbound_id", rec.UUID)
		return nil, nil
	}
	domain, err := d.domains.FindByID(rec.DomainID)
	if err != nil {
		return nil, fmt.Errorf("load domain %d for mailbox delivery: %w", rec.DomainID, err)
	}
	if !domain.OwnershipVerified {
		logger.Warn("mailbox delivery skipped: message domain is no longer verified",
			"inbound_id", rec.UUID, "domain_id", rec.DomainID)
		return nil, nil
	}

	st := &expansionState{
		delivered: make(map[uint]bool),
		expanded:  make(map[uint]bool),
		res:       &MailboxDeliveryResult{Mailboxes: []string{}},
	}
	seen := make(map[string]bool, len(rec.Recipients))
	for _, rcpt := range rec.Recipients {
		addr := strings.ToLower(strings.TrimSpace(rcpt))
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		_, host, ok := splitAddress(addr)
		if !ok {
			continue
		}
		// Deliver only to recipients on the record's verified domain: the
		// route matched through that domain, and provisioning is bound to its
		// owner. Recipients the SMTP backend accepted on other verified
		// domains are separate records' concern (resolveDomain resolves one
		// domain per stored record).
		if !strings.EqualFold(host, domain.Domain) {
			continue
		}

		// SF-65 expansion, in fixed order: alias, then group; an address that
		// is neither resolves (and provisions) directly — the exact pre-SF-65
		// path. A nil resolver skips straight to the direct path.
		if d.aliases != nil {
			if targetID, ok := d.aliases.ResolveAliasAddress(rec.UserID, addr); ok {
				st.append(ctx, d, rec, raw, targetID, fmt.Sprintf("alias %q", addr))
				continue
			}
			if memberIDs, ok := d.aliases.ResolveGroupAddress(rec.UserID, addr); ok {
				d.expandGroup(ctx, rec, raw, memberIDs, 1, st, addr)
				continue
			}
		}

		mb, err := d.resolveOrProvision(rec.UserID, rec.WorkspaceID, addr)
		if err != nil {
			if errors.Is(err, errMailboxNotProvisioned) {
				continue // no mailbox, auto-provision off: archive only
			}
			if st.otherErr == nil {
				st.otherErr = fmt.Errorf("resolve mailbox %q: %w", addr, err)
			}
			continue
		}
		st.append(ctx, d, rec, raw, mb.ID, fmt.Sprintf("recipient %q", addr))
	}
	if st.quotaErr != nil {
		return st.res, st.quotaErr
	}
	return st.res, st.otherErr
}

// expandGroup fans one group's delivery out to its member mailboxes. A
// member that anchors one of the owner's groups expands recursively (its
// members receive; the anchor itself gets no copy from its own group) with
// the expanded set cutting cycles and re-expansion and the depth bounded by
// maxGroupExpansionDepth — a cyclic configuration terminates, every member
// mailbox receives at most one copy.
func (d *MailboxDelivery) expandGroup(ctx context.Context, rec *models.InboundEmail, raw []byte, memberIDs []uint, depth int, st *expansionState, groupAddr string) {
	if depth > maxGroupExpansionDepth {
		logger.Warn("inbound group expansion depth exceeded; cutting",
			"inbound_id", rec.UUID, "group", groupAddr, "depth", depth)
		return
	}
	for _, mbID := range memberIDs {
		if d.aliases == nil {
			break // seam unwired between calls: no further expansion
		}
		if st.delivered[mbID] {
			continue // idempotency: one copy per mailbox
		}
		if nested, ok := d.aliases.ResolveGroupByMailbox(rec.UserID, mbID); ok {
			if st.expanded[mbID] {
				continue // cycle cut (or already expanded): never re-expand
			}
			st.expanded[mbID] = true
			d.expandGroup(ctx, rec, raw, nested, depth+1, st, groupAddr)
			continue
		}
		st.append(ctx, d, rec, raw, mbID, fmt.Sprintf("group %q member", groupAddr))
	}
}

// append files the message into ONE mailbox (by ID) exactly once per
// delivery: an already-attempted mailbox is skipped. Resolution goes through
// the owner-scoped GetMailbox, so a missing or foreign mailbox (a stale
// alias target, a member owned by another user) can never receive — that is
// the isolation boundary, skipped without failing the delivery. Over quota
// is the typed dominant outcome, exactly as on the direct path.
func (st *expansionState) append(ctx context.Context, d *MailboxDelivery, rec *models.InboundEmail, raw []byte, mailboxID uint, source string) {
	if st.delivered[mailboxID] {
		return
	}
	mb, err := d.store.GetMailbox(mailboxID, rec.UserID, 0)
	if err != nil {
		if errors.Is(err, mailstore.ErrMailboxNotFound) || errors.Is(err, mailstore.ErrNotMailboxOwner) {
			logger.Warn("mailbox delivery skipped: target mailbox missing or foreign",
				"inbound_id", rec.UUID, "mailbox_id", mailboxID, "source", source)
			st.delivered[mailboxID] = true
			return
		}
		if st.otherErr == nil {
			st.otherErr = fmt.Errorf("resolve mailbox %d (%s): %w", mailboxID, source, err)
		}
		return
	}
	st.delivered[mailboxID] = true
	if _, err := d.appendToInbox(ctx, rec.UserID, mb, raw); err != nil {
		if errors.Is(err, mailstore.ErrQuotaExceeded) {
			// Wrap keeps errors.As/Is working for the executor while naming
			// the refusing mailbox in the chain.
			if st.quotaErr == nil {
				st.quotaErr = fmt.Errorf("mailbox %q over quota: %w", mb.Name, err)
			}
			return
		}
		if st.otherErr == nil {
			st.otherErr = fmt.Errorf("append to mailbox %q: %w", mb.Name, err)
		}
		return
	}
	st.res.Mailboxes = append(st.res.Mailboxes, mb.Name)
}

// errMailboxNotProvisioned — the recipient has no mailbox and auto-provision
// is disabled: not an error, the archive-only fallback.
var errMailboxNotProvisioned = errors.New("inbound: recipient has no mailbox and auto-provision is disabled")

// resolveOrProvision finds the recipient's mailbox, creating it on first
// delivery when auto-provision is on. The (user_id, name) unique index is the
// race backstop: a concurrent first delivery either sees the other's mailbox
// on the re-lookup or loses the duplicate insert, which the re-lookup then
// resolves.
func (d *MailboxDelivery) resolveOrProvision(userID uint, workspaceID *uint, addr string) (*models.Mailbox, error) {
	mb, err := d.store.FindMailboxByName(userID, addr)
	if err == nil {
		return mb, nil
	}
	if !errors.Is(err, mailstore.ErrMailboxNotFound) {
		return nil, err
	}
	if !d.cfg.AutoProvision {
		return nil, errMailboxNotProvisioned
	}
	mb, err = d.store.CreateMailbox(userID, workspaceID, addr, 0)
	if err == nil {
		return mb, nil
	}
	// Lost a provisioning race (or any create failure): re-lookup once. A
	// unique-index loser finds the winner's mailbox; anything else surfaces.
	mb, findErr := d.store.FindMailboxByName(userID, addr)
	if findErr == nil {
		return mb, nil
	}
	return nil, err
}

// appendToInbox files the raw message into the mailbox's INBOX. The internal
// system path scopes by user (workspace 0): the mailbox was resolved through
// the owner's (user_id, name) identity, and the mailstore's workspace check
// only narrows when both sides are non-zero.
func (d *MailboxDelivery) appendToInbox(ctx context.Context, userID uint, mb *models.Mailbox, raw []byte) (*mailstore.AppendResult, error) {
	inbox, err := d.store.GetFolderByPath(mb.ID, userID, 0, "INBOX")
	if err != nil {
		return nil, fmt.Errorf("resolve INBOX of mailbox %d: %w", mb.ID, err)
	}
	return d.store.Append(ctx, userID, 0, mailstore.AppendIn{
		MailboxID: mb.ID,
		FolderID:  inbox.ID,
		Raw:       raw,
		// No flags preset: inbound delivery arrives unread (\Recent only),
		// like any IMAP LDA.
	})
}
