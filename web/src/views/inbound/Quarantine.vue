<!--
  Copyright (c) 2026 Jonas Kaninda.

  This file is part of Posta.

  This program is free software: you can redistribute it and/or modify
  it under the terms of the GNU Affero General Public License as published by
  the Free Software Foundation, either version 3 of the License, or
  (at your option) any later version.

  This program is distributed in the hope that it will be useful,
  but WITHOUT ANY WARRANTY; without even the implied warranty of
  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
  GNU Affero General Public License for more details.

  You should have received a copy of the GNU Affero General Public License
  along with this program.  If not, see <https://www.gnu.org/licenses/>.

  SPDX-License-Identifier: AGPL-3.0-or-later
-->
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { inboundQuarantineApi } from "../../api/inbound";
import type { InboundQuarantine } from "../../api/types";
import Pagination from "../../components/Pagination.vue";
import { usePagination } from "../../composables/usePagination";
import { useConfirm } from "../../composables/useConfirm";
import { useNotificationStore } from "../../stores/notification";
import { useWorkspaceStore } from "../../stores/workspace";

// Quarantine review (OB-2, ADR-002 decision 3): held inbound messages.
// Release re-runs routing for the message; delete is permanent and audited.
const notify = useNotificationStore();
const wsStore = useWorkspaceStore();
const { confirm } = useConfirm();

const loading = ref(true);
const records = ref<InboundQuarantine[]>([]);
const featureDisabled = ref(false);
const busyId = ref<number | null>(null);

const status = ref("pending");
const domainId = ref<string>("");

const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true;
  try {
    const res = await inboundQuarantineApi.list({
      page,
      status: status.value || undefined,
      domain_id: domainId.value ? Number(domainId.value) : undefined,
    });
    records.value = res.data.data;
    pageable.value = res.data.pageable;
    featureDisabled.value = false;
  } catch (e: any) {
    if (e?.response?.status === 404) {
      featureDisabled.value = true;
    } else {
      console.error("Failed to load quarantine records", e);
      notify.error("Failed to load quarantine records");
    }
  } finally {
    loading.value = false;
  }
});

function applyFilters() {
  goToPage(0);
}

function resetFilters() {
  status.value = "pending";
  domainId.value = "";
  goToPage(0);
}

async function releaseRecord(record: InboundQuarantine) {
  const okToRelease = await confirm({
    title: "Release message",
    message:
      "Routing will be re-run for this message and it will be delivered through the route it now matches.",
    confirmText: "Release",
    variant: "info",
  });
  if (!okToRelease) return;
  busyId.value = record.id;
  try {
    await inboundQuarantineApi.release(record.id);
    notify.success("Message released — routing re-run");
    goToPage(pageable.value?.current_page ?? 0);
  } catch (e: any) {
    if (e?.response?.status === 422) {
      notify.error("Cannot release: the recipient no longer matches an enabled route on a verified domain");
    } else {
      notify.error("Failed to release message");
    }
  } finally {
    busyId.value = null;
  }
}

async function deleteRecord(record: InboundQuarantine) {
  const okToDelete = await confirm({
    title: "Delete message permanently",
    message:
      "The message and its attachments will be destroyed permanently. This cannot be undone.",
    confirmText: "Delete permanently",
    variant: "danger",
  });
  if (!okToDelete) return;
  busyId.value = record.id;
  try {
    await inboundQuarantineApi.delete(record.id);
    notify.success("Message deleted permanently");
    goToPage(pageable.value?.current_page ?? 0);
  } catch {
    notify.error("Failed to delete message");
  } finally {
    busyId.value = null;
  }
}

function statusBadgeClass(status: string) {
  switch (status) {
    case "pending":
      return "badge badge-warning";
    case "released":
      return "badge badge-success";
    case "deleted":
      return "badge badge-danger";
    default:
      return "badge";
  }
}

function formatDate(date: string | null | undefined) {
  if (!date) return "-";
  return new Date(date).toLocaleString();
}

function formatBytes(n: number | undefined) {
  if (!n) return "-";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(2)} MB`;
}

function formatScore(score: number | null | undefined) {
  return score === null || score === undefined ? "-" : score.toFixed(2);
}
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Quarantine</h1>
    </div>

    <div v-if="loading" class="loading-page">
      <div class="spinner"></div>
    </div>

    <div v-else-if="featureDisabled" class="card">
      <div class="empty-state">
        <h3>Inbound email is disabled</h3>
        <p>
          Ask your administrator to enable inbound processing by setting
          <code>POSTA_INBOUND_ENABLED=true</code> and configuring the SMTP receiver.
        </p>
      </div>
    </div>

    <template v-else>
      <div class="card">
        <div
          class="card-body"
          style="display: flex; gap: 12px; flex-wrap: wrap; align-items: flex-end"
        >
          <div style="flex: 1 1 140px">
            <label class="form-label">Status</label>
            <select v-model="status" class="form-select" @change="applyFilters">
              <option value="pending">Pending review</option>
              <option value="released">Released</option>
              <option value="deleted">Deleted</option>
              <option value="">Any</option>
            </select>
          </div>

          <div style="flex: 1 1 140px">
            <label class="form-label">Domain ID</label>
            <input
              v-model="domainId"
              class="form-input"
              type="number"
              min="1"
              placeholder="All domains"
              @keyup.enter="applyFilters"
            />
          </div>

          <div style="display: flex; gap: 8px">
            <button class="btn btn-primary" @click="applyFilters">Apply</button>
            <button class="btn btn-secondary" @click="resetFilters">Reset</button>
          </div>
        </div>

        <div v-if="records.length === 0" class="empty-state">
          <h3>No quarantined messages</h3>
          <p>
            Messages routed to quarantine by an inbound routing rule will appear here
            for review.
          </p>
        </div>
        <template v-else>
          <div class="table-wrapper">
            <table>
              <thead>
                <tr>
                  <th>From</th>
                  <th>Subject</th>
                  <th>Reason</th>
                  <th>Score</th>
                  <th>Status</th>
                  <th>Received At</th>
                  <th v-if="wsStore.canEdit">Actions</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="record in records" :key="record.id">
                  <td>{{ record.inbound?.sender || "-" }}</td>
                  <td>{{ record.inbound?.subject || "(no subject)" }}</td>
                  <td>{{ record.reason }}</td>
                  <td>{{ formatScore(record.score) }}</td>
                  <td>
                    <span :class="statusBadgeClass(record.status)">{{
                      record.status
                    }}</span>
                  </td>
                  <td>{{ formatDate(record.received_at) }}</td>
                  <td v-if="wsStore.canEdit">
                    <template v-if="record.status === 'pending'">
                      <button
                        class="btn btn-primary btn-sm"
                        style="margin-right: 6px"
                        :disabled="busyId === record.id"
                        @click="releaseRecord(record)"
                      >
                        Release
                      </button>
                      <button
                        class="btn btn-danger btn-sm"
                        :disabled="busyId === record.id"
                        @click="deleteRecord(record)"
                      >
                        Delete
                      </button>
                    </template>
                    <span v-else-if="record.status === 'released'" class="text-muted">
                      {{ formatDate(record.reviewed_at) }}
                    </span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <Pagination :pageable="pageable" @page="goToPage" />
        </template>
      </div>
    </template>
  </div>
</template>
