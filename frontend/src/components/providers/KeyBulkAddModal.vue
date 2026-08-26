<!-- frontend/src/components/providers/KeyBulkAddModal.vue
     Bulk-add flow for the providers detail page. One textarea, one line
     per `label:key` (label optional). Shared test_model + enabled toggle.
     The parser (utils/providerValidators.parseBulkKeyLines) runs on
     every input so the operator sees line-numbered errors BEFORE the
     submit round trip — failing the click is a confirmation, not a
     surprise. -->
<template>
  <ModalDrawer
    v-model:show="showModel"
    :title="t('providers.addBulkKey')"
    max-width="560px"
    :mask-closable="false"
    :close-on-esc="false"
    :confirm-text="t('providers.save')"
    :cancel-text="t('providers.cancel')"
    :loading="submitting"
    :back-label="t('common.back')"
    @confirm="onSubmit"
  >
    <n-form
      require-mark-placement="left"
      :model="form"
      class="provider-form-dense"
      label-placement="left"
      label-align="right"
      label-width="auto"
    >
      <n-form-item>
        <template #label>
          <HelpLabel :tip="t('providers.bulkKeyInputTip')">{{ t('providers.bulkKeyInputLabel') }}</HelpLabel>
        </template>
        <n-input
          v-model:value="form.bulk"
          type="textarea"
          :rows="8"
          :placeholder="bulkPlaceholder"
          :status="hasBlockingIssues ? 'error' : undefined"
        />
        <div class="bulk-summary">
          <span :class="{ 'bulk-summary--error': hasBlockingIssues }">{{ summaryText }}</span>
        </div>
      </n-form-item>
      <ProviderModelTester
        v-model:value="form.testModel"
        :base-url="baseUrl"
        :api-key="firstValidPlaintext"
        :provider-type="providerType"
      />
      <n-form-item>
        <template #label>
          <HelpLabel :tip="t('providers.statusEnabled_tip')">{{ t('providers.statusEnabled') }}</HelpLabel>
        </template>
        <n-switch v-model:value="form.enabled" />
      </n-form-item>
    </n-form>
  </ModalDrawer>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage } from 'naive-ui'
import { displayMessage } from '../../api/client'
import { useProvidersStore } from '../../store/providers'
import { parseBulkKeyLines } from '../../utils/providerValidators'
import HelpLabel from '../HelpLabel.vue'
import ModalDrawer from '../common/ModalDrawer.vue'
import ProviderModelTester from './ProviderModelTester.vue'

const props = defineProps<{
  show: boolean
  providerId: number
  baseUrl: string
  providerType: string
  destinationCount: number
}>()
const emit = defineEmits<{
  'update:show': [boolean]
  saved: [created: number, failed: number]
}>()

const showModel = computed({
  get: () => props.show,
  set: (v) => emit('update:show', v),
})

const { t } = useI18n()
const message = useMessage()
const store = useProvidersStore()

const submitting = ref(false)
const form = reactive({ bulk: '', testModel: '', enabled: false })

watch(
  () => props.show,
  (visible) => {
    if (!visible) return
    form.bulk = ''
    form.testModel = ''
    form.enabled = false
  },
)

const parsed = computed(() => parseBulkKeyLines(form.bulk))
const hasBlockingIssues = computed(
  () => parsed.value.errors.length > 0 || parsed.value.duplicates.length > 0,
)
const summaryText = computed(() => {
  if (form.bulk.trim() === '') return t('providers.bulkKeySummaryEmpty')
  return t('providers.bulkKeySummary', {
    valid: parsed.value.valid.length,
    errors: parsed.value.errors.length,
    duplicates: parsed.value.duplicates.length,
  })
})
// ProviderModelTester needs a real plaintext to verify the test model
// against; the first valid row's key stands in for that, so the test
// picker sees the same destination the batch will hit.
const firstValidPlaintext = computed(() => parsed.value.valid[0]?.plaintext ?? '')
const bulkPlaceholder = computed(() => t('providers.bulkKeyPlaceholder'))

async function onSubmit() {
  if (parsed.value.valid.length === 0) {
    message.warning(t('providers.bulkKeySummaryEmpty'))
    return
  }
  if (hasBlockingIssues.value) {
    message.warning(
      t('providers.bulkSubmitWithIssues', { count: parsed.value.errors.length + parsed.value.duplicates.length }),
    )
    return
  }
  submitting.value = true
  try {
    const managementStatus = form.enabled ? 1 : 2
    const items = parsed.value.valid.map((row) => ({
      label: row.label,
      plaintext: row.plaintext,
      test_model: form.testModel,
      management_status: managementStatus,
    }))
    const result = await store.bulkCreateKeys(props.providerId, items, props.destinationCount)
    message.success(t('providers.bulkResultToast', { created: result.created, failed: result.failed }))
    emit('saved', result.created, result.failed)
    showModel.value = false
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
.bulk-summary {
  margin-top: 4px;
  font-size: 12px;
  color: var(--text-color-3, #888);
}
.bulk-summary--error {
  color: var(--error-color, #d03050);
}
</style>
