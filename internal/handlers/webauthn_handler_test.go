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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	wa "github.com/goposta/posta/internal/services/webauthn"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/twofactor"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"github.com/pquerna/otp/totp"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The WebAuthn handler (R-19) is served through the real okapi stack with the
// real JWT middleware, and the ceremonies run against a real
// github.com/go-webauthn/webauthn Service. The "browser" is a software
// authenticator: an ECDSA P-256 key that builds spec-conformant attestation
// objects (fmt=none) and assertion signatures over a minimal CBOR encoder, so
// the full begin/finish round trips — challenge binding, origin checks, key
// storage, the 2FA pending marker, and the issued session — are pinned
// end-to-end. The users table rides a fake database/sql driver in the
// established arrangement; credentials live in an in-memory CredentialStore
// and ceremony state in the service's MemoryChallengeStore.

const waTestJWTSecret = "webauthn-handler-test-secret"

const (
	waRPID     = "dashboard.example.com"
	waOrigin   = "https://dashboard.example.com"
	waUserID   = 2
	waUserMail = "user2@example.com"
	waUserJTI  = "jti-wa-session-1"
)

var waTestTime = time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)

// ---------- the software authenticator ----------

// waCborHead writes a CBOR type/major header for values up to 32 bits.
func waCborHead(major uint8, val uint64) []byte {
	head := major << 5
	switch {
	case val < 24:
		return []byte{head | byte(val)}
	case val <= 0xff:
		return []byte{head | 24, byte(val)}
	case val <= 0xffff:
		return []byte{head | 25, byte(val >> 8), byte(val)}
	default:
		return []byte{head | 26, byte(val >> 24), byte(val >> 16), byte(val >> 8), byte(val)}
	}
}

func waCborUint(v uint64) []byte { return waCborHead(0, v) }

func waCborInt(v int64) []byte {
	if v >= 0 {
		return waCborHead(0, uint64(v))
	}
	return waCborHead(1, uint64(-1-v))
}

func waCborBytes(b []byte) []byte { return append(waCborHead(2, uint64(len(b))), b...) }

func waCborText(s string) []byte { return append(waCborHead(3, uint64(len(s))), s...) }

func waCborMap(pairs ...[]byte) []byte {
	out := waCborHead(5, uint64(len(pairs)))
	for _, p := range pairs {
		out = append(out, p...)
	}
	return out
}

// waCoseEC2Key encodes the public key as a COSE EC2 key (ES256/P-256), the
// algorithm the default credential parameters advertise.
func waCoseEC2Key(pub *ecdsa.PublicKey) []byte {
	x := pub.X.FillBytes(make([]byte, 32))
	y := pub.Y.FillBytes(make([]byte, 32))
	return waCborMap(
		append(waCborUint(1), waCborUint(2)...),  // kty: EC2
		append(waCborUint(3), waCborInt(-7)...),  // alg: ES256
		append(waCborInt(-1), waCborUint(1)...),  // crv: P-256
		append(waCborInt(-2), waCborBytes(x)...), // x
		append(waCborInt(-3), waCborBytes(y)...), // y
	)
}

type waTestAuthenticator struct {
	t         *testing.T
	key       *ecdsa.PrivateKey
	credID    []byte
	signCount uint32
}

func newWATestAuthenticator(t *testing.T) *waTestAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate authenticator key: %v", err)
	}
	credID := make([]byte, 32)
	if _, err := rand.Read(credID); err != nil {
		t.Fatalf("generate credential id: %v", err)
	}
	return &waTestAuthenticator{t: t, key: key, credID: credID}
}

func waB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// waClientData builds the clientDataJSON the ceremonies compare byte-exact.
func waClientData(t *testing.T, ceremony, challenge, origin string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"type":      ceremony,
		"challenge": challenge,
		"origin":    origin,
	})
	if err != nil {
		t.Fatalf("marshal client data: %v", err)
	}
	return raw
}

// registrationResponse builds the create() ceremony body for finish
// registration: an fmt=none attestation object carrying the credential.
func (a *waTestAuthenticator) registrationResponse(challenge string) []byte {
	a.t.Helper()
	clientData := waClientData(a.t, "webauthn.create", challenge, waOrigin)
	clientDataHash := sha256.Sum256(clientData)

	rpidHash := sha256.Sum256([]byte(waRPID))
	authData := append([]byte{}, rpidHash[:]...)
	authData = append(authData, 0x41) // UP | AT
	authData = append(authData, 0, 0, 0, 0)
	authData = append(authData, make([]byte, 16)...) // zero AAGUID
	authData = append(authData, byte(len(a.credID)>>8), byte(len(a.credID)))
	authData = append(authData, a.credID...)
	authData = append(authData, waCoseEC2Key(&a.key.PublicKey)...)

	attObj := waCborMap(
		append(waCborText("fmt"), waCborText("none")...),
		append(waCborText("attStmt"), waCborMap()...),
		append(waCborText("authData"), waCborBytes(authData)...),
	)

	body, err := json.Marshal(map[string]any{
		"id":    waB64(a.credID),
		"rawId": waB64(a.credID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    waB64(clientData),
			"attestationObject": waB64(attObj),
			"transports":        []string{"internal"},
		},
		"_clientDataHash": waB64(clientDataHash[:]), // debug aid; ignored by the parser
	})
	if err != nil {
		a.t.Fatalf("marshal registration response: %v", err)
	}
	return body
}

// assertionResponse builds the get() ceremony body for finish login: an
// assertion signature over authData || clientDataHash with the credential key.
func (a *waTestAuthenticator) assertionResponse(challenge string, userHandle []byte) []byte {
	a.t.Helper()
	clientData := waClientData(a.t, "webauthn.get", challenge, waOrigin)
	clientDataHash := sha256.Sum256(clientData)

	a.signCount++
	count := make([]byte, 4)
	binary.BigEndian.PutUint32(count, a.signCount)

	rpidHash := sha256.Sum256([]byte(waRPID))
	authData := append([]byte{}, rpidHash[:]...)
	authData = append(authData, 0x01) // UP
	authData = append(authData, count...)

	sigData := append(append([]byte{}, authData...), clientDataHash[:]...)
	digest := sha256.Sum256(sigData)
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		a.t.Fatalf("sign assertion: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"id":    waB64(a.credID),
		"rawId": waB64(a.credID),
		"type":  "public-key",
		"response": map[string]any{
			"authenticatorData": waB64(authData),
			"clientDataJSON":    waB64(clientData),
			"signature":         waB64(sig),
			"userHandle":        waB64(userHandle),
		},
	})
	if err != nil {
		a.t.Fatalf("marshal assertion response: %v", err)
	}
	return body
}

// waLoginBody wraps a bare assertion in the login finish envelope the handler
// binds (webauthnLoginFinishBody).
func waLoginBody(email string, assertion []byte) []byte {
	return []byte(`{"email":"` + email + `","assertion":` + string(assertion) + `}`)
}

// waSwapClientData rewrites a fragment of the attestation's clientDataJSON:
// base64url-decode, replace, re-encode — so a test can forge a ceremony that
// belongs to no begin call.
func waSwapClientData(t *testing.T, body []byte, old, new string) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode registration body: %v", err)
	}
	resp, _ := m["response"].(map[string]any)
	encoded, _ := resp["clientDataJSON"].(string)
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode clientDataJSON: %v", err)
	}
	if !strings.Contains(string(raw), old) {
		t.Fatalf("clientDataJSON %s does not contain %q", raw, old)
	}
	resp["clientDataJSON"] = waB64([]byte(strings.Replace(string(raw), old, new, 1)))
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("re-encode registration body: %v", err)
	}
	return out
}

// ---------- the fake credential store ----------

type waFakeCreds struct {
	mu    sync.Mutex
	creds map[uint][]*models.WebAuthnCredential
}

func newWAFakeCreds() *waFakeCreds { return &waFakeCreds{creds: map[uint][]*models.WebAuthnCredential{}} }

func (s *waFakeCreds) ListByUser(userID uint) ([]models.WebAuthnCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]models.WebAuthnCredential, 0, len(s.creds[userID]))
	for _, c := range s.creds[userID] {
		out = append(out, *c)
	}
	return out, nil
}

func (s *waFakeCreds) Create(cred *models.WebAuthnCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *cred
	s.creds[cred.UserID] = append(s.creds[cred.UserID], &cp)
	return nil
}

func (s *waFakeCreds) Save(cred *models.WebAuthnCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.creds[cred.UserID] {
		if c.ID == cred.ID {
			*c = *cred
			return nil
		}
	}
	return wa.ErrCredentialNotFound
}

func (s *waFakeCreds) mutate(userID uint, credentialID string, fn func(*models.WebAuthnCredential)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.creds[userID] {
		if c.ID == credentialID {
			fn(c)
			return nil
		}
	}
	return wa.ErrCredentialNotFound
}

func (s *waFakeCreds) Rename(userID uint, credentialID, name string) error {
	return s.mutate(userID, credentialID, func(c *models.WebAuthnCredential) { c.Name = name })
}

func (s *waFakeCreds) Delete(userID uint, credentialID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.creds[userID][:0]
	for _, c := range s.creds[userID] {
		if c.ID == credentialID {
			continue
		}
		kept = append(kept, c)
	}
	if len(kept) == len(s.creds[userID]) {
		return wa.ErrCredentialNotFound
	}
	s.creds[userID] = kept
	return nil
}

func (s *waFakeCreds) all(userID uint) []*models.WebAuthnCredential {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*models.WebAuthnCredential{}, s.creds[userID]...)
}

// ---------- the fake users database ----------

var waHColumns = []string{"id", "name", "email", "role", "active", "password_hash",
	"two_factor_enabled", "two_factor_secret", "created_at", "last_login_at"}

var waHDefaults = map[string]func() driver.Value{
	"name":              func() driver.Value { return "" },
	"role":              func() driver.Value { return "user" },
	"active":            func() driver.Value { return true },
	"password_hash":     func() driver.Value { return "" },
	"two_factor_secret": func() driver.Value { return "" },
	"created_at":        func() driver.Value { return waTestTime },
}

type waHState struct {
	mu   sync.Mutex
	rows [][]driver.Value
	seq  int64
}

func newWaHState() *waHState { return &waHState{seq: 100} }

func (s *waHState) colIndex(col string) int {
	for i, c := range waHColumns {
		if c == col {
			return i
		}
	}
	return -1
}

func (s *waHState) rowMaps() []map[string]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]driver.Value
	for _, row := range s.rows {
		m := make(map[string]driver.Value, len(waHColumns))
		for i, c := range waHColumns {
			m[c] = row[i]
		}
		out = append(out, m)
	}
	return out
}

func waHVal(v any) driver.Value {
	switch t := v.(type) {
	case nil:
		return nil
	case uint:
		return int64(t)
	case int:
		return int64(t)
	case bool, string, time.Time:
		return t
	default:
		panic(fmt.Sprintf("waHVal: unsupported type %T", v))
	}
}

func waHSeed(t *testing.T, s *waHState, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	row := make([]driver.Value, len(waHColumns))
	for i, col := range waHColumns {
		row[i] = nil
		if def, ok := waHDefaults[col]; ok {
			row[i] = def()
		}
	}
	for col, v := range vals {
		idx := -1
		for i, c := range waHColumns {
			if c == col {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatalf("users has no column %s", col)
		}
		row[idx] = waHVal(v)
	}
	if row[s.colIndex("id")] == nil {
		s.seq++
		row[s.colIndex("id")] = s.seq
	}
	s.rows = append(s.rows, row)
}

type waHConn struct{ s *waHState }

func (c *waHConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *waHConn) Close() error                        { return nil }
func (c *waHConn) Begin() (driver.Tx, error)           { return waHTx{}, nil }

type waHTx struct{}

func (waHTx) Commit() error   { return nil }
func (waHTx) Rollback() error { return nil }

type waHResult struct{ rows int64 }

func (r waHResult) LastInsertId() (int64, error) { return 0, nil }
func (r waHResult) RowsAffected() (int64, error) { return r.rows, nil }

type waHRows struct {
	cols []string
	i    int
	rows [][]driver.Value
}

func (r *waHRows) Columns() []string {
	if len(r.cols) > 0 {
		return r.cols
	}
	return waHColumns
}
func (r *waHRows) Close() error { return nil }
func (r *waHRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type waHConnector struct{ state *waHState }

func (c waHConnector) Connect(context.Context) (driver.Conn, error) { return &waHConn{s: c.state}, nil }
func (c waHConnector) Driver() driver.Driver                        { return waHDrv{} }

type waHDrv struct{}

func (waHDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

func waHGorm(t *testing.T, state *waHState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(waHConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open webauthn fake gorm: %v", err)
	}
	return gdb
}

func waHArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("waHState: no arg with ordinal %d", n))
}

// waHWhere parses `col = $n` equality conditions out of the WHERE clause.
func waHWhere(compact, query string, args []driver.NamedValue) ([][2]driver.Value, error) {
	i := strings.Index(compact, " where ")
	if i < 0 {
		return nil, nil
	}
	where := compact[i+len(" where "):]
	for _, cut := range []string{" order by ", " limit ", " for "} {
		if j := strings.Index(where, cut); j >= 0 {
			where = where[:j]
		}
	}
	var out [][2]driver.Value
	for _, cond := range strings.Split(where, " and ") {
		cond = strings.TrimSpace(cond)
		at := strings.Index(cond, " = $")
		if at < 0 {
			return nil, fmt.Errorf("waHState: unsupported condition %q in: %s", cond, query)
		}
		ordinal := 0
		if _, err := fmt.Sscanf(cond[at+4:], "%d", &ordinal); err != nil {
			return nil, fmt.Errorf("waHState: bad bind ordinal in %q", cond)
		}
		col := strings.ReplaceAll(cond[:at], `"`, "")
		col = strings.TrimPrefix(col, "users.")
		out = append(out, [2]driver.Value{col, waHArg(args, ordinal)})
	}
	return out, nil
}

func (c *waHConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	compact := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if strings.Contains(compact, `insert into "events"`) {
		// Audit events ride through the bus; the wa tests never assert their
		// contents, so acknowledge with a minted id for gorm's RETURNING scan.
		return &waHRows{cols: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
	}
	if !strings.Contains(compact, `from "users"`) {
		return nil, fmt.Errorf("waHState: cannot route query: %s", query)
	}
	where, err := waHWhere(compact, query, args)
	if err != nil {
		return nil, err
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows [][]driver.Value
	for _, row := range s.rows {
		matched := true
		for _, cond := range where {
			idx := -1
			for i, col := range waHColumns {
				if col == cond[0].(string) {
					idx = i
				}
			}
			if idx < 0 {
				matched = false
				break
			}
			switch want := cond[1].(type) {
			case int64:
				if v, ok := row[idx].(int64); !ok || v != want {
					matched = false
				}
			case string:
				if v, ok := row[idx].(string); !ok || v != want {
					matched = false
				}
			default:
				matched = row[idx] == cond[1]
			}
			if !matched {
				break
			}
		}
		if matched {
			rows = append(rows, row)
		}
	}
	return &waHRows{rows: rows}, nil
}

func (c *waHConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if strings.Contains(lower, `insert into "events"`) {
		// Audit events are persisted through the bus; the wa tests never assert
		// their contents, so acknowledge the insert without storing it.
		return waHResult{rows: 1}, nil
	}
	if !strings.Contains(lower, `update "users"`) {
		return nil, fmt.Errorf("waHState: unsupported exec: %s", query)
	}
	where, err := waHWhere(lower, query, args)
	if err != nil {
		return nil, err
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	set := lower[strings.Index(lower, " set ")+len(" set "):]
	set = set[:strings.Index(set, " where ")]
	var setters [][2]driver.Value
	for _, assignment := range strings.Split(set, ",") {
		assignment = strings.TrimSpace(assignment)
		at := strings.Index(assignment, "=$")
		if at < 0 {
			return nil, fmt.Errorf("waHState: unsupported set %q in: %s", assignment, query)
		}
		ordinal := 0
		if _, err := fmt.Sscanf(assignment[at+2:], "%d", &ordinal); err != nil {
			return nil, fmt.Errorf("waHState: bad set bind in %q", assignment)
		}
		col := strings.ReplaceAll(assignment[:at], `"`, "")
		setters = append(setters, [2]driver.Value{col, waHArg(args, ordinal)})
	}
	matched := int64(0)
	for _, row := range s.rows {
		ok := true
		for _, cond := range where {
			idx := -1
			for i, col := range waHColumns {
				if col == cond[0].(string) {
					idx = i
				}
			}
			if idx < 0 {
				ok = false
				break
			}
			if want, isInt := cond[1].(int64); isInt {
				if v, isRowInt := row[idx].(int64); !isRowInt || v != want {
					ok = false
				}
			} else if row[idx] != cond[1] {
				ok = false
			}
			if !ok {
				break
			}
		}
		if !ok {
			continue
		}
		matched++
		for _, st := range setters {
			idx := -1
			for i, col := range waHColumns {
				if col == st[0].(string) {
					idx = i
				}
			}
			if idx < 0 {
				return nil, fmt.Errorf("waHState: update names unknown column %q", st[0])
			}
			row[idx] = st[1]
		}
	}
	return waHResult{rows: matched}, nil
}

// ---------- harness ----------

type waHarness struct {
	state *waHState
	creds *waFakeCreds
	svc   *wa.Service
	o     *okapi.TestServer
}

func newWaHarness(t *testing.T, disabled bool) *waHarness {
	t.Helper()
	state := newWaHState()
	gdb := waHGorm(t, state)
	userRepo := repositories.NewUserRepository(gdb)

	users := NewUserHandler(userRepo, waTestJWTSecret, nil, nil)
	creds := newWAFakeCreds()

	var svc *wa.Service
	if !disabled {
		var err error
		svc, err = wa.New(wa.Config{
			RPID:          waRPID,
			RPDisplayName: "Posta",
			RPOrigins:     []string{waOrigin},
		}, creds, wa.NewMemoryChallengeStore())
		if err != nil {
			t.Fatalf("build webauthn service: %v", err)
		}
	}

	h := NewWebAuthnHandler(svc, userRepo, users)
	// Audit events go through a live bus backed by the fake database; the wa
	// fake stores event inserts without assertions, which still exercises the
	// non-nil audit path the handler guards on.
	h.SetAuditLogger(audit.NewLogger(eventbus.New(repositories.NewEventRepository(gdb))))

	o := okapi.NewTestServer(t)
	jwtCfg := &config.Config{JWTSecret: waTestJWTSecret}
	jwtAuth := middlewares.JWTAuth(jwtCfg)
	authed := o.Group("/auth/webauthn", jwtAuth.Middleware)
	authed.Post("/register/begin", h.BeginRegistration)
	authed.Post("/register/finish", h.FinishRegistration)
	authed.Get("/credentials", h.ListCredentials)
	authed.Put("/credentials/{id}", okapi.H(h.RenameCredential))
	authed.Delete("/credentials/{id}", okapi.H(h.DeleteCredential))

	public := o.Group("/auth/webauthn")
	public.Post("/login/begin", okapi.H(h.BeginLogin))
	public.Post("/login/finish", h.FinishLogin)

	return &waHarness{state: state, creds: creds, svc: svc, o: o}
}

func (h *waHarness) do(t *testing.T, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.o.ServeHTTP(rec, req)
	return rec
}

// waUserToken mints the dashboard session token for the seeded user.
func waUserToken(t *testing.T) string {
	t.Helper()
	tok, err := okapi.GenerateJwtToken([]byte(waTestJWTSecret), map[string]any{
		"sub": waUserID, "email": waUserMail, "role": "user", "aud": "posta", "jti": waUserJTI,
	}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateJwtToken: %v", err)
	}
	return tok
}

func waSeedUser(t *testing.T, h *waHarness, twoFactor bool) string {
	t.Helper()
	secret := ""
	if twoFactor {
		generated, _, err := twofactor.GenerateSecret(waUserMail)
		if err != nil {
			t.Fatalf("generate TOTP secret: %v", err)
		}
		secret = generated
	}
	waHSeed(t, h.state, map[string]any{
		"id": waUserID, "name": "Passkey User", "email": waUserMail, "role": "user",
		"active": true, "password_hash": "x",
		"two_factor_enabled": twoFactor, "two_factor_secret": secret,
		"created_at": waTestTime,
	})
	return secret
}

// totp_GenerateCode mints a valid TOTP code for secret at time t, from the same
// library twofactor.ValidateCode verifies against.
func totp_GenerateCode(secret string, t time.Time) (string, error) {
	return totp.GenerateCode(secret, t)
}

// waRegister performs the registration ceremony against the HTTP handlers and
// returns the stored credential row.
func (h *waHarness) waRegister(t *testing.T, auth *waTestAuthenticator) *models.WebAuthnCredential {
	t.Helper()
	token := waUserToken(t)

	begin := h.do(t, http.MethodPost, "/auth/webauthn/register/begin", token, []byte("{}"))
	if begin.Code != http.StatusOK {
		t.Fatalf("register begin status = %d, want 200 (body %s)", begin.Code, begin.Body.String())
	}
	challenge := waDataMap(t, begin)["challenge"].(string)

	finish := h.do(t, http.MethodPost, "/auth/webauthn/register/finish", token, auth.registrationResponse(challenge))
	if finish.Code != http.StatusCreated {
		t.Fatalf("register finish status = %d, want 201 (body %s)", finish.Code, finish.Body.String())
	}
	data := waDataMap(t, finish)
	if data["id"] != waB64(auth.credID) {
		t.Fatalf("credential id = %v, want %s", data["id"], waB64(auth.credID))
	}
	if data["attestation_type"] != "none" {
		t.Fatalf("attestation_type = %v, want none", data["attestation_type"])
	}
	creds := h.creds.all(waUserID)
	if len(creds) != 1 {
		t.Fatalf("stored credentials = %d, want 1", len(creds))
	}
	return creds[0]
}

func waDataMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if resp.Error != nil {
		return map[string]any{"_error": resp.Error.Message}
	}
	return resp.Data
}

// waUserHandle is the 8-byte big-endian user handle the service advertises.
func waUserHandle() []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, waUserID)
	return buf
}

// ---------- disabled feature ----------

func TestWebAuthnDisabledAnswersServiceUnavailable(t *testing.T) {
	h := newWaHarness(t, true)
	token := waUserToken(t)

	for _, tc := range []struct {
		method, path string
		body         []byte
	}{
		{http.MethodPost, "/auth/webauthn/register/begin", []byte("{}")},
		{http.MethodPost, "/auth/webauthn/register/finish", []byte("{}")},
		{http.MethodGet, "/auth/webauthn/credentials", nil},
		{http.MethodPost, "/auth/webauthn/login/begin", []byte(`{"email":"` + waUserMail + `"}`)},
		{http.MethodPost, "/auth/webauthn/login/finish", []byte(`{"email":"` + waUserMail + `"}`)},
	} {
		rec := h.do(t, tc.method, tc.path, token, tc.body)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s status = %d, want 503 (body %s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "passkeys are not configured") {
			t.Fatalf("%s %s body %q missing the disabled message", tc.method, tc.path, rec.Body.String())
		}
	}
}

// ---------- registration ----------

func TestWebAuthnRegistrationCeremony(t *testing.T) {
	h := newWaHarness(t, false)
	waSeedUser(t, h, false)
	auth := newWATestAuthenticator(t)
	token := waUserToken(t)

	// Begin: the creation options scope the ceremony to the RPID and user handle.
	begin := h.do(t, http.MethodPost, "/auth/webauthn/register/begin", token, []byte("{}"))
	if begin.Code != http.StatusOK {
		t.Fatalf("begin status = %d, want 200 (body %s)", begin.Code, begin.Body.String())
	}
	data := waDataMap(t, begin)
	if data["challenge"] == "" {
		t.Fatalf("creation options carry no challenge: %v", data)
	}
	rp, _ := data["rp"].(map[string]any)
	if rp["id"] != waRPID {
		t.Fatalf("rp.id = %v, want %s", rp["id"], waRPID)
	}
	user, _ := data["user"].(map[string]any)
	handle, err := base64.RawURLEncoding.DecodeString(user["id"].(string))
	if err != nil {
		t.Fatalf("user.id is not base64url: %v", err)
	}
	if binary.BigEndian.Uint64(handle) != waUserID {
		t.Fatalf("user.id = %v, want the user's handle", user["id"])
	}

	// Finish: the attestation verifies and the credential is stored.
	finish := h.do(t, http.MethodPost, "/auth/webauthn/register/finish", token, auth.registrationResponse(data["challenge"].(string)))
	if finish.Code != http.StatusCreated {
		t.Fatalf("finish status = %d, want 201 (body %s)", finish.Code, finish.Body.String())
	}
	stored := h.creds.all(waUserID)
	if len(stored) != 1 {
		t.Fatalf("stored credentials = %d, want 1", len(stored))
	}
	if stored[0].ID != waB64(auth.credID) {
		t.Fatalf("stored id = %s, want %s", stored[0].ID, waB64(auth.credID))
	}
	if len(stored[0].PublicKey) == 0 {
		t.Fatal("stored credential has no public key")
	}

	// The credential shows up in the management list.
	list := h.do(t, http.MethodGet, "/auth/webauthn/credentials", token, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", list.Code)
	}
	var listResp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Data) != 1 || listResp.Data[0]["id"] != waB64(auth.credID) {
		t.Fatalf("credential list = %v, want the registered credential", listResp.Data)
	}

	// The challenge is single-use: replaying the finish fails.
	replay := h.do(t, http.MethodPost, "/auth/webauthn/register/finish", token, auth.registrationResponse(data["challenge"].(string)))
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("replayed finish status = %d, want 400 (body %s)", replay.Code, replay.Body.String())
	}
	if !strings.Contains(replay.Body.String(), "expired or already used") {
		t.Fatalf("replay body %q missing the single-use message", replay.Body.String())
	}
	// And the refusal stored nothing.
	if n := len(h.creds.all(waUserID)); n != 1 {
		t.Fatalf("stored credentials after replay = %d, want 1", n)
	}
}

func TestWebAuthnRegistrationRejectsForeignCeremony(t *testing.T) {
	h := newWaHarness(t, false)
	waSeedUser(t, h, false)
	auth := newWATestAuthenticator(t)
	token := waUserToken(t)

	begin := h.do(t, http.MethodPost, "/auth/webauthn/register/begin", token, []byte("{}"))
	challenge := waDataMap(t, begin)["challenge"].(string)

	// A ceremony begun for a different challenge must not verify: forge a
	// registration response whose clientData carries another ceremony's
	// challenge.
	body := waSwapClientData(t, auth.registrationResponse(challenge), challenge, "a-different-ceremonys-challenge")
	finish := h.do(t, http.MethodPost, "/auth/webauthn/register/finish", token, []byte(body))
	if finish.Code != http.StatusBadRequest {
		t.Fatalf("cross-ceremony finish status = %d, want 400 (body %s)", finish.Code, finish.Body.String())
	}
	if n := len(h.creds.all(waUserID)); n != 0 {
		t.Fatalf("stored credentials = %d, want 0", n)
	}
}

// ---------- login ----------

func TestWebAuthnLoginBeginRejectsUnknownAndCredlessAccounts(t *testing.T) {
	h := newWaHarness(t, false)
	waSeedUser(t, h, false)

	rec := h.do(t, http.MethodPost, "/auth/webauthn/login/begin", "", []byte(`{"email":"nobody@example.com"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown email status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "passkey sign-in is not available") {
		t.Fatalf("unknown email body %q is not the generic refusal", rec.Body.String())
	}

	// The known account without credentials gets the same generic refusal, so
	// the endpoint cannot be used to enumerate accounts.
	rec = h.do(t, http.MethodPost, "/auth/webauthn/login/begin", "", []byte(`{"email":"`+waUserMail+`"}`))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "passkey sign-in is not available") {
		t.Fatalf("credless account status = %d body %s, want the generic refusal", rec.Code, rec.Body.String())
	}

	rec = h.do(t, http.MethodPost, "/auth/webauthn/login/begin", "", []byte(`{}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing email status = %d, want 400", rec.Code)
	}
}

func TestWebAuthnLoginCeremony(t *testing.T) {
	h := newWaHarness(t, false)
	waSeedUser(t, h, false)
	auth := newWATestAuthenticator(t)
	h.waRegister(t, auth)

	// Begin login: assertion options scoped to the registered credential.
	begin := h.do(t, http.MethodPost, "/auth/webauthn/login/begin", "", []byte(`{"email":"`+waUserMail+`"}`))
	if begin.Code != http.StatusOK {
		t.Fatalf("login begin status = %d, want 200 (body %s)", begin.Code, begin.Body.String())
	}
	data := waDataMap(t, begin)
	challenge, _ := data["challenge"].(string)
	if challenge == "" {
		t.Fatalf("assertion options carry no challenge: %v", data)
	}
	allowed, _ := data["allowCredentials"].([]any)
	if len(allowed) != 1 {
		t.Fatalf("allowCredentials = %v, want the single registered credential", data["allowCredentials"])
	}
	if entry, _ := allowed[0].(map[string]any); entry["id"] != waB64(auth.credID) {
		t.Fatalf("allowCredentials[0].id = %v, want %s", entry["id"], waB64(auth.credID))
	}

	// Finish login: the assertion verifies and a session is issued like a
	// password login.
	finish := h.do(t, http.MethodPost, "/auth/webauthn/login/finish", "", waLoginBody(waUserMail, auth.assertionResponse(challenge, waUserHandle())))
	if finish.Code != http.StatusOK {
		t.Fatalf("login finish status = %d, want 200 (body %s)", finish.Code, finish.Body.String())
	}
	loginData := waDataMap(t, finish)
	if loginData["token"] == "" {
		t.Fatalf("login response carries no token: %v", loginData)
	}
	userOut, _ := loginData["user"].(map[string]any)
	if userOut["email"] != waUserMail {
		t.Fatalf("login user = %v, want %s", userOut, waUserMail)
	}
	cookies := finish.Result().Cookies()
	cookieSet := false
	for _, ck := range cookies {
		if ck.Name == middlewares.SessionCookieName && ck.Value == loginData["token"].(string) {
			cookieSet = true
		}
	}
	if !cookieSet {
		t.Fatalf("login did not set the %s cookie (cookies %+v)", middlewares.SessionCookieName, cookies)
	}

	// The authenticator's counter advance and last-used stamp are persisted.
	stored := h.creds.all(waUserID)
	if len(stored) != 1 || stored[0].SignCount == 0 {
		t.Fatalf("stored credential after login = %+v, want the counter advanced", stored)
	}
	if stored[0].LastUsedAt == nil {
		t.Fatal("stored credential after login has no last-used stamp")
	}

	// The challenge is consumed: replaying the assertion fails.
	replay := h.do(t, http.MethodPost, "/auth/webauthn/login/finish", "", waLoginBody(waUserMail, auth.assertionResponse(challenge, waUserHandle())))
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed assertion status = %d, want 401 (body %s)", replay.Code, replay.Body.String())
	}

	// A fresh ceremony with a tampered signature fails and issues no session.
	begin2 := h.do(t, http.MethodPost, "/auth/webauthn/login/begin", "", []byte(`{"email":"`+waUserMail+`"}`))
	challenge2 := waDataMap(t, begin2)["challenge"].(string)
	tampered := strings.Replace(string(auth.assertionResponse(challenge2, waUserHandle())), `"signature":"`, `"signature":"AAAA`, 1)
	bad := h.do(t, http.MethodPost, "/auth/webauthn/login/finish", "", waLoginBody(waUserMail, []byte(tampered)))
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("tampered assertion status = %d, want 401 (body %s)", bad.Code, bad.Body.String())
	}
}

func TestWebAuthnLoginFinishInputValidation(t *testing.T) {
	h := newWaHarness(t, false)
	waSeedUser(t, h, false)
	auth := newWATestAuthenticator(t)
	h.waRegister(t, auth)

	cases := []struct {
		name string
		body string
	}{
		{"missing email", `{"assertion":{"id":"x"}}`},
		{"missing assertion", `{"email":"` + waUserMail + `"}`},
		{"garbage assertion", `{"email":"` + waUserMail + `","assertion":{"nope":true}}`},
		{"unknown email", `{"email":"nobody@example.com","assertion":{"id":"x"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(t, http.MethodPost, "/auth/webauthn/login/finish", "", []byte(tc.body))
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 400/401 (body %s)", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), `"token"`) {
				t.Fatalf("refused login issued a token: %s", rec.Body.String())
			}
		})
	}
}

func TestWebAuthnLoginSecondFactor(t *testing.T) {
	h := newWaHarness(t, false)
	secret := waSeedUser(t, h, true)
	auth := newWATestAuthenticator(t)
	h.waRegister(t, auth)

	// The passkey verifies, but 2FA still applies: the finish answers the
	// requires_2fa 401 with a one-shot pending token.
	begin := h.do(t, http.MethodPost, "/auth/webauthn/login/begin", "", []byte(`{"email":"`+waUserMail+`"}`))
	challenge := waDataMap(t, begin)["challenge"].(string)
	first := h.do(t, http.MethodPost, "/auth/webauthn/login/finish", "", waLoginBody(waUserMail, auth.assertionResponse(challenge, waUserHandle())))
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("2FA login status = %d, want 401 (body %s)", first.Code, first.Body.String())
	}
	data := waDataMap(t, first)
	if data["requires_2fa"] != true || data["passkey_verified"] != true {
		t.Fatalf("2FA response = %v, want requires_2fa + passkey_verified", data)
	}
	pending, _ := data["pending_token"].(string)
	if pending == "" {
		t.Fatalf("2FA response carries no pending token: %v", data)
	}

	code, err := totp_GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}
	payload, _ := json.Marshal(map[string]string{
		"email":           waUserMail,
		"pending_token":   pending,
		"two_factor_code": code,
	})
	second := h.do(t, http.MethodPost, "/auth/webauthn/login/finish", "", payload)
	if second.Code != http.StatusOK {
		t.Fatalf("second-factor status = %d, want 200 (body %s)", second.Code, second.Body.String())
	}
	final := waDataMap(t, second)
	if final["token"] == "" {
		t.Fatalf("second-factor response carries no token: %v", final)
	}

	// The pending token was one-shot: redeeming it again fails even with a
	// fresh valid code.
	code2, _ := totp_GenerateCode(secret, time.Now())
	replayPayload, _ := json.Marshal(map[string]string{
		"email":           waUserMail,
		"pending_token":   pending,
		"two_factor_code": code2,
	})
	replay := h.do(t, http.MethodPost, "/auth/webauthn/login/finish", "", replayPayload)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed pending token status = %d, want 401 (body %s)", replay.Code, replay.Body.String())
	}
}

func TestWebAuthnLoginSecondFactorRejectsBadCodeAndTokens(t *testing.T) {
	h := newWaHarness(t, false)
	waSeedUser(t, h, true)
	auth := newWATestAuthenticator(t)
	h.waRegister(t, auth)

	begin := h.do(t, http.MethodPost, "/auth/webauthn/login/begin", "", []byte(`{"email":"`+waUserMail+`"}`))
	challenge := waDataMap(t, begin)["challenge"].(string)
	first := h.do(t, http.MethodPost, "/auth/webauthn/login/finish", "", waLoginBody(waUserMail, auth.assertionResponse(challenge, waUserHandle())))
	pending := waDataMap(t, first)["pending_token"].(string)

	// Unknown pending token.
	badToken, _ := json.Marshal(map[string]string{"email": waUserMail, "pending_token": "bogus", "two_factor_code": "123456"})
	rec := h.do(t, http.MethodPost, "/auth/webauthn/login/finish", "", badToken)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bogus pending token status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}

	// Wrong TOTP code.
	badCode, _ := json.Marshal(map[string]string{"email": waUserMail, "pending_token": pending, "two_factor_code": "000000"})
	rec = h.do(t, http.MethodPost, "/auth/webauthn/login/finish", "", badCode)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad TOTP code status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	// And no session was issued.
	if strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatalf("failed second factor issued a token: %s", rec.Body.String())
	}
}

// ---------- credential management ----------

func TestWebAuthnCredentialManagement(t *testing.T) {
	h := newWaHarness(t, false)
	waSeedUser(t, h, false)
	auth := newWATestAuthenticator(t)
	stored := h.waRegister(t, auth)
	token := waUserToken(t)
	credPath := "/auth/webauthn/credentials/" + stored.ID

	// Rename the credential.
	rename := h.do(t, http.MethodPut, credPath, token, []byte(`{"name":"MacBook Touch ID"}`))
	if rename.Code != http.StatusOK {
		t.Fatalf("rename status = %d, want 200 (body %s)", rename.Code, rename.Body.String())
	}
	if h.creds.all(waUserID)[0].Name != "MacBook Touch ID" {
		t.Fatalf("credential name = %q, want the new label", h.creds.all(waUserID)[0].Name)
	}

	// Rename an unknown credential.
	missing := h.do(t, http.MethodPut, "/auth/webauthn/credentials/unknown-cred", token, []byte(`{"name":"X"}`))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("rename unknown status = %d, want 404", missing.Code)
	}

	// Delete the credential.
	del := h.do(t, http.MethodDelete, credPath, token, nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204 (body %s)", del.Code, del.Body.String())
	}
	if n := len(h.creds.all(waUserID)); n != 0 {
		t.Fatalf("credentials after delete = %d, want 0", n)
	}

	// Deleting it again is a 404, and the gone credential cannot log in.
	again := h.do(t, http.MethodDelete, credPath, token, nil)
	if again.Code != http.StatusNotFound {
		t.Fatalf("delete again status = %d, want 404", again.Code)
	}
	begin := h.do(t, http.MethodPost, "/auth/webauthn/login/begin", "", []byte(`{"email":"`+waUserMail+`"}`))
	if begin.Code != http.StatusBadRequest {
		t.Fatalf("login begin after delete status = %d, want 400", begin.Code)
	}
}
