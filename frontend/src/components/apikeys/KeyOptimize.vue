<template>
  <n-modal
    :show="show"
    preset="card"
    :title="t('costOptimization.title')"
    style="max-width: 520px"
    :mask-closable="false"
    :close-on-esc="false"
    @update:show="(v: boolean) => emit('update:show', v)"
  >
    <div v-if="loading" class="loading-row">{{ t('common.loading') }}</div>
    <n-form
      v-else
      ref="formRef"
      require-mark-placement="left"
      :model="form"
      :rules="rules"
      label-placement="top"
    >
      <n-form-item>
        <template #label>
          <HelpLabel :tip="t('apiKeys.cspOverride_tip')">{{ t('apiKeys.cspOverride') }}</HelpLabel>
        </template>
        <n-radio-group :value="cspMode" @update:value="onCspModeChange">
          <n-radio value="inherit">{{ t('apiKeys.cspOverrideInherit') }}</n-radio>
          <n-radio value="on">{{ t('apiKeys.cspModeOn') }}</n-radio>
          <n-radio value="off">{{ t('apiKeys.cspModeOff') }}</n-radio>
        </n-radio-group>
      </n-form-item>

      <n-form-item v-if="cspMode === 'on'" path="custom_system_prompt">
        <template #label>
          <HelpLabel :tip="t('costOptimization.cspTitle_tip')">{{ t('costOptimization.cspTitle') }}</HelpLabel>
        </template>
        <CustomPromptEditor
          v-model:text="form.custom_system_prompt"
          :autosize="{ minRows: 4 }"
          :show-input="false"
          :multiple="true"
          :placeholder="t('apiKeys.cspTextPlaceholder')"
        />
      </n-form-item>

      <p v-else class="mode-hint">{{ cspMode === 'inherit' ? t('apiKeys.cspInheritHint') : t('apiKeys.cspOffHint') }}</p>
    </n-form>

    <template #footer>
      <n-space justify="end">
        <n-button @click="emit('update:show', false)">{{ t('apiKeys.cancel') }}</n-button>
        <n-button type="primary" :loading="saving" :disabled="loading" @click="onSave">{{ t('apiKeys.save') }}</n-button>
      </n-space>
    </template>
  </n-modal>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NRadio, NRadioGroup, useMessage, type FormInst, type FormRules } from 'naive-ui'
import { useApiKeysStore } from '../../store/apiKeys'
import { displayMessage, APIError } from '../../api/client'
import { getAPIKey } from '../../api/apiKeys'
import { API_KEY_CONFLICT } from '../../api/errcodes'
import { customSystemPromptRule } from '../../utils/apiKeyValidators'
import HelpLabel from '../HelpLabel.vue'
import CustomPromptEditor from '../CustomPromptEditor.vue'

// The parent passes only the key id and remounts via :key="apiKeyId" on each
// open, so onMounted fires once per open and performs the authoritative GET
// (the list row's CSP snapshot may already be stale by the time the modal is
// opened — another admin/tab could have edited the same key).
const props = defineProps<{
  show: boolean
  apiKeyId: number
}>()
const emit = defineEmits<{ (e: 'update:show', v: boolean): void; (e: 'saved'): void }>()

const { t } = useI18n()
const message = useMessage()
const store = useApiKeysStore()

const formRef = ref<FormInst | null>(null)
const loading = ref(true)
const saving = ref(false)
// CAS token captured from the authoritative GET — sent back as
// expected_updated_at on save. A 409 means another writer bumped it after our
// read; we re-GET and surface the conflict rather than overwriting.
const expectedUpdatedAt = ref<string | null>(null)

const form = reactive({
  custom_system_prompt_enabled_override: false,
  custom_system_prompt_enabled: false,
  custom_system_prompt: '',
})

const rules = computed<FormRules>(() => ({
  // CSP text is required only when override is active and enabled; the 2000
  // rune cap mirrors the service layer's MaxCustomSystemPromptLen.
  custom_system_prompt: customSystemPromptRule(
    t,
    form.custom_system_prompt_enabled_override,
    form.custom_system_prompt_enabled,
  ),
}))

// cspMode bridges the three-way radio (inherit / on / off) to the two
// underlying boolean fields the API expects (override + enabled).
type CspMode = 'inherit' | 'on' | 'off'
const cspMode = computed<CspMode>(() => {
  if (!form.custom_system_prompt_enabled_override) return 'inherit'
  return form.custom_system_prompt_enabled ? 'on' : 'off'
})
function onCspModeChange(mode: CspMode) {
  if (mode === 'inherit') {
    form.custom_system_prompt_enabled_override = false
  } else {
    form.custom_system_prompt_enabled_override = true
    form.custom_system_prompt_enabled = mode === 'on'
  }
}

// fill adopts the authoritative GET response into the form and captures the
// CAS token. A failed GET keeps loading=true off and surfaces the error; the
// modal stays non-editable so a network blip can't trick the admin into
// saving empty defaults over the real row.
function fill(override: boolean, enabled: boolean, text: string, updatedAt: string) {
  form.custom_system_prompt_enabled_override = override
  form.custom_system_prompt_enabled = enabled
  form.custom_system_prompt = text
  expectedUpdatedAt.value = updatedAt
}

async function load() {
  loading.value = true
  try {
    const key = await getAPIKey(props.apiKeyId)
    fill(
      key.custom_system_prompt_enabled_override,
      key.custom_system_prompt_enabled,
      key.custom_system_prompt,
      key.updated_at,
    )
  } catch (err) {
    message.error(displayMessage(err, t))
    emit('update:show', false)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
})

async function onSave() {
  if (saving.value || loading.value) return
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  saving.value = true
  try {

    await store.update(props.apiKeyId, {
      custom_system_prompt_enabled_override: form.custom_system_prompt_enabled_override,
      custom_system_prompt_enabled: form.custom_system_prompt_enabled,
      custom_system_prompt: form.custom_system_prompt,
      compress_enabled_override: form.custom_system_prompt_enabled_override,
      compress_enabled: form.custom_system_prompt_enabled,
      // expected_updated_at ties this save to the GET we opened with — a 409
      // means the row moved underneath us and we must re-read before saving.
      expected_updated_at: expectedUpdatedAt.value ?? undefined,
    })

    emit('saved')
  } catch (err) {
    if (err instanceof APIError && err.code === API_KEY_CONFLICT) {
      // Concurrent edit: another writer (or another tab) committed first.
      // Don't let the user fight theirs — surface the conflict and reload the
      // authoritative state so the next save uses a fresh CAS token.
      message.error(t('apiKeys.cspConflict'))
      await load()
    } else {
      message.error(displayMessage(err, t))
    }
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.mode-hint {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  font-size: var(--text-sm);
  color: var(--color-text-muted);
  background: var(--color-bg-elevated, var(--color-bg));
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-md);
}

.loading-row {
  padding: var(--space-4) 0;
  color: var(--color-text-muted);
}
</style>
