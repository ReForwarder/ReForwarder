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

package seeder

import "embed"

// templateFS holds the built-in seed templates. Bodies are grouped by language
// folder ("templates/<lang>/<base>.<html|txt>.tmpl") with a shared, language-
// agnostic "templates/styles.css". The .tmpl files are plain Go-template text
// (with {{ posta_* }} system variables) and are rendered by the email template
// renderer at seed time. Adding a locale means dropping in a new <lang> folder.
//
//go:embed templates
var templateFS embed.FS

// tmpl reads an embedded template file by name (relative to templates/).
// It panics on a missing file: templates are compiled into the binary, so a
// miss is always a build-time mistake, never a runtime/user condition.
func tmpl(name string) string {
	b, err := templateFS.ReadFile("templates/" + name)
	if err != nil {
		panic("seeder: missing embedded template " + name + ": " + err.Error())
	}
	return string(b)
}

// htmlTmpl / textTmpl load a localized body by base name and language code,
// resolving to "<lang>/<base>.<html|txt>.tmpl".
func htmlTmpl(base, lang string) string { return tmpl(lang + "/" + base + ".html.tmpl") }
func textTmpl(base, lang string) string { return tmpl(lang + "/" + base + ".txt.tmpl") }

// defaultCSS is the shared stylesheet inlined into every seeded template.
var defaultCSS = tmpl("styles.css")
