#!/bin/sh
# Copyright (c) 2026 Jonas Kaninda.
#
# This file is part of Posta.
#
# This program is free software: you can redistribute it and/or modify
# it under the terms of the GNU Affero General Public License as published by
# the Free Software Foundation, either version 3 of the License, or
# (at your option) any later version.
#
# This program is distributed in the hope that it will be useful,
# but WITHOUT ANY WARRANTY; without even the implied warranty of
# MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
# GNU Affero General Public License for more details.
#
# You should have received a copy of the GNU Affero General Public License
# along with this program.  If not, see <https://www.gnu.org/licenses/>.
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Container health check for both Posta roles.
#
set -eu

PORT="${POSTA_WORKER_HEALTH_PORT:-${POSTA_PORT:-9000}}"
PATH_="${POSTA_HEALTHCHECK_PATH:-/healthz}"
URL="http://127.0.0.1:${PORT}${PATH_}"

if wget --quiet --spider --timeout=3 --tries=1 "$URL" 2>/dev/null; then
  exit 0
fi

echo "posta healthcheck failed: ${URL}" >&2
exit 1
