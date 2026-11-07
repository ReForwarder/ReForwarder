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

package routes

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/cron"
	"github.com/goposta/posta/internal/handlers"
	"github.com/goposta/posta/internal/services/inbound"
	"github.com/goposta/posta/internal/services/messages"
	"github.com/jkaninda/okapi"
)

// The API contract (Section-11 gate, R-21 follow-up): the frontend once called
// POST /subscriber-lists/preview while the backend registered
// /subscriber-lists/preview-segment — a drift no unit test on either side
// catches. This test pins the whole surface: it registers every route the
// server exposes (all feature flags on) and diffs the method+path list against
// a golden file. Any route change now fails the build until the golden file is
// consciously regenerated, and web/src/api/admin.spec.ts checks the frontend
// side of the same file.
//
// To regenerate after an intentional API change:
//
//	go test ./internal/routes/ -run TestAPIRouteContract -update
//
// The golden file lives in docs/implementation/api_routes.txt (it is
// documentation as much as a fixture); its header documents this workflow.

const contractGoldenPath = "../../docs/implementation/api_routes.txt"

// contractGoldenHeader is written (and rewritten) above the route list on
// every -update pass, so the workflow documentation travels with the file.
const contractGoldenHeader = `# ReForwarder API route contract (generated — do not edit the route lines)
#
# One "METHOD PATH" pair per line, sorted; registered by
# internal/routes/contract_test.go with every feature flag enabled
# (POSTA_METRICS_ENABLED / POSTA_INBOUND_ENABLED / POSTA_MESSAGES_ENABLED /
# POSTA_SMTP_RELAY_ENABLED), so the list includes the env-gated groups.
#
# The Go side (TestAPIRouteContract) fails when a route is added, removed or
# repathed without regenerating this file; the Vue side
# (web/src/api/admin.spec.ts) reads the same file and fails when an api/*.ts
# module calls an endpoint that is not registered here. This is the R-21
# guard: the frontend once called /subscriber-lists/preview while the backend
# registered /subscriber-lists/preview-segment.
#
# After an INTENTIONAL API change:
#   1. go test ./internal/routes/ -run TestAPIRouteContract -update
#   2. commit the regenerated file together with the route change
#   3. update web/src/api/*.ts callers if the change was breaking
`

var contractUpdate = flag.Bool("update", false, "rewrite docs/implementation/api_routes.txt from the currently registered routes")

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}

// contractServer registers the full route surface with every feature flag
// enabled, so the golden file pins the complete API including the
// env-gated route groups (inbound, messages, relay, metrics) instead of
// only the default-on subset. Handlers are constructed with nil
// repositories: registration only captures handler references, nothing is
// ever dispatched.
func contractServer(t *testing.T) *okapi.TestServer {
	t.Helper()

	app := okapi.NewTestServer(t)

	// The cron handler is nil-guarded in adminRoutes (no manager, no
	// /admin/jobs), so a real one is constructed to keep the golden file
	// identical to a production boot.
	cronHandler := handlers.NewCronHandler(cron.NewManager(nil))

	// The SF-59 operational actions are registered only when a worker producer
	// is wired (r.h.queueOps != nil); a production boot always runs one, so
	// construct the handler with nil collaborators for the same reason.
	queueOps := handlers.NewQueueOpsHandler(nil, nil, nil, nil)

	// inbound and formIngest handlers exist only when their feature flag is on;
	// construct them the same way InitRoutes does (nil stores are fine — the
	// constructors are field-assign only).
	messageSvc := messages.NewService(nil, nil, nil, nil, nil, messages.Config{}, []byte("contract-test"))
	formIngest := handlers.NewFormIngestHandler(messageSvc, inbound.NewIPRateLimiter(0, 0), 0)
	inboundHandler := handlers.NewInboundHandler(
		inbound.NewService(nil, nil, nil, inbound.Config{}), nil, nil, "", nil,
	)

	r := &Router{
		app: app.Okapi,
		cfg: &config.Config{
			MetricsEnabled:   true,
			InboundEnabled:   true,
			MessagesEnabled:  true,
			SMTPRelayEnabled: true,
		},
		v1: app.Group("/api/v1"),
		h: routerHandlers{
			cron:       cronHandler,
			formIngest: formIngest,
			inbound:    inboundHandler,
			queueOps:   queueOps,
		},
	}
	r.registerRoutes()
	return app
}

func contractRoutes(t *testing.T) []string {
	t.Helper()
	app := contractServer(t)

	lines := make([]string, 0, 256)
	for _, route := range app.Routes() {
		method := strings.ToUpper(route.Method)
		lines = append(lines, fmt.Sprintf("%-6s %s", method, route.Path))
	}
	sort.Strings(lines)
	return lines
}

func TestAPIRouteContract(t *testing.T) {
	routes := contractRoutes(t)
	got := contractGoldenHeader + strings.Join(routes, "\n") + "\n"

	if *contractUpdate {
		abs, err := filepath.Abs(contractGoldenPath)
		if err != nil {
			t.Fatalf("resolve golden path: %v", err)
		}
		if err := os.WriteFile(abs, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden file: %v", err)
		}
		t.Logf("API contract written to %s (%d routes)", abs, len(routes))
		return
	}

	want, err := os.ReadFile(contractGoldenPath)
	if err != nil {
		t.Fatalf("read golden file %s (run `go test ./internal/routes/ -run TestAPIRouteContract -update` after an intentional API change): %v", contractGoldenPath, err)
	}
	if got != string(want) {
		t.Fatalf(`registered routes differ from docs/implementation/api_routes.txt.
The API surface changed; if the change is intentional, regenerate the golden file with:
  go test ./internal/routes/ -run TestAPIRouteContract -update
and update the frontend callers (web/src/api/admin.spec.ts guards the other side).

--- want (golden) ---
%s
--- got (registered) ---
%s`, string(want), got)
	}
}
