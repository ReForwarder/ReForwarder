<script setup lang="ts">
import { computed } from "vue";
import type { TemplateLocalization } from "../../../api/types";

const props = defineProps<{ localizations: TemplateLocalization[]; sending: boolean }>();
const emit = defineEmits<{ (e: "close"): void; (e: "send"): void }>();

export interface SendTestForm {
  to: string;
  from: string;
  language: string;
  data: string;
}

const form = defineModel<SendTestForm>("form", { required: true });

const recipients = computed(() =>
  form.value.to
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean)
);

const dataValid = computed(() => {
  try {
    JSON.parse(form.value.data || "{}");
    return true;
  } catch {
    return false;
  }
});

const valid = computed(() => recipients.value.length > 0 && dataValid.value);
const hasLanguages = computed(() => props.localizations.length > 0);
</script>

<template>
  <div class="modal" style="max-width: 560px" @mousedown.stop @mouseup.stop>
    <div class="modal-header">
      <h3>Send a test</h3>
    </div>

    <div class="modal-body">
      <div class="form-group">
        <label class="form-label" for="test-to">To</label>
        <input id="test-to" v-model="form.to" class="form-input" placeholder="you@example.com" />
        <span class="form-hint">
          <template v-if="recipients.length > 1">
            {{ recipients.length }} recipients. Each gets their own copy.
          </template>
          <template v-else>Separate several addresses with commas.</template>
        </span>
      </div>

      <div class="form-group">
        <label class="form-label" for="test-from">From</label>
        <input id="test-from" v-model="form.from" class="form-input" placeholder="Workspace default" />
        <span class="form-hint">Leave empty to send from the workspace's configured sender.</span>
      </div>

      <div class="form-group">
        <label class="form-label" for="test-language">Language</label>
        <select id="test-language" v-model="form.language" class="form-select" :disabled="!hasLanguages">
          <option v-for="l in localizations" :key="l.language" :value="l.language">
            {{ l.language }}
          </option>
        </select>
        <span v-if="!hasLanguages" class="form-hint">
          This version has no content yet, so there is nothing to send.
        </span>
      </div>

      <div class="form-group">
        <label class="form-label" for="test-data">Sample data (JSON)</label>
        <textarea id="test-data" v-model="form.data" class="form-textarea mono" rows="5"></textarea>
        <span class="form-hint" :class="{ bad: !dataValid }">
          <template v-if="dataValid">Fills the template's variables for this send.</template>
          <template v-else>This is not valid JSON.</template>
        </span>
      </div>
    </div>

    <div class="modal-footer">
      <button class="btn btn-secondary" @click="emit('close')">Cancel</button>
      <button class="btn btn-primary" :disabled="sending || !valid || !hasLanguages" @click="emit('send')">
        {{ sending ? "Sending…" : "Send test" }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.mono {
  font-family: var(--font-mono);
  font-size: 12px;
}

.bad {
  color: var(--danger-text);
}
</style>
