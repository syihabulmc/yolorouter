<!-- frontend/src/components/apikeys/KeyCompressModal.vue
     Dedicated modal for a single key's input-compression override. The
     three-way radio (inherit / on / off) bridges to the two boolean columns
     the API stores: override=false means inherit the global setting;
     override true + enabled true means this key compresses; override true +
     enabled false means this key never compresses. Saving PATCHes only the
     two compress fields — limits/owner/scope/CSP are edited elsewhere.

     Optimistic-lock contract: the modal performs an authoritative GET on
     open (the list row's compress snapshot may be stale by the time the
     admin opens this), captures updated_at as the CAS token, and sends it
     back as expected_updated_at on save. A 409 (errcode 11013) means
     another writer committed first — we surface that and re-GET instead
     of letting two sessions fight over the same key. -->
<template>
  <n-modal
    :show="show"
    preset="card"
    :title="t('apiKeys.compressSection')"
    style="max-width: 520px"
    :mask-closable="false"
    :close-on-esc="false"
    @update:show="(v: boolean) => emit('update:show', v)"
  >
    <div v-if="loading" class="loading-row">{{ t('common.loading') }}</div>
    <n-form
      v-else
      :model="form"
      label-placement="top"
    >
      <n-form-item>
        <template #label>
          <HelpLabel :tip="t('apiKeys.compressOverride_tip')">{{ t('apiKeys.compressOverride') }}</HelpLabel>
        </template>
        <n-radio-group :value="compressMode" @update:value="onModeChange">
          <n-radio value="inherit">{{ t('apiKeys.compressOverrideInherit') }}</n-radio>
          <n-radio value="on">{{ t('apiKeys.compressModeOn') }}</n-radio>
          <n-radio value="off">{{ t('apiKeys.compressModeOff') }}</n-radio>
        </n-radio-group>
      </n-form-item>

      <p class="mode-hint">{{ modeHint }}</p>
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
import { NRadio, NRadioGroup, useMessage } from 'naive-ui'
import { useApiKeysStore } from '../../store/apiKeys'
import { displayMessage, APIError } from '../../api/client'
import { getAPIKey } from '../../api/apiKeys'
import { API_KEY_CONFLICT } from '../../api/errcodes'
import HelpLabel from '../HelpLabel.vue'

// The parent passes only the key id and remounts via :key="apiKeyId" on each
// open, so onMounted fires once per open and performs the authoritative GET
// (the list row's compress snapshot may already be stale by the time the
// modal is opened — another admin/tab could have edited the same key).
const props = defineProps<{
  show: boolean
  apiKeyId: number
}>()
const emit = defineEmits<{ (e: 'update:show', v: boolean): void; (e: 'saved'): void }>()

const { t } = useI18n()
const message = useMessage()
const store = useApiKeysStore()

const loading = ref(true)
const saving = ref(false)
// CAS token captured from the authoritative GET — sent back as
// expected_updated_at on save. A 409 means another writer bumped it after
// our read; we re-GET and surface the conflict rather than overwriting.
const expectedUpdatedAt = ref<string | null>(null)

const form = reactive({
  compress_enabled_override: false,
  compress_enabled: false,
})

// compressMode bridges the three-way radio (inherit / on / off) to the two
// underlying boolean fields the API expects (override + enabled).
type CompressMode = 'inherit' | 'on' | 'off'
const compressMode = computed<CompressMode>(() => {
  if (!form.compress_enabled_override) return 'inherit'
  return form.compress_enabled ? 'on' : 'off'
})

const modeHint = computed(() => {
  if (compressMode.value === 'inherit') return t('apiKeys.compressInheritHint')
  if (compressMode.value === 'on') return t('apiKeys.compressOnHint')
  return t('apiKeys.compressOffHint')
})

function onModeChange(mode: CompressMode) {
  if (mode === 'inherit') {
    form.compress_enabled_override = false
  } else {
    form.compress_enabled_override = true
    form.compress_enabled = mode === 'on'
  }
}

// fill adopts the authoritative GET response into the form and captures the
// CAS token. A failed GET keeps loading=true off and surfaces the error; the
// modal stays non-editable so a network blip can't trick the admin into
// saving empty defaults over the real row.
function fill(override: boolean, enabled: boolean, updatedAt: string) {
  form.compress_enabled_override = override
  form.compress_enabled = enabled
  expectedUpdatedAt.value = updatedAt
}

async function load() {
  loading.value = true
  try {
    const key = await getAPIKey(props.apiKeyId)
    fill(
      key.compress_enabled_override,
      key.compress_enabled,
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
  saving.value = true
  try {
    await store.update(props.apiKeyId, {
      compress_enabled_override: form.compress_enabled_override,
      compress_enabled: form.compress_enabled,
      // expected_updated_at ties this save to the GET we opened with — a
      // 409 means the row moved underneath us and we must re-read before
      // saving.
      expected_updated_at: expectedUpdatedAt.value ?? undefined,
    })
    emit('saved')
  } catch (err) {
    if (err instanceof APIError && err.code === API_KEY_CONFLICT) {
      // Concurrent edit: another writer (or another tab) committed first.
      // Don't let the user fight theirs — surface the conflict and reload
      // the authoritative state so the next save uses a fresh CAS token.
      message.error(t('apiKeys.compressConflict'))
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
