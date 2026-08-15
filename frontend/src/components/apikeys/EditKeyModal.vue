<!-- frontend/src/components/apikeys/EditKeyModal.vue
     Edits one key's owner/remark/limits/allowlist via sparse PATCH. Limit
     fields use "clear = unlimited" semantics: an empty input maps to the
     backend's 0-sentinel which clears the column. Expiry can be set or moved
     later but not cleared (backend has no clear-sentinel for timestamps) —
     to remove an expiry, revoke and re-create. Per-key custom-system-prompt
     and compression are edited in the dedicated optimization modal. -->
<template>
  <ModalDrawer
    v-model:show="showModel"
    :title="t('apiKeys.editTitle')"
    max-width="520px"
    :mask-closable="false"
    :close-on-esc="false"
    :confirm-text="t('apiKeys.save')"
    :cancel-text="t('apiKeys.cancel')"
    :loading="saving"
    :back-label="t('common.back')"
    @confirm="onSave"
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
      <n-form-item path="owner_label">
        <template #label>
          <HelpLabel :tip="t('apiKeys.ownerLabel_tip')">{{ t('apiKeys.ownerLabel') }}</HelpLabel>
        </template>
        <n-input v-model:value="form.owner_label" :maxlength="50" />
      </n-form-item>
      <n-form-item path="remark">
        <template #label>
          <HelpLabel :tip="t('apiKeys.remark_tip')">{{ t('apiKeys.remark') }}</HelpLabel>
        </template>
        <n-input v-model:value="form.remark" type="textarea" :autosize="{ minRows: 2 }" :maxlength="200" />
      </n-form-item>
      <n-form-item v-if="authStore.isAdmin">
        <template #label>
          <HelpLabel :tip="t('apiKeys.modelScope_tip')">{{ t('apiKeys.modelScope') }}</HelpLabel>
        </template>
        <n-radio-group v-model:value="form.allow_all_models">
          <n-radio :value="true">{{ t('apiKeys.modelScopeAll') }}</n-radio>
          <n-radio :value="false">{{ t('apiKeys.modelScopeCustom') }}</n-radio>
        </n-radio-group>
      </n-form-item>
      <n-form-item v-if="authStore.isAdmin && !form.allow_all_models" path="model_ids">
        <template #label>
          <HelpLabel :tip="t('apiKeys.modelAllowlist_tip')">{{ t('apiKeys.modelAllowlist') }}</HelpLabel>
        </template>
        <FilterSelectField
          :value="form.model_ids"
          :label="t('apiKeys.modelAllowlist')"
          multiple
          filterable
          size="medium"
          :clearable="false"
          :options="modelOptions"
          :placeholder="t('apiKeys.modelAllowlist')"
          width="100%"
          class="w-full"
          @update:value="(v: number | number[] | null) => (form.model_ids = (v as number[] | null) ?? [])"
        />
      </n-form-item>
      <div :style="isMobile ? 'position: absolute; top: 12px; right: 10px;' : 'position: absolute; top: 17px; right: 60px;'">
        <NDatePicker v-model:value="form.expires_at" type="datetime" :clearable="false" class="full-width" :placeholder="t('apiKeys.selectExpiresAt')" />
      </div>

      <div v-if="authStore.isAdmin" class="limit-section">
        <div class="limit-section__label">{{ t('apiKeys.limitsSection') }}</div>
        <div class="limit-grid">
          <n-form-item>
            <template #label>
              <HelpLabel :tip="t('apiKeys.rpmLimit_tip')">{{ t('apiKeys.rpmLimit') }}</HelpLabel>
            </template>
            <n-input-number v-model:value="form.rpm_limit" :min="0" :placeholder="t('apiKeys.clearByZeroHint')" class="full-width" />
          </n-form-item>
          <n-form-item>
            <template #label>
              <HelpLabel :tip="t('apiKeys.tpmLimit_tip')">{{ t('apiKeys.tpmLimit') }}</HelpLabel>
            </template>
            <n-input-number v-model:value="form.tpm_limit" :min="0" :placeholder="t('apiKeys.clearByZeroHint')" class="full-width" />
          </n-form-item>
          <n-form-item>
            <template #label>
              <HelpLabel :tip="t('apiKeys.concurrencyLimit_tip')">{{ t('apiKeys.concurrencyLimit') }}</HelpLabel>
            </template>
            <n-input-number v-model:value="form.concurrency_limit" :min="0" :placeholder="t('apiKeys.clearByZeroHint')" class="full-width" />
          </n-form-item>
          <n-form-item>
            <template #label>
              <HelpLabel :tip="t('apiKeys.budgetLimit_tip')">{{ t('apiKeys.budgetLimit') }}</HelpLabel>
            </template>
            <n-input-number v-model:value="form.budget_amount" :min="0" :step="0.01" :placeholder="t('apiKeys.clearByZeroHint')" class="full-width" />
          </n-form-item>
        </div>
      </div>
    </n-form>
  </ModalDrawer>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NDatePicker, NRadio, NRadioGroup, useMessage, type FormInst, type FormRules } from 'naive-ui'
import { useApiKeysStore } from '../../store/apiKeys'
import { useModelsStore } from '../../store/models'
import { useAuthStore } from '../../store/auth'
import { displayMessage } from '../../api/client'
import { getAPIKey, type APIKey, type UpdateAPIKeyInput } from '../../api/apiKeys'
import { fromMicros, toMicros } from '../../utils/money'
import { modelIdsRule } from '../../utils/apiKeyValidators'
import HelpLabel from '../HelpLabel.vue'
import ModalDrawer from '../common/ModalDrawer.vue'
import FilterSelectField from '../common/FilterSelectField.vue'
import { useIsMobile } from '../../composables/useIsMobile'

const props = defineProps<{ show: boolean; apiKeyId: number }>()
const emit = defineEmits<{ (e: 'update:show', v: boolean): void; (e: 'saved'): void }>()

// ModalDrawer owns a v-model:show; bridge it to this component's existing
// :show / @update:show contract so the parent doesn't have to change.
const showModel = computed({
  get: () => props.show,
  set: (v) => emit('update:show', v),
})

const { t } = useI18n()
const message = useMessage()
const store = useApiKeysStore()
const modelsStore = useModelsStore()
const authStore = useAuthStore()

// Drives the header float position for the expiry picker (mobile drawer vs
// desktop card anchor differently).
const isMobile = useIsMobile()

const formRef = ref<FormInst | null>(null)
const loading = ref(true)
const saving = ref(false)

const form = reactive({
  owner_label: '',
  remark: '',
  allow_all_models: false,
  model_ids: [] as number[],
  expires_at: null as number | null,
  rpm_limit: null as number | null,
  tpm_limit: null as number | null,
  concurrency_limit: null as number | null,
  budget_amount: null as number | null,
})

const rules = computed<FormRules>(() => ({
  owner_label: [{ max: 50, trigger: ['blur', 'input'] }],
  remark: [{ max: 200, trigger: ['blur', 'input'] }],
  // A custom allowlist needs at least one model; an all-models key needs none.
  model_ids: modelIdsRule(t, !form.allow_all_models),
}))

const modelOptions = computed(() =>
  modelsStore.list.map((m) => ({ label: m.name, value: m.id })),
)

// initialExpiresAt captures the expiry loaded by fill() so onSave can tell
// "user changed expiry" from "user left the original expiry alone". Without
// it, an already-expired key couldn't be edited for unrelated fields (the
// future-time check would reject the loaded past expiry on every save).
const initialExpiresAt = ref<number | null>(null)

function fill(k: APIKey) {
  form.owner_label = k.owner_label
  form.remark = k.remark
  form.allow_all_models = k.allow_all_models
  form.model_ids = [...k.model_ids]
  form.expires_at = k.expires_at ? new Date(k.expires_at).getTime() : null
  initialExpiresAt.value = form.expires_at
  form.rpm_limit = k.rpm_limit
  form.tpm_limit = k.tpm_limit
  form.concurrency_limit = k.concurrency_limit
  form.budget_amount = k.budget_limit_micros != null ? fromMicros(k.budget_limit_micros) : null
}

onMounted(async () => {
  // The models list is best-effort for the allowlist picker; don't let its
  // failure block loading the key — and fetchList's own .catch already
  // swallowed its rejection, so Promise.all bought nothing but ceremony.
  // The catalog endpoint is admin-only and the picker is hidden for
  // members, so they skip the fetch entirely.
  if (authStore.isAdmin) {
    void modelsStore.fetchList().catch((err) => message.error(displayMessage(err, t)))
  }
  try {
    const key = await getAPIKey(props.apiKeyId)
    fill(key)
  } catch (err) {
    message.error(displayMessage(err, t))
    emit('update:show', false)
  } finally {
    loading.value = false
  }
})

async function onSave() {
  if (loading.value) return
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  // Only validate/send expiry when the user actually changed it. An
  // already-expired key keeps its original expiry (sent as undefined ->
  // backend leaves the column untouched); a fresh future expiry is validated
  // here and forwarded. The picker has :clearable="false" because the backend
  // has no clear-sentinel for timestamps — clearing would silently no-op.
  const expiryChanged = form.expires_at !== initialExpiresAt.value
  if (expiryChanged && form.expires_at != null && form.expires_at <= Date.now()) {
    message.error(t('apiKeys.expiresMustBeFuture'))
    return
  }
  saving.value = true
  try {
    // Numeric limits: empty -> 0 sentinel -> backend clears the column.
    // Members may only send label/remark/expiry — the backend rejects any
    // restricted field outright, so their payload must omit the rest.
    const input: UpdateAPIKeyInput = authStore.isAdmin
      ? {
          owner_label: form.owner_label,
          remark: form.remark,
          allow_all_models: form.allow_all_models,
          model_ids: form.model_ids,
          expires_at: expiryChanged && form.expires_at != null ? new Date(form.expires_at).toISOString() : undefined,
          rpm_limit: form.rpm_limit ?? 0,
          tpm_limit: form.tpm_limit ?? 0,
          concurrency_limit: form.concurrency_limit ?? 0,
          budget_limit_micros: form.budget_amount != null ? toMicros(form.budget_amount) : 0,
        }
      : {
          owner_label: form.owner_label,
          remark: form.remark,
          expires_at: expiryChanged && form.expires_at != null ? new Date(form.expires_at).toISOString() : undefined,
        }
    await store.update(props.apiKeyId, input)
    emit('saved')
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    saving.value = false
  }
}
</script>

<style scoped lang="less">
.full-width {
  width: 100%;
}

/* Group the rate/budget caps under a labelled divider so they read as one
   "limits" block, set apart from the identity and scope fields above. */
.limit-section {
  margin-top: 4px;
  padding-top: 16px;
  border-top: 1px solid var(--n-divider-color, rgba(0, 0, 0, 0.09));
}

.limit-section__label {
  margin-bottom: 8px;
  font-size: 13px;
  color: var(--n-text-color-3, rgba(0, 0, 0, 0.45));
}

/* Each limit holds at most a handful of digits, so a full-width row per field
   stretches the modal needlessly. Lay them two per row; each control fills its
   own cell. */
.limit-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  column-gap: 16px;
}

@media (max-width: @mobile-breakpoint) {
  .limit-grid {
    grid-template-columns: 1fr;
  }
}

.loading-row {
  padding: var(--space-4) 0;
  color: var(--color-text-muted);
  min-height: 510px;
}
</style>
