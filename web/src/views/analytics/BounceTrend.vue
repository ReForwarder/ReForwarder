<script setup lang="ts">
import { computed } from "vue";
import type { BounceRatePoint } from "../../api/types";
import { formatNumber, longDate } from "./chart";
import StackedBars, { type BarPoint, type Series } from "./StackedBars.vue";

const props = defineProps<{ points: BounceRatePoint[] }>();

const series: Series[] = [
  { key: "hard", label: "Hard", colorVar: "--danger-500" },
  { key: "soft", label: "Soft", colorVar: "--warning-500" },
  { key: "complaint", label: "Complaint", colorVar: "--primary-500" },
];

const bars = computed<BarPoint[]>(() =>
  props.points.map((p) => ({
    date: p.date,
    values: { hard: p.hard, soft: p.soft, complaint: p.complaint },
  }))
);

const totals = computed(() => {
  let hard = 0;
  let soft = 0;
  let complaint = 0;
  for (const p of props.points) {
    hard += p.hard;
    soft += p.soft;
    complaint += p.complaint;
  }
  return { hard, soft, complaint, total: hard + soft + complaint };
});
</script>

<template>
  <div class="card">
    <div class="card-header">
      <h2>Bounces</h2>
      <div class="summary">
        <strong>{{ formatNumber(totals.total) }}</strong> in this window
      </div>
    </div>

    <div class="card-body">
      <div v-if="totals.total === 0" class="empty-state chart-empty">
        <h3>No bounces</h3>
        <p>Nothing bounced in this window, which is the result you want.</p>
      </div>

      <template v-else>
        <StackedBars :points="bars" :series="series" chart-label="Bounces per day by type">
          <template #tip="{ point, total }">
            <strong>{{ longDate(point.date) }}</strong>
            <span><i class="key" style="background: var(--danger-500)"></i>{{ point.values.hard }} hard</span>
            <span><i class="key" style="background: var(--warning-500)"></i>{{ point.values.soft }} soft</span>
            <span><i class="key" style="background: var(--primary-500)"></i>{{ point.values.complaint }} complaint</span>
            <span class="tip-total">{{ total }} total</span>
          </template>
        </StackedBars>

        <div class="legend">
          <span><i class="key" style="background: var(--danger-500)"></i>Hard — the address does not exist</span>
          <span><i class="key" style="background: var(--warning-500)"></i>Soft — temporary, may retry</span>
          <span><i class="key" style="background: var(--primary-500)"></i>Complaint — marked as spam</span>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.summary {
  font-size: 12px;
  color: var(--text-tertiary);
}

.summary strong {
  color: var(--text-secondary);
  font-size: 13px;
}

.chart-empty {
  padding: 36px 16px;
}

.legend {
  display: flex;
  gap: 18px;
  flex-wrap: wrap;
  margin-top: 14px;
  font-size: 12px;
  color: var(--text-tertiary);
}

.key {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 2px;
  margin-right: 6px;
}

.tip-total {
  padding-top: 4px;
  margin-top: 2px;
  border-top: 1px solid var(--border-secondary);
  color: var(--text-tertiary);
}
</style>
