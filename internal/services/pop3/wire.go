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

package pop3

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// maxCommandLine bounds one client command line (RFC 1939 caps arguments at
// 255 bytes; 4096 leaves generous slack for long app-password secrets and
// AUTH PLAIN initial responses).
const maxCommandLine = 4096

// errLineTooLong reports a client command line beyond maxCommandLine.
var errLineTooLong = errors.New("pop3: command line too long")

// readCommandLine reads one CRLF-terminated command line, stripping the
// terminator. An overlong line is drained to its end (so the session cannot
// desync mid-line) and reported as errLineTooLong.
func readCommandLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		// Drain the remainder of the oversized line before failing, so a
		// follow-up command read cannot resume inside the attacker's junk.
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

// trimCRLF strips a trailing CR and/or LF from one received line.
func trimCRLF(line []byte) []byte {
	n := len(line)
	for n > 0 && (line[n-1] == '\n' || line[n-1] == '\r') {
		n--
	}
	return line[:n]
}

// splitLine breaks a command line into its verb (upper-cased) and raw
// argument ("" when absent). PASS and AUTH keep their argument verbatim —
// secrets may contain spaces; DELE/TOP/LIST re-split with splitArgs.
func splitLine(line []byte) (cmd string, arg string) {
	text := string(line)
	trimmed := trimLeftSpaces(text)
	end := -1
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] == ' ' || trimmed[i] == '\t' {
			end = i
			break
		}
	}
	if end < 0 {
		return upperASCII(trimmed), ""
	}
	return upperASCII(trimmed[:end]), trimLeftSpaces(trimmed[end+1:])
}

// splitArgs splits an argument string into whitespace-separated fields.
func splitArgs(arg string) []string {
	fields := bytes.Fields([]byte(arg))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, string(f))
	}
	return out
}

func trimLeftSpaces(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	return s
}

// upperASCII upper-cases a command verb (RFC 1939 commands are 4-letter
// ASCII; bytes >= 0x80 pass through untouched).
func upperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}

// writeStuffed writes data as the multiline response body: POP3 byte-stuffing
// prepends a '.' to every line that starts with one (RFC 1939 §2.3.1.2 —
// "octet-stuffed" lines), and the caller's terminating CRLF.CRLF is written
// separately. Line boundaries are \n; a final partial line is completed with
// CRLF so the terminator cannot fuse with message bytes.
func writeStuffed(w *bufio.Writer, data []byte) error {
	for pos := 0; pos < len(data); {
		nl := bytes.IndexByte(data[pos:], '\n')
		var line []byte
		if nl < 0 {
			line = data[pos:]
			pos = len(data)
		} else {
			line = data[pos : pos+nl+1]
			pos += nl + 1
		}
		if len(line) > 0 && line[0] == '.' {
			if err := w.WriteByte('.'); err != nil {
				return err
			}
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if _, err := w.WriteString("\r\n"); err != nil {
			return err
		}
	}
	return nil
}

// splitHeaderBody splits a raw RFC 5322 message into the header block
// (including the separating blank line) and the body, tolerating bare-LF
// messages. A message without a body separator has no body ("" returned).
func splitHeaderBody(raw []byte) (headers []byte, body []byte) {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i+4], raw[i+4:]
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return raw[:i+2], raw[i+2:]
	}
	return raw, nil
}

// firstBodyLines returns the first maxLines lines of body (a line ends at
// \n; a trailing partial line counts as one). maxLines <= 0 returns nil.
func firstBodyLines(body []byte, maxLines int) []byte {
	if maxLines <= 0 || len(body) == 0 {
		return nil
	}
	count := 0
	for pos := 0; pos < len(body); {
		nl := bytes.IndexByte(body[pos:], '\n')
		if nl < 0 {
			if count+1 >= maxLines {
				return body[pos:]
			}
			count++
			break
		}
		count++
		if count == maxLines {
			return body[pos : pos+nl+1]
		}
		pos += nl + 1
	}
	return body
}
