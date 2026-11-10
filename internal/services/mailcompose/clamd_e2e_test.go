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
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/spamfilter"
)

// End-to-end ADR-006 check: the REAL ClamdClient (the production scanner
// seam the routes wire) against an in-process fake clamd speaking the INSTREAM
// TCP protocol — proving the compose scan gate works against the actual client
// behavior, not just a stand-in.

// eicarTestSample is the industry-standard benign antivirus test payload.
const eicarTestSample = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`

// fakeClamdServer is a minimal clamd: reads zINSTREAM\0 + length-prefixed
// chunks, answers each completed scan with the configured response line
// (NUL-terminated, as the protocol requires).
type fakeClamdServer struct {
	listener net.Listener
	mu       sync.Mutex
	response string
	scans    int
}

func startFakeClamdServer(t *testing.T, response string) *fakeClamdServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeClamdServer{listener: l, response: response}
	go s.serve()
	t.Cleanup(func() { _ = l.Close() })
	return s
}

func (s *fakeClamdServer) host() string { return "127.0.0.1" }
func (s *fakeClamdServer) port() int    { return s.listener.Addr().(*net.TCPAddr).Port }

func (s *fakeClamdServer) scanCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scans
}

func (s *fakeClamdServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeClamdServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	// Read the command (NUL-terminated).
	var cmd []byte
	one := make([]byte, 1)
	for {
		if _, err := io.ReadFull(conn, one); err != nil {
			return
		}
		if one[0] == 0 {
			break
		}
		cmd = append(cmd, one[0])
	}
	if !strings.HasPrefix(string(cmd), "zINSTREAM") {
		// Unknown command: answer with an error line.
		_, _ = conn.Write([]byte("UNKNOWN COMMAND\x00"))
		return
	}
	// Drain length-prefixed chunks until the zero-length terminator.
	var lenBuf [4]byte
	for {
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(lenBuf[:])
		if n == 0 {
			break
		}
		if _, err := io.CopyN(io.Discard, conn, int64(n)); err != nil {
			return
		}
	}
	s.mu.Lock()
	s.scans++
	resp := s.response
	s.mu.Unlock()
	_, _ = conn.Write([]byte(resp + "\x00"))
}

// TestUploadAttachmentAgainstRealClamdClient: the production ClamdClient
// detects the EICAR sample through the wire protocol, and the compose gate
// refuses the upload with the signature (ADR-006 decision 2).
func TestUploadAttachmentAgainstRealClamdClient(t *testing.T) {
	fake := startFakeClamdServer(t, "stream: Eicar-Signature FOUND")
	client := spamfilter.NewClamdClient(spamfilter.ClamdConfig{
		Host:    fake.host(),
		Port:    fake.port(),
		Timeout: 1_000_000_000, // 1s
	})
	store := newFakeMailstore()
	staging := newMemStaging()
	svc := newTestService(store, &fakeSender{}, client, staging, newFakeDelivery())

	_, err := svc.UploadAttachment(context.Background(), 7, 0, "eicar.txt", "text/plain", []byte(eicarTestSample))
	var infected *AttachmentInfectedError
	if !errors.As(err, &infected) {
		t.Fatalf("err = %v, want *AttachmentInfectedError", err)
	}
	if infected.Signature != "Eicar-Signature" {
		t.Errorf("signature = %q, want Eicar-Signature", infected.Signature)
	}
	if got := fake.scanCount(); got != 1 {
		t.Errorf("clamd scans = %d, want 1", got)
	}
	if staging.count(7) != 0 {
		t.Errorf("infected upload staged rows: %d", staging.count(7))
	}

	// Clean verdict: "stream: OK".
	cleanFake := startFakeClamdServer(t, "stream: OK")
	cleanClient := spamfilter.NewClamdClient(spamfilter.ClamdConfig{
		Host: cleanFake.host(), Port: cleanFake.port(), Timeout: 1_000_000_000,
	})
	svcClean := newTestService(store, &fakeSender{}, cleanClient, staging, newFakeDelivery())
	row, err := svcClean.UploadAttachment(context.Background(), 7, 0, "safe.txt", "text/plain", []byte("just text"))
	if err != nil {
		t.Fatalf("clean upload: %v", err)
	}
	if row.ScanState != string(models.MailAttachmentScanClean) {
		t.Errorf("scan state = %q, want clean", row.ScanState)
	}
}
