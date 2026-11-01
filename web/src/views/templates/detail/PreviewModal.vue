<script setup lang="ts">
import { ref, watch } from "vue";
import type { TemplatePreview } from "../../../api/types";

const props = defineProps<{
  language: string;
  preview: TemplatePreview | null;
  loading: boolean;
  error: string;
}>();

const emit = defineEmits<{ (e: "close"): void; (e: "render"): void }>();

const data = defineModel<string>("data", { required: true });

// Most mail is opened on a phone, and a layout that only works at desktop width
// is the commonest way a template disappoints. The width toggle makes that
// checkable here instead of after sending a test to yourself.
const WIDTHS = [
  { key: "desktop", label: "Desktop", px: 0 },
  { key: "mobile", label: "Mobile", px: 375 },
];
const width = ref("desktop");

const dataError = ref("");

// Re-render as the sample data is edited, the way the full editor does, so the
// preview is never quietly stale relative to the JSON above it.
let timer: ReturnType<typeof setTimeout> | null = null;
watch(data, (value) => {
  if (timer) clearTimeout(timer);
  try {
    JSON.parse(value || "{}");
    dataError.value = "";
  } catch {
    dataError.value = "This is not valid JSON, so the preview is not being re-rendered.";
    return;
  }
  timer = setTimeout(() => emit("render"), 400);
});
</script>

<template>
  <div class="modal preview-modal" @mousedown.stop @mouseup.stop>
    <div class="modal-header">
      <h3>Preview — {{ language }}</h3>
      <div class="widths" role="group" aria-label="Preview width">
        <button
          v-for="w in WIDTHS"
          :key="w.key"
          type="button"
          class="width"
          :class="{ active: width === w.key }"
          :aria-pressed="width === w.key"
          @click="width = w.key"
        >
          {{ w.label }}
        </button>
      </div>
    </div>

    <div class="modal-body">
      <div class="form-group">
        <label class="form-label" for="preview-data">Sample data (JSON)</label>
        <textarea id="preview-data" v-model="data" class="form-textarea mono" rows="3"></textarea>
        <span class="form-hint">
          <template v-if="dataError">{{ dataError }}</template>
          <template v-else>Edits re-render the preview automatically.</template>
        </span>
      </div>

      <div v-if="error" class="render-error">{{ error }}</div>

      <template v-if="preview">
        <div class="section">
          <div class="section-label">Subject</div>
          <div class="subject">{{ preview.subject }}</div>
        </div>

        <div v-if="preview.html" class="section">
          <div class="section-label">
            HTML
            <span v-if="loading" class="rendering">rendering…</span>
          </div>
          <div class="frame-wrap" :class="width">
            <iframe
              :srcdoc="preview.html"
              sandbox=""
              class="frame"
              title="Rendered HTML preview"
            ></iframe>
          </div>
        </div>

        <div v-if="preview.text" class="section">
          <div class="section-label">Plain text</div>
          <pre class="text">{{ preview.text }}</pre>
        </div>

        <p v-if="!preview.text" class="no-text">
          This language has no plain-text body. Clients that do not render HTML will show nothing.
        </p>
      </template>
    </div>

    <div class="modal-footer">
      <button class="btn btn-secondary" @click="emit('close')">Close</button>
    </div>
  </div>
</template>

<style scoped>
.preview-modal {
  max-width: 860px;
  width: 100%;
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.widths {
  display: flex;
  gap: 4px;
}

.width {
  padding: 4px 12px;
  border: 1px solid var(--border-primary);
  border-radius: 999px;
  background: var(--bg-primary);
  font: inherit;
  font-size: 12px;
  color: var(--text-secondary);
  cursor: pointer;
}

.width.active {
  background: var(--bg-active);
  border-color: var(--primary-500);
  color: var(--primary-600);
}

.mono {
  font-family: var(--font-mono);
  font-size: 12px;
}

.render-error {
  padding: 10px 14px;
  margin-bottom: 16px;
  border-radius: var(--radius);
  background: var(--danger-50);
  color: var(--danger-text);
  font-size: 13px;
}

.section {
  margin-bottom: 16px;
}

.section-label {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-tertiary);
}

.rendering {
  text-transform: none;
  letter-spacing: 0;
  font-weight: 400;
  opacity: 0.8;
}

.subject {
  padding: 10px 14px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-tertiary);
  font-size: 14px;
  color: var(--text-primary);
}

.frame-wrap {
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  overflow: hidden;
  /* Rendered email is authored on white — constant in both themes. */
  background: var(--bg-email-canvas);
}

.frame-wrap.mobile {
  max-width: 375px;
  margin: 0 auto;
}

.frame {
  display: block;
  width: 100%;
  min-height: 340px;
  border: 0;
  background: var(--bg-email-canvas);
}

.text {
  margin: 0;
  padding: 10px 14px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-tertiary);
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--text-secondary);
  white-space: pre-wrap;
  overflow-x: auto;
}

.no-text {
  margin: 0;
  font-size: 12px;
  color: var(--text-tertiary);
}
</style>
