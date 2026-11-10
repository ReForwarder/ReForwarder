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

package mailcompose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/services/spamfilter"
)

// In-memory fakes for the compose seam. They mirror the mailstore service's
// observable contract (ownership scoping, per-folder UID claims, \Deleted +
// expunge, blob ref counting) closely enough that the compose-layer decisions
// — gates, ordering, linkage, isolation — are exercised for real; the
// storage-level guarantees themselves are the mailstore suite's own subject
// (fakedb_test.go + the real-Postgres suite).

type mbKey struct {
	userID uint
	name   string
}

type folderKey struct {
	mailboxID uint
	path      string
}

type msgKey struct {
	folderID uint
	uid      uint32
}

// fakeMailstore is the Mailstore fake.
type fakeMailstore struct {
	mu sync.Mutex

	mailboxes  map[uint]*models.Mailbox
	mbByName   map[mbKey]*models.Mailbox
	folders    map[uint]*models.MailFolder
	folderPath map[folderKey]*models.MailFolder
	messages   map[uint]*models.MailMessage
	byMsgKey   map[msgKey]*models.MailMessage

	blobs map[string][]byte
	refs  map[string]int64

	nextID uint

	// appendErr, when set, fails every Append (quota-refusal injection).
	appendErr error
	// expunged records the UIDs expunge removed per folder.
	expunged map[uint][]uint32
	// appends records every Append's (mailbox, folder path, flags, raw).
	appends []appendRecord
}

type appendRecord struct {
	MailboxID uint
	FolderID  uint
	Folder    string
	Flags     uint32
	Raw       []byte
}

func newFakeMailstore() *fakeMailstore {
	return &fakeMailstore{
		mailboxes:  map[uint]*models.Mailbox{},
		mbByName:   map[mbKey]*models.Mailbox{},
		folders:    map[uint]*models.MailFolder{},
		folderPath: map[folderKey]*models.MailFolder{},
		messages:   map[uint]*models.MailMessage{},
		byMsgKey:   map[msgKey]*models.MailMessage{},
		blobs:      map[string][]byte{},
		refs:       map[string]int64{},
		nextID:     100,
		expunged:   map[uint][]uint32{},
	}
}

func (f *fakeMailstore) id() uint {
	f.nextID++
	return f.nextID
}

func (f *fakeMailstore) seedMailbox(userID, workspaceID uint, name string, quotaBytes int64) *models.Mailbox {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createMailboxLocked(userID, workspaceID, name, quotaBytes)
}

func (f *fakeMailstore) createMailboxLocked(userID, workspaceID uint, name string, quotaBytes int64) *models.Mailbox {
	var wsPtr *uint
	if workspaceID != 0 {
		ws := workspaceID
		wsPtr = &ws
	}
	mb := &models.Mailbox{ID: f.id(), UserID: userID, WorkspaceID: wsPtr, Name: name, QuotaBytes: quotaBytes, Status: models.MailboxStatusActive}
	f.mailboxes[mb.ID] = mb
	f.mbByName[mbKey{userID, name}] = mb
	inbox := &models.MailFolder{ID: f.id(), MailboxID: mb.ID, Name: "INBOX", IMAPPath: "INBOX", UIDValidity: 1, UIDNext: 1, ModSeqNext: 1}
	f.folders[inbox.ID] = inbox
	f.folderPath[folderKey{mb.ID, "INBOX"}] = inbox
	return mb
}

func (f *fakeMailstore) FindMailboxByName(userID uint, name string) (*models.Mailbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mb, ok := f.mbByName[mbKey{userID, name}]
	if !ok {
		return nil, mailstore.ErrMailboxNotFound
	}
	return mb, nil
}

func (f *fakeMailstore) CreateMailbox(userID uint, workspaceID *uint, name string, quotaBytes int64) (*models.Mailbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ws := uint(0)
	if workspaceID != nil {
		ws = *workspaceID
	}
	if _, exists := f.mbByName[mbKey{userID, name}]; exists {
		return nil, fmt.Errorf("mailbox %q exists", name)
	}
	return f.createMailboxLocked(userID, ws, name, quotaBytes), nil
}

func (f *fakeMailstore) GetMailbox(mailboxID, userID, workspaceID uint) (*models.Mailbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mb, ok := f.mailboxes[mailboxID]
	if !ok || mb.UserID != userID || (workspaceID != 0 && mb.WorkspaceID != nil && *mb.WorkspaceID != workspaceID) {
		return nil, mailstore.ErrMailboxNotFound
	}
	return mb, nil
}

func (f *fakeMailstore) GetFolderByPath(mailboxID, userID, workspaceID uint, path string) (*models.MailFolder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mb, ok := f.mailboxes[mailboxID]
	if !ok || mb.UserID != userID {
		return nil, mailstore.ErrMailboxNotFound
	}
	folder, ok := f.folderPath[folderKey{mailboxID, path}]
	if !ok {
		return nil, mailstore.ErrFolderNotFound
	}
	return folder, nil
}

func (f *fakeMailstore) CreateFolder(mailboxID, userID, workspaceID uint, parentPath, name string) (*models.MailFolder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mb, ok := f.mailboxes[mailboxID]
	if !ok || mb.UserID != userID {
		return nil, mailstore.ErrMailboxNotFound
	}
	path := name
	if parentPath != "" {
		path = parentPath + "/" + name
	}
	key := folderKey{mailboxID, path}
	if _, exists := f.folderPath[key]; exists {
		return nil, mailstore.ErrFolderExists
	}
	folder := &models.MailFolder{ID: f.id(), MailboxID: mailboxID, Name: name, IMAPPath: path, UIDValidity: 1, UIDNext: 1, ModSeqNext: 1}
	f.folders[folder.ID] = folder
	f.folderPath[key] = folder
	return folder, nil
}

func (f *fakeMailstore) Append(_ context.Context, userID, workspaceID uint, in mailstore.AppendIn) (*mailstore.AppendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appendErr != nil {
		return nil, f.appendErr
	}
	folder, ok := f.folders[in.FolderID]
	if !ok {
		return nil, mailstore.ErrFolderNotFound
	}
	mb, ok := f.mailboxes[folder.MailboxID]
	if !ok || mb.UserID != userID || (workspaceID != 0 && mb.WorkspaceID != nil && *mb.WorkspaceID != workspaceID) {
		return nil, mailstore.ErrNotMailboxOwner
	}
	if len(in.Raw) == 0 {
		return nil, mailstore.ErrEmptyMessage
	}
	uid := folder.UIDNext
	folder.UIDNext++
	folder.ModSeqNext++
	sum := sha256.Sum256(in.Raw)
	hash := hex.EncodeToString(sum[:])
	f.refs[hash]++
	f.blobs[hash] = in.Raw
	msg := &models.MailMessage{
		ID:          f.id(),
		FolderID:    folder.ID,
		UID:         uid,
		MessageID:   messageIDOfRaw(in.Raw),
		Subject:     subjectOfRaw(in.Raw),
		BodyBlobRef: hash,
		Size:        int64(len(in.Raw)),
		Flags:       in.Flags | models.FlagRecent,
		ModSeq:      folder.ModSeqNext,
	}
	f.messages[msg.ID] = msg
	f.byMsgKey[msgKey{folder.ID, uid}] = msg
	f.appends = append(f.appends, appendRecord{MailboxID: mb.ID, FolderID: folder.ID, Folder: folder.IMAPPath, Flags: in.Flags, Raw: append([]byte(nil), in.Raw...)})
	return &mailstore.AppendResult{MessageID: msg.ID, FolderID: folder.ID, UID: uid, ModSeq: msg.ModSeq}, nil
}

func (f *fakeMailstore) GetMessageByUID(folderID uint, uid uint32, userID, _ uint) (*models.MailMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	folder, ok := f.folders[folderID]
	if !ok {
		return nil, mailstore.ErrFolderNotFound
	}
	mb, ok := f.mailboxes[folder.MailboxID]
	if !ok || mb.UserID != userID {
		return nil, mailstore.ErrNotMailboxOwner
	}
	msg, ok := f.byMsgKey[msgKey{folderID, uid}]
	if !ok {
		return nil, mailstore.ErrMessageNotFound
	}
	return msg, nil
}

func (f *fakeMailstore) GetMessageBody(_ context.Context, messageID, userID, _ uint) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msg, ok := f.messages[messageID]
	if !ok {
		return nil, mailstore.ErrMessageNotFound
	}
	folder, ok := f.folders[msg.FolderID]
	if !ok {
		return nil, mailstore.ErrFolderNotFound
	}
	mb, ok := f.mailboxes[folder.MailboxID]
	if !ok || mb.UserID != userID {
		return nil, mailstore.ErrNotMailboxOwner
	}
	return f.blobs[msg.BodyBlobRef], nil
}

func (f *fakeMailstore) MarkDeleted(_, _ uint, messageID uint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	msg, ok := f.messages[messageID]
	if !ok {
		return mailstore.ErrMessageNotFound
	}
	msg.Flags |= models.FlagDeleted
	return nil
}

func (f *fakeMailstore) Expunge(folderID, userID, _ uint) (*mailstore.ExpungeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	folder, ok := f.folders[folderID]
	if !ok {
		return nil, mailstore.ErrFolderNotFound
	}
	mb, ok := f.mailboxes[folder.MailboxID]
	if !ok || mb.UserID != userID {
		return nil, mailstore.ErrNotMailboxOwner
	}
	var uids []uint32
	for key, msg := range f.byMsgKey {
		if msg.FolderID != folderID {
			continue
		}
		if msg.Flags&models.FlagDeleted == 0 {
			continue
		}
		uids = append(uids, msg.UID)
		f.refs[msg.BodyBlobRef]--
		delete(f.byMsgKey, key)
		delete(f.messages, msg.ID)
	}
	f.expunged[folderID] = append(f.expunged[folderID], uids...)
	return &mailstore.ExpungeResult{Expunged: int64(len(uids)), UIDs: uids}, nil
}

func (f *fakeMailstore) ReserveBlob(_ context.Context, content []byte) (mailstore.BlobRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	f.refs[hash]++
	f.blobs[hash] = append([]byte(nil), content...)
	return mailstore.BlobRef{Hash: hash, Size: int64(len(content))}, nil
}

func (f *fakeMailstore) ReleaseBlob(_ context.Context, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refs[hash] <= 0 {
		return fmt.Errorf("no ref for %s", hash)
	}
	f.refs[hash]--
	return nil
}

func (f *fakeMailstore) ReadBlob(_ context.Context, hash string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refs[hash] <= 0 {
		return nil, mailstore.ErrBlobCorrupt
	}
	return f.blobs[hash], nil
}

func (f *fakeMailstore) SweepStaleAttachmentUploads(_ context.Context, _ uint, _ time.Duration) (int64, error) {
	return 0, nil
}

func (f *fakeMailstore) refCount(hash string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refs[hash]
}

func (f *fakeMailstore) folderFor(mailboxID uint, path string) *models.MailFolder {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.folderPath[folderKey{mailboxID, path}]
}

func (f *fakeMailstore) uidExists(folderID uint, uid uint32) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.byMsgKey[msgKey{folderID, uid}]
	return ok
}

// subjectOfRaw pulls the Subject header for the fake's message rows.
func subjectOfRaw(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\r\n") {
		if strings.HasPrefix(line, "Subject:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Subject:"))
		}
		if line == "" {
			break
		}
	}
	return ""
}

// messageIDOfRaw pulls the bare Message-ID header for the fake's rows (the
// draft-replace path reads it back through GetMessageByUID).
func messageIDOfRaw(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\r\n") {
		if strings.HasPrefix(line, "Message-ID:") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "Message-ID:")), "<>")
		}
		if line == "" {
			break
		}
	}
	return ""
}

// fakeSender captures SendMailCompose calls.
type fakeSender struct {
	mu      sync.Mutex
	reqs    []*email.MailComposeRequest
	err     error
	counter int
}

func (s *fakeSender) SendMailCompose(_ context.Context, req *email.MailComposeRequest) (*email.SendResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	s.reqs = append(s.reqs, req)
	s.counter++
	// Echo the pre-minted UUID like the real pipeline does: the response ID
	// IS the row's UUID the compose service fixed in the request.
	id := req.UUID
	if id == "" {
		id = fmt.Sprintf("out-uuid-%d", s.counter)
	}
	return &email.SendResponse{ID: id, Status: models.EmailStatusQueued}, nil
}

func (s *fakeSender) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

// fakeScanner implements Scanner with canned outcomes.
type fakeScanner struct {
	mu       sync.Mutex
	enabled  bool
	failMode spamfilter.ClamFailMode
	scans    int
	// verdict, when non-empty, marks every sample infected with it.
	verdict string
	// err, when set, fails every scan (transport failure).
	err error
}

func (s *fakeScanner) Scan(_ context.Context, _ []byte) (spamfilter.ScanResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scans++
	if s.err != nil {
		return spamfilter.ScanResult{}, s.err
	}
	if s.verdict != "" {
		return spamfilter.ScanResult{Infected: true, Signature: s.verdict}, nil
	}
	return spamfilter.ScanResult{}, nil
}

func (s *fakeScanner) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

func (s *fakeScanner) FailMode() spamfilter.ClamFailMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failMode == "" {
		return spamfilter.ClamFailOpen
	}
	return s.failMode
}

// memStaging is the in-memory StagingStore.
type memStaging struct {
	mu   sync.Mutex
	rows map[string]*models.MailAttachmentUpload
}

func newMemStaging() *memStaging {
	return &memStaging{rows: map[string]*models.MailAttachmentUpload{}}
}

func stagingKey(userID uint, hash string) string { return fmt.Sprintf("%d|%s", userID, hash) }

func (m *memStaging) FindByUserAndHash(userID uint, hash string) (*models.MailAttachmentUpload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[stagingKey(userID, hash)]
	if !ok {
		return nil, nil
	}
	cp := *row
	return &cp, nil
}

func (m *memStaging) Save(u *models.MailAttachmentUpload) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u.ID == 0 {
		u.ID = uint(len(m.rows) + 1)
	}
	cp := *u
	m.rows[stagingKey(u.UserID, u.BlobHash)] = &cp
	return nil
}

func (m *memStaging) DeleteByUserAndHashes(userID uint, hashes []string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, h := range hashes {
		if _, ok := m.rows[stagingKey(userID, h)]; ok {
			delete(m.rows, stagingKey(userID, h))
			n++
		}
	}
	return n, nil
}

func (m *memStaging) count(userID uint) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k := range m.rows {
		if strings.HasPrefix(k, fmt.Sprintf("%d|", userID)) {
			n++
		}
	}
	return n
}

// fakeDelivery is the in-memory DeliveryStore.
type fakeDelivery struct {
	mu     sync.Mutex
	emails map[string]*models.Email
	events map[string][]models.EmailEvent
}

func newFakeDelivery() *fakeDelivery {
	return &fakeDelivery{emails: map[string]*models.Email{}, events: map[string][]models.EmailEvent{}}
}

func (d *fakeDelivery) seed(uuid string, userID uint, em *models.Email, events ...models.EmailEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if em == nil {
		em = &models.Email{}
	}
	em.UUID = uuid
	em.UserID = userID
	d.emails[uuid] = em
	d.events[uuid] = events
}

func (d *fakeDelivery) FindEmailByUUIDForUser(uuid string, userID uint) (*models.Email, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	em, ok := d.emails[uuid]
	if !ok || em.UserID != userID {
		return nil, nil
	}
	return em, nil
}

func (d *fakeDelivery) ListEventsByEmailUUID(uuid string) ([]models.EmailEvent, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]models.EmailEvent(nil), d.events[uuid]...), nil
}

// newTestService wires a Service over the fakes.
func newTestService(store *fakeMailstore, sender *fakeSender, scanner Scanner, staging *memStaging, delivery *fakeDelivery) *Service {
	return New(store, sender, scanner, staging, delivery, Config{AutoProvision: true})
}
