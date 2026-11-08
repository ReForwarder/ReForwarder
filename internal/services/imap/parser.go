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

package imap

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Wire-tokenizer for the IMAP command grammar the server accepts (RFC 9051
// §4.3 subset, ADR-004 command list): atoms (incl. BODY[...] section specs
// folded into one token, and <partial> suffixes), quoted strings, literals
// ({n} synchronizing and {n+} non-synchronizing), and parenthesized lists
// surfaced as open/close tokens.
//
// Literal handling (RFC 9051 §4.3): when a line ends with {n} the server
// sends the "+ go ahead" continuation, reads exactly n octets, and continues
// the command on the following line. {n+} skips the continuation request
// (LITERAL+ on the wire; accepted although not advertised — documented).

const (
	maxLineBytes = 8192 // a command line without its literals stays below this
)

// errProtocol marks a malformed command; the session answers with BAD.
var errProtocol = errors.New("imap: protocol error")

// literalTooLargeError is returned when a command declares a literal above
// the configured cap. No continuation request is sent (the client never
// transmits the octets), and the session answers the command's tag with
// `NO [TOOBIG]` and stays in sync — the client's next line is its next
// command (RFC 9051 §4.3 literal-rejection flow).
type literalTooLargeError struct {
	size int64
	tag  string // filled from the tokens parsed before the literal
}

func (e *literalTooLargeError) Error() string {
	return fmt.Sprintf("imap: literal of %d octets exceeds the cap", e.size)
}

// token is one parsed element of a command.
type token struct {
	// kind: 'a' atom (BODY[...] sections stay inside the atom text),
	// 's' quoted string or literal (Val is the decoded value), '(' , ')'.
	Kind byte
	Val  string
}

// commandTokens carries one command's tokens: the tag, the verb, and the
// argument list.
type commandTokens struct {
	Tag   string
	Verb  string // uppercased
	Args  []token
	lineN int // lines consumed (diagnostics)
}

// lineReader reads CRLF lines plus literals from the wire, issuing the
// synchronizing continuation when a literal requires it.
type lineReader struct {
	br  *bufio.Reader
	out io.Writer // continuation requests go here ("+ ...\r\n")

	maxLiteral int64
}

func (r *lineReader) readLine() (string, error) {
	var b strings.Builder
	for {
		chunk, err := r.br.ReadSlice('\n')
		b.Write(chunk)
		if err == bufio.ErrBufferFull {
			if b.Len() > maxLineBytes {
				return "", fmt.Errorf("%w: line too long", errProtocol)
			}
			continue
		}
		if err != nil {
			return "", err
		}
		break
	}
	s := b.String()
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")
	return s, nil
}

// continuationBytes writes the synchronizing continuation request.
func (r *lineReader) continuation() error {
	_, err := io.WriteString(r.out, "+ Ready for literal data\r\n")
	return err
}

// readLiteral reads exactly n octets after issuing the continuation (unless
// nonSync). A size above the cap errors without sending the continuation.
func (r *lineReader) readLiteral(n int64, nonSync bool) ([]byte, error) {
	if n < 0 || n > r.maxLiteral {
		return nil, &literalTooLargeError{size: n}
	}
	if !nonSync {
		if err := r.continuation(); err != nil {
			return nil, err
		}
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r.br, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// tokenize parses one full command (lines + literals) into tokens.
func (r *lineReader) tokenize() (*commandTokens, error) {
	cmd := &commandTokens{}
	line, err := r.readLine()
	if err != nil {
		return nil, err
	}
	cmd.lineN = 1
	var pending []token
	for {
		rest, toks, litAtEnd, err := scanLine(line, r)
		if err != nil {
			// An oversized literal keeps the tag parsed so far: the session
			// can answer tagged NO and resynchronize on the next line.
			var le *literalTooLargeError
			if errors.As(err, &le) && len(toks) >= 1 {
				le.tag = toks[0].Val
				return nil, le
			}
			return nil, err
		}
		pending = append(pending, toks...)
		if !litAtEnd {
			_ = rest
			break
		}
		// The literal consumed the end of the line; the command continues
		// after the literal octets on the next line.
		line, err = r.readLine()
		if err != nil {
			return nil, err
		}
		cmd.lineN++
		if cmd.lineN > 64 {
			return nil, fmt.Errorf("%w: too many continuation lines", errProtocol)
		}
	}
	if len(pending) < 2 {
		return nil, fmt.Errorf("%w: missing tag or command", errProtocol)
	}
	cmd.Tag = pending[0].Val
	verb := pending[1].Val
	cmd.Verb = strings.ToUpper(verb)
	cmd.Args = pending[2:]
	return cmd, nil
}

// scanLine scans one physical line into tokens. When the line ends with a
// literal marker the literal is read from the wire and appended as a token,
// and litAtEnd=true tells the caller to continue on the next line.
func scanLine(line string, r *lineReader) (rest string, toks []token, litAtEnd bool, err error) {
	i := 0
	for i < len(line) {
		c := line[i]
		switch {
		case c == ' ':
			i++
		case c == '(':
			toks = append(toks, token{Kind: '('})
			i++
		case c == ')':
			toks = append(toks, token{Kind: ')'})
			i++
		case c == '"':
			s, ni, perr := scanQuoted(line, i)
			if perr != nil {
				return "", nil, false, perr
			}
			toks = append(toks, token{Kind: 's', Val: s})
			i = ni
		case c == '{':
			n, nonSync, ni, perr := scanLiteralMarker(line, i)
			if perr != nil {
				return "", toks, false, perr
			}
			if ni != len(line) {
				return "", toks, false, fmt.Errorf("%w: literal marker must end the line", errProtocol)
			}
			data, lerr := r.readLiteral(int64(n), nonSync)
			if lerr != nil {
				// Keep the tokens parsed so far (the tag leads them) so the
				// session can answer tagged NO on an oversized literal.
				return "", toks, false, lerr
			}
			toks = append(toks, token{Kind: 's', Val: string(data)})
			return "", toks, true, nil
		default:
			s, ni, perr := scanAtom(line, i)
			if perr != nil {
				return "", nil, false, perr
			}
			toks = append(toks, token{Kind: 'a', Val: s})
			i = ni
		}
	}
	return "", toks, false, nil
}

// scanQuoted reads a quoted string starting at line[i] == '"'.
func scanQuoted(line string, i int) (string, int, error) {
	var b strings.Builder
	i++ // skip the opening quote
	for i < len(line) {
		c := line[i]
		switch c {
		case '\\':
			if i+1 >= len(line) {
				return "", 0, fmt.Errorf("%w: dangling backslash in quoted string", errProtocol)
			}
			i++
			b.WriteByte(line[i])
			i++
		case '"':
			return b.String(), i + 1, nil
		case '\r', '\n':
			return "", 0, fmt.Errorf("%w: unterminated quoted string", errProtocol)
		default:
			b.WriteByte(c)
			i++
		}
	}
	return "", 0, fmt.Errorf("%w: unterminated quoted string", errProtocol)
}

// scanLiteralMarker parses {n} / {n+} starting at line[i] == '{'.
func scanLiteralMarker(line string, i int) (n int, nonSync bool, next int, err error) {
	end := strings.IndexByte(line[i:], '}')
	if end < 0 {
		return 0, false, 0, fmt.Errorf("%w: unterminated literal marker", errProtocol)
	}
	body := line[i+1 : i+end]
	nonSync = strings.HasSuffix(body, "+")
	if nonSync {
		body = body[:len(body)-1]
	}
	v, perr := strconv.Atoi(body)
	if perr != nil || v < 0 {
		return 0, false, 0, fmt.Errorf("%w: bad literal size %q", errProtocol, body)
	}
	return v, nonSync, i + end + 1, nil
}

// scanAtom reads an atom: everything up to a space, a paren, or end of line.
// A '[' inside an atom folds the whole bracketed section (up to the first
// ']' — IMAP section specs contain no ']' within the field list) into the
// atom, so "BODY[HEADER.FIELDS (From To)]<0.10>" arrives as one token.
func scanAtom(line string, i int) (string, int, error) {
	start := i
	inSection := false
	for i < len(line) {
		c := line[i]
		switch {
		case c == '[':
			if inSection {
				return "", 0, fmt.Errorf("%w: nested section bracket", errProtocol)
			}
			inSection = true
			i++
		case c == ']' && inSection:
			inSection = false
			i++
		case c == ' ' || c == '(' || c == ')':
			if inSection {
				// Spaces and parens are legal inside a section spec.
				i++
				continue
			}
			return line[start:i], i, nil
		case c == '{' && !inSection:
			return line[start:i], i, nil
		default:
			i++
		}
	}
	if inSection {
		return "", 0, fmt.Errorf("%w: unterminated section bracket", errProtocol)
	}
	return line[start:i], i, nil
}

// wantAtom returns the next token as an atom value (uppercased when asked).
func wantAtom(toks []token, i int, upper bool) (string, int, error) {
	if i >= len(toks) {
		return "", i, fmt.Errorf("%w: missing argument", errProtocol)
	}
	t := toks[i]
	if t.Kind != 'a' && t.Kind != 's' {
		return "", i, fmt.Errorf("%w: expected an atom", errProtocol)
	}
	if upper {
		return strings.ToUpper(t.Val), i + 1, nil
	}
	return t.Val, i + 1, nil
}

// wantString accepts an atom, quoted string or literal as a mailbox-name /
// text argument.
func wantString(toks []token, i int) (string, int, error) {
	if i >= len(toks) {
		return "", i, fmt.Errorf("%w: missing argument", errProtocol)
	}
	t := toks[i]
	if t.Kind != 'a' && t.Kind != 's' {
		return "", i, fmt.Errorf("%w: expected a string", errProtocol)
	}
	return t.Val, i + 1, nil
}
