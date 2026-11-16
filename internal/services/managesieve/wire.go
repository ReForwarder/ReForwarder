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
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package managesieve

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/filterengine"
)

// maxCommandLine bounds one client command line (script names, capability
// words and AUTH PLAIN initial responses all live here).
const maxCommandLine = 4096

// maxScriptBytes bounds one PUTSCRIPT / CHECKSCRIPT literal — one script
// document's whole body. A document beyond the bound is refused before any
// storage write.
const maxScriptBytes = 256 * 1024

// errLineTooLong reports a client command line beyond maxCommandLine.
var errLineTooLong = errors.New("managesieve: command line too long")

// errScriptTooLarge reports a script literal beyond maxScriptBytes.
var errScriptTooLarge = errors.New("managesieve: script literal exceeds the size bound")

// readCommandLine reads one CRLF-terminated command line, stripping the
// terminator. An overlong line is drained to its end (so the session cannot
// desync mid-line) and reported as errLineTooLong — the POP3 listener's
// reader.
func readCommandLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		// Drain the remainder of the oversized line before failing, so a
		// follow-up command read cannot resume inside the junk.
		for {
			_, derr := r.ReadSlice('\n')
			if derr == io.EOF {
				break
			}
			if derr == nil {
				break
			}
			if !errors.Is(derr, bufio.ErrBufferFull) {
				return nil, derr
			}
		}
		return nil, errLineTooLong
	}
	if err != nil {
		return nil, err
	}
	return trimCRLF(line), nil
}

// readLiteral reads an RFC 5804 literal body: exactly n octets after the
// command line, followed by CRLF. `n` is bounded by maxScriptBytes. The
// reader must be positioned at the first literal octet (the command-line
// read consumed the `{n}` line's CRLF).
func readLiteral(r *bufio.Reader, n int) ([]byte, error) {
	if n < 0 || n > maxScriptBytes {
		return nil, errScriptTooLarge
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("managesieve: literal read: %w", err)
	}
	term := make([]byte, 2)
	if _, err := io.ReadFull(r, term); err != nil {
		return nil, fmt.Errorf("managesieve: literal terminator read: %w", err)
	}
	if term[0] != '\r' || term[1] != '\n' {
		return nil, errors.New("managesieve: literal is not CRLF-terminated")
	}
	return body, nil
}

// trimCRLF strips a trailing CR and/or LF from one received line.
func trimCRLF(line []byte) []byte {
	n := len(line)
	for n > 0 && (line[n-1] == '\n' || line[n-1] == '\r') {
		n--
	}
	return line[:n]
}

// splitLine breaks a command line into its verb (upper-cased) and raw
// argument ("" when absent). Verbs are case-insensitive per RFC 5804 §1.1.
func splitLine(line []byte) (cmd string, arg string) {
	text := trimLeftSpaces(string(line))
	end := -1
	for i := 0; i < len(text); i++ {
		if text[i] == ' ' || text[i] == '\t' {
			end = i
			break
		}
	}
	if end < 0 {
		return upperASCII(text), ""
	}
	return upperASCII(text[:end]), trimLeftSpaces(text[end+1:])
}

// sieveArg is one parsed command argument: a sieve string (quoted, literal,
// or a bare atom — clients that send unquoted names are tolerated for the
// name-taking commands) or a bare number (HAVESPACE's size).
type sieveArg struct {
	str      string
	number   int64
	isNumber bool
}

// parseArgs splits a raw argument string into sieve arguments. A `{n}` /
// `{n+}` token (a literal) consumes its body from the session reader — the
// RFC 5804 client-side continuation for PUTSCRIPT / CHECKSCRIPT bodies.
// Unknown syntax is an error (the caller answers NO).
func (s *session) parseArgs(arg string) ([]sieveArg, error) {
	var out []sieveArg
	for {
		arg = trimLeftSpaces(arg)
		if arg == "" {
			return out, nil
		}
		switch {
		case arg[0] == '"':
			val, rest, err := unquoteSieveString(arg)
			if err != nil {
				return nil, err
			}
			out = append(out, sieveArg{str: val})
			arg = rest
		case arg[0] == '{':
			end := strings.IndexByte(arg, '}')
			if end < 0 {
				return nil, errors.New("managesieve: unterminated literal marker")
			}
			marker := arg[1:end]
			marker = strings.TrimSuffix(marker, "+")
			var n int
			if _, err := fmt.Sscanf(marker, "%d", &n); err != nil {
				return nil, errors.New("managesieve: malformed literal marker")
			}
			body, lerr := readLiteral(s.reader, n)
			if lerr != nil {
				return nil, lerr
			}
			// The literal body replaces the rest of the argument string —
			// nothing follows it on the logical command line.
			out = append(out, sieveArg{str: string(body)})
			return out, nil
		default:
			end := -1
			for i := 0; i < len(arg); i++ {
				if arg[i] == ' ' || arg[i] == '\t' {
					end = i
					break
				}
			}
			token := arg
			if end >= 0 {
				token = arg[:end]
				arg = arg[end:]
			} else {
				arg = ""
			}
			if n, ok := parseSieveNumber(token); ok {
				out = append(out, sieveArg{number: n, isNumber: true})
			} else {
				out = append(out, sieveArg{str: token})
			}
		}
	}
}

// argString returns argument i as a sieve string (quoted, literal, or bare
// atom). A number argument converts to its decimal form.
func argString(args []sieveArg, i int) (string, bool) {
	if i >= len(args) {
		return "", false
	}
	a := args[i]
	if a.isNumber {
		return fmt.Sprintf("%d", a.number), true
	}
	return a.str, true
}

// argNumber returns argument i as a number.
func argNumber(args []sieveArg, i int) (int64, bool) {
	if i >= len(args) {
		return 0, false
	}
	a := args[i]
	if a.isNumber {
		return a.number, true
	}
	return 0, false
}

// parseSieveNumber parses a bare decimal atom as a number.
func parseSieveNumber(token string) (int64, bool) {
	if token == "" {
		return 0, false
	}
	var n int64
	for i := 0; i < len(token); i++ {
		c := token[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		if n > (1<<62)/10 {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}

// unquoteSieveString decodes one quoted sieve string from the head of arg
// (RFC 5804 §1.2: `\"` and `\\` are the only escapes), returning the value
// and the remainder after the closing quote.
func unquoteSieveString(arg string) (string, string, error) {
	if len(arg) == 0 || arg[0] != '"' {
		return "", "", errors.New("managesieve: expected a quoted string")
	}
	var b strings.Builder
	i := 1
	for ; i < len(arg); i++ {
		c := arg[i]
		switch c {
		case '"':
			return b.String(), arg[i+1:], nil
		case '\\':
			if i+1 >= len(arg) || (arg[i+1] != '"' && arg[i+1] != '\\') {
				return "", "", errors.New("managesieve: invalid escape in quoted string")
			}
			b.WriteByte(arg[i+1])
			i++
		default:
			b.WriteByte(c)
		}
	}
	return "", "", errors.New("managesieve: unterminated quoted string")
}

// quoteSieveString encodes one sieve string for the wire (the server-side
// quoted-string form; all server text answers go through this so a value can
// never break out of the quotes — no bare CR/LF or quotes can be injected).
func quoteSieveString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", " ", "\n", " ")
	return `"` + r.Replace(s) + `"`
}

// trimLeftSpaces strips leading spaces/tabs.
func trimLeftSpaces(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	return s
}

// upperASCII upper-cases a command verb (RFC 5804 verbs are ASCII; bytes
// >= 0x80 pass through untouched).
func upperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}

// scriptDocument is the serialized filter-script document PUTSCRIPT /
// CHECKSCRIPT carry: the SF-14 engine's own shape, not RFC 5228 text.
type scriptDocument struct {
	Conditions []models.FilterRule         `json:"conditions"`
	Actions    []filterengine.ScriptAction `json:"actions"`
}

// parseScriptDocument decodes the serialized document and runs it through
// the SAME strict evaluator the delivery engine runs (filterengine.
// ValidateScript — the fail-closed grammar: an unknown field/operator or an
// unknown action type is refused here, before any storage write).
func parseScriptDocument(body string) ([]models.FilterRule, []filterengine.ScriptAction, error) {
	var doc scriptDocument
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, fmt.Errorf("the script document must be {\"conditions\": [...], \"actions\": [...]}: %v", err)
	}
	// Trailing content after the JSON object is refused (a document is one
	// object — not a stream).
	if dec.More() {
		return nil, nil, errors.New("the script document must be a single JSON object")
	}
	if err := filterengine.ValidateScript(doc.Conditions, doc.Actions); err != nil {
		return nil, nil, err
	}
	return doc.Conditions, doc.Actions, nil
}

// readDeadline is the session idle deadline (a connection that sends nothing
// for the idle timeout is reaped).
func (s *session) readDeadline() time.Time {
	return time.Now().Add(s.srv.cfg.IdleTimeout)
}
