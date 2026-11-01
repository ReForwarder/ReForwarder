<script setup lang="ts">
import { computed, ref } from "vue";
import type { ProviderBreakdownPoint } from "../../api/types";
import { formatNumber } from "./chart";

const props = defineProps<{ rows: ProviderBreakdownPoint[] }>();

type SortKey = "total" | "delivery_rate" | "failed";
const sortBy = ref<SortKey>("total");

const total = computed(() => props.rows.reduce((sum, r) => sum + r.total, 0));

const sorted = computed(() =>
  [...props.rows].sort((a, b) => {
    if (sortBy.value === "delivery_rate") {
      // A provider with no attempts has no rate; keep those last rather than
      // letting a 0% with zero volume top the table.
      const av = a.sent + a.failed === 0 ? -1 : a.delivery_rate;
      const bv = b.sent + b.failed === 0 ? -1 : b.delivery_rate;
      return av - bv;
    }
    return b[sortBy.value] - a[sortBy.value];
  })
);

// The provider that is actually hurting deliverability: enough volume to matter
// and a rate below par. Naming it saves the reader scanning the table.
const problem = computed(() => {
  let found: ProviderBreakdownPoint | null = null;
  for (const r of props.rows) {
    const attempts = r.sent + r.failed;
    if (attempts < 20 || r.delivery_rate >= 95) continue;
    if (!found || r.failed > found.failed) found = r;
  }
  return found;
});

// Per-provider brand colors were hardcoded hexes and are gone under the
// monochrome brand: identity lives in the name column, so the dot and the
// share bar are one neutral ink that reads on both canvases.
function color(_provider: string): string {
  return "var(--text-muted)";
}

function share(value: number): number {
  return total.value === 0 ? 0 : (value / total.value) * 100;
}

function rateClass(r: ProviderBreakdownPoint): string {
  if (r.sent + r.failed === 0) return "";
  if (r.delivery_rate < 75) return "rate-bad";
  if (r.delivery_rate < 90) return "rate-warn";
  return "rate-good";
}
</script>

<template>
  <div class="card">
    <div class="card-header">
      <h2>Mailbox providers</h2>
      <div class="summary">
        <strong>{{ formatNumber(total) }}</strong> recipients
        <template v-if="problem">
          <span class="dot">·</span>
          <span class="warn">
            {{ problem.provider }} at {{ problem.delivery_rate.toFixed(0) }}%
          </span>
        </template>
      </div>
    </div>

    <div class="card-body">
      <div v-if="rows.length === 0" class="empty-state chart-empty">
        <h3>No recipient data</h3>
        <p>Provider deliverability appears once mail goes to real mailboxes.</p>
      </div>

      <template v-else>
        <p class="lead">
          Where your mail went, and how each provider treated it. A rate well below the others
          usually means a reputation problem with that provider specifically, not with your
          sending overall.
        </p>

        <div class="sorts">
          <span>Sort by</span>
          <button
            v-for="s in (['total', 'delivery_rate', 'failed'] as const)"
            :key="s"
            type="button"
            class="sort"
            :class="{ active: sortBy === s }"
            @click="sortBy = s"
          >
            {{ s === "total" ? "Volume" : s === "delivery_rate" ? "Worst rate" : "Failures" }}
          </button>
        </div>

        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Provider</th>
                <th>Share</th>
                <th class="num">Delivered</th>
                <th class="num">Failed</th>
                <th class="num" title="Blocked before sending because every recipient was suppressed">Suppressed</th>
                <th class="num">Rate</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in sorted" :key="r.provider">
                <td>
                  <span class="dot-inline" :style="{ background: color(r.provider) }" aria-hidden="true"></span>
                  {{ r.provider }}
                </td>
                <td class="share-cell">
                  <div class="track">
                    <div class="fill" :style="{ width: share(r.total) + '%', background: color(r.provider) }"></div>
                  </div>
                  <span class="share-pct">{{ share(r.total).toFixed(1) }}%</span>
                </td>
                <td class="num">{{ r.sent.toLocaleString() }}</td>
                <td class="num">{{ r.failed.toLocaleString() }}</td>
                <td class="num">{{ r.suppressed.toLocaleString() }}</td>
                <td class="num rate" :class="rateClass(r)">
                  {{ r.sent + r.failed > 0 ? r.delivery_rate.toFixed(1) + "%" : "—" }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.summary {
  display: flex;
  align-items: baseline;
  gap: 6px;
  font-size: 12px;
  color: var(--text-tertiary);
}

.summary strong {
  color: var(--text-secondary);
  font-size: 13px;
}

.dot {
  opacity: 0.6;
}

.warn {
  color: var(--warning-text);
  font-weight: 500;
}

.chart-empty {
  padding: 36px 16px;
}

.lead {
  margin: 0 0 14px;
  font-size: 12px;
  color: var(--text-tertiary);
  line-height: 1.6;
  max-width: 70ch;
}

.sorts {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
  font-size: 12px;
  color: var(--text-tertiary);
}

.sort {
  border: 0;
  background: none;
  padding: 0;
  font: inherit;
  font-size: 12px;
  color: var(--text-tertiary);
  cursor: pointer;
}

.sort:hover {
  color: var(--text-primary);
}

.sort.active {
  color: var(--primary-600);
  font-weight: 600;
}

.table-scroll {
  overflow-x: auto;
}

.dot-inline {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 8px;
}

/* Technical identity column — provider names carry the mono voice. */
td:first-child {
  font-family: var(--font-mono);
  font-size: 13px;
}

.num {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.share-cell {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 170px;
}

.track {
  flex: 1;
  height: 6px;
  border-radius: 3px;
  background: var(--bg-hover);
  overflow: hidden;
}

.fill {
  height: 100%;
  border-radius: 3px;
}

.share-pct {
  min-width: 44px;
  text-align: right;
  font-size: 12px;
  color: var(--text-tertiary);
  font-variant-numeric: tabular-nums;
}

.rate {
  font-weight: 600;
}

.rate-good {
  color: var(--success-text);
}

.rate-warn {
  color: var(--warning-text);
}

.rate-bad {
  color: var(--danger-text);
}
</style>
