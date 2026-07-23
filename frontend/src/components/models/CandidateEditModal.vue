<!-- frontend/src/components/models/CandidateEditModal.vue -->
<template>
  <n-modal
    :show="show"
    preset="card"
    :title="editingCandidate ? t('models.editCandidate') : t('models.addCandidate')"
    style="max-width: 520px"
    :mask-closable="false"
    :close-on-esc="false"
    @update:show="onUpdateShow"
  >
    <div v-if="modelName" class="outward-model">
      <span class="outward-model__label">{{ t('models.name') }}</span>
      <span class="outward-model__value">{{ modelName }}</span>
    </div>
    <n-form require-mark-placement="left" ref="formRef" :model="form" :rules="rules">
      <n-form-item v-if="!editingCandidate" path="providerId">
        <template #label>
          <div class="label-row">
            <HelpLabel :tip="t('models.provider_tip')">{{ t('models.provider') }}</HelpLabel>
            <n-button text type="primary" size="tiny" @click="openNewProviderModal">
              {{ t('providers.createButton') }}
            </n-button>
          </div>
        </template>
        <n-select
          v-model:value="form.providerId"
          :options="providerOptions"
          :placeholder="t('models.provider')"
          style="width: 100%"
        />
      </n-form-item>
      <n-form-item path="providerModelName">
        <template #label>
          <HelpLabel :tip="t('models.providerModelName_tip')">{{ t('models.providerModelName') }}</HelpLabel>
        </template>
        <n-select
          v-model:value="providerModelName"
          :options="modelOptions"
          :loading="loadingModels"
          filterable
          tag
          clearable
          :placeholder="t('models.providerModelNameHint')"
        />
      </n-form-item>
      <div class="price-grid">
      <n-form-item path="inputPrice">
        <template #label>
          <HelpLabel :tip="t('models.inputPrice_tip')">{{ t('models.inputPrice') }}</HelpLabel>
        </template>
        <n-input-number v-model:value="form.inputPrice" :min="0" style="width: 100%" />
      </n-form-item>
      <n-form-item path="outputPrice">
        <template #label>
          <HelpLabel :tip="t('models.outputPrice_tip')">{{ t('models.outputPrice') }}</HelpLabel>
        </template>
        <n-input-number v-model:value="form.outputPrice" :min="0" style="width: 100%" />
      </n-form-item>
      <n-form-item>
        <template #label>
          <HelpLabel :tip="t('models.cacheWritePrice_tip')">{{ t('models.cacheWritePrice') }}</HelpLabel>
        </template>
        <n-input-number v-model:value="form.cacheWritePrice" :min="0" style="width: 100%" />
      </n-form-item>
      <n-form-item>
        <template #label>
          <HelpLabel :tip="t('models.cacheReadPrice_tip')">{{ t('models.cacheReadPrice') }}</HelpLabel>
        </template>
        <n-input-number v-model:value="form.cacheReadPrice" :min="0" style="width: 100%" />
      </n-form-item>
      <n-form-item>
        <template #label>
          <HelpLabel :tip="t('models.maxOutput_tip')">{{ t('models.maxOutput') }}</HelpLabel>
        </template>
        <n-input-number v-model:value="form.maxOutput" :min="0" style="width: 100%" />
      </n-form-item>
      </div>
    </n-form>

    <div class="test-section">
      <div class="test-section__label">
        <HelpLabel :tip="t('models.testMapping_tip')">{{ t('models.testMapping') }}</HelpLabel>
      </div>
      <n-space :size="8">
        <n-tooltip trigger="hover" placement="top">
          <template #trigger>
            <n-button size="small" :loading="testing === 'basic'" @click="onTest('basic')">{{ t('models.testBasic') }}</n-button>
          </template>
          {{ t('models.testBasic_tip') }}
        </n-tooltip>
        <n-tooltip trigger="hover" placement="top">
          <template #trigger>
            <n-button size="small" :loading="testing === 'streaming'" @click="onTest('streaming')">{{ t('models.testStreaming') }}</n-button>
          </template>
          {{ t('models.testStreaming_tip') }}
        </n-tooltip>
        <n-tooltip trigger="hover" placement="top">
          <template #trigger>
            <n-button size="small" :loading="testing === 'function_calling'" @click="onTest('function_calling')">{{ t('models.testFunctionCalling') }}</n-button>
          </template>
          {{ t('models.testFunctionCalling_tip') }}
        </n-tooltip>
      </n-space>
      <n-alert v-if="testResult" :type="testResult.ok ? 'success' : 'error'" style="margin-top: 12px">
        {{ testResultLabel }}
      </n-alert>
    </div>

    <template #footer>
      <n-space justify="end">
        <n-button @click="onUpdateShow(false)">{{ t('models.cancel') }}</n-button>
        <n-button :loading="submitting" @click="onSave(false)">{{ t('models.saveDisabled') }}</n-button>
        <n-button type="primary" :disabled="!basicTestPassed" :loading="submitting" @click="onSave(true)">
          {{ t('models.saveEnabled') }}
        </n-button>
      </n-space>
    </template>
  </n-modal>

  <NewProviderModal v-model:show="showNewProviderModal" />
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
// NTooltip is not in main.ts's create() registry, so it must be imported
// explicitly or Vue renders it as an inert unknown element.
import { NTooltip, useMessage, type FormInst, type FormRules } from 'naive-ui'
import { useModelsStore } from '../../store/models'
import { useProvidersStore } from '../../store/providers'
import { displayMessage } from '../../api/client'
import { providerModelNameRule, nonNegativePriceRule } from '../../utils/modelValidators'
import { candidateTestPassed, candidateTestResultText } from '../../utils/modelStatusDisplay'
import HelpLabel from '../HelpLabel.vue'
import NewProviderModal from '../providers/NewProviderModal.vue'
import type { ModelCandidate } from '../../api/models'

const props = defineProps<{
  show: boolean
  modelId: number
  // modelName is the outward model this candidate maps to, shown read-only so
  // the admin knows which model they are configuring — and, since a blank
  // provider model name defaults to it, which name a mapping falls back to.
  modelName?: string
  editingCandidate?: ModelCandidate | null
}>()
const emit = defineEmits<{ 'update:show': [boolean]; saved: [] }>()

const { t } = useI18n()
const message = useMessage()
const store = useModelsStore()
const providersStore = useProvidersStore()

const formRef = ref<FormInst | null>(null)
const submitting = ref(false)
const testing = ref<'basic' | 'streaming' | 'function_calling' | null>(null)
// testType is tracked so basicTestPassed can tell a passing BASIC test apart
// from a passing streaming/function_calling test — only the basic test gates
// enablement. outcome is the failure reason for the result alert (carried for
// both the new-mapping and edit branches).
const testResult = ref<{ ok: boolean; outcome?: number | null; testType: 'basic' | 'streaming' | 'function_calling' } | null>(null)
// basicTestPassed gates the "save and enable" button. It is satisfied by the
// candidate's stored basic verification, or by a fresh in-modal BASIC test
// pass — NOT by a streaming/function_calling pass, which does not imply the
// basic mapping works (the server refuses to enable an unverified candidate,
// so counting those would enable the button then hit a rejection). UX gate
// only; the server independently re-checks.
const basicTestPassed = computed(
  () =>
    props.editingCandidate?.verification_status === 1 ||
    (testResult.value?.testType === 'basic' && testResult.value.ok),
)

// Result alert text: on a failed new-mapping test, append the specific
// outcome reason so a wrong model/bad key/unreachable address is
// distinguishable rather than a blanket "test failed".
const testResultLabel = computed(() => {
  const r = testResult.value
  if (!r) return ''
  return candidateTestResultText(t, r.ok, r.outcome)
})

const showNewProviderModal = ref(false)
let providerIdBeforeCreate = 0

const form = reactive({
  providerId: null as number | null,
  providerModelName: '',
  inputPrice: 0,
  outputPrice: 0,
  cacheWritePrice: undefined as number | undefined,
  cacheReadPrice: undefined as number | undefined,
  maxOutput: 0,
})

const providerOptions = computed(() =>
  providersStore.list.map((p) => ({ label: p.name, value: p.id, disabled: false })),
)

// Model-name picker: the catalogue is fetched lazily for the selected provider
// and merged with the current value so a value not in the catalogue (a custom
// tag, or an edited candidate's stored name) still renders. The field remains
// a free-text combobox (filterable + tag), so a failed/empty fetch degrades to
// manual entry rather than blocking the field.
const fetchedModels = ref<string[]>([])
const loadingModels = ref(false)
let modelFetchSeq = 0

const modelOptions = computed(() => {
  const names = new Set(fetchedModels.value)
  if (form.providerModelName) names.add(form.providerModelName)
  return Array.from(names, (m) => ({ label: m, value: m }))
})

// NSelect emits null when cleared; keep form.providerModelName a string and
// treat blank as null so the placeholder ("blank = use the model name itself")
// shows instead of an empty-string value.
const providerModelName = computed<string | null>({
  get: () => form.providerModelName || null,
  set: (value) => {
    form.providerModelName = value ?? ''
  },
})

async function loadProviderModels(providerId: number | null) {
  const seq = ++modelFetchSeq
  fetchedModels.value = []
  // Bumping seq above already invalidated any in-flight fetch, so its finally
  // can no longer clear the flag — reset it here or clearing the provider
  // while a fetch is pending would leave the picker spinning forever.
  if (!providerId) {
    loadingModels.value = false
    return
  }
  loadingModels.value = true
  try {
    const { models } = await providersStore.listModelsForProvider(providerId)
    // A newer fetch started while this was in flight — its result wins.
    if (seq !== modelFetchSeq) return
    fetchedModels.value = models
  } catch {
    // Silent by design: the catalogue is a convenience. On any failure the
    // field stays a free-text combobox so the admin can type the name.
  } finally {
    if (seq === modelFetchSeq) loadingModels.value = false
  }
}

// Reload the catalogue whenever the target provider changes — including when
// the show-watch seeds providerId (edit mode) or openNewProviderModal sets a
// freshly created provider.
watch(
  () => form.providerId,
  (id) => {
    void loadProviderModels(id)
  },
)

const rules: FormRules = {
  providerId: [{ required: true, type: 'number', message: t('models.fieldRequired'), trigger: ['change', 'blur'] }],
  providerModelName: providerModelNameRule(t),
  inputPrice: nonNegativePriceRule(t),
  outputPrice: nonNegativePriceRule(t),
}

watch(
  () => props.show,
  (visible) => {
    if (!visible) return
    testResult.value = null
    if (props.editingCandidate) {
      form.providerId = props.editingCandidate.provider_id
      form.providerModelName = props.editingCandidate.provider_model_name
      form.inputPrice = props.editingCandidate.input_price
      form.outputPrice = props.editingCandidate.output_price
      form.cacheWritePrice = props.editingCandidate.cache_write_price ?? undefined
      form.cacheReadPrice = props.editingCandidate.cache_read_price ?? undefined
      form.maxOutput = props.editingCandidate.max_output
    } else {
      form.providerId = null
      form.providerModelName = ''
      form.inputPrice = 0
      form.outputPrice = 0
      form.cacheWritePrice = undefined
      form.cacheReadPrice = undefined
      form.maxOutput = 0
      providersStore.fetchList()
    }
  },
)

function onUpdateShow(value: boolean) {
  emit('update:show', value)
}

function openNewProviderModal() {
  // NewProviderModal.vue only emits 'update:show' (an unused 'created'
  // emit was removed) — so instead of listening for a
  // creation event, capture the highest existing provider id, then diff
  // against the refetched list once the modal closes.
  providerIdBeforeCreate = providersStore.list.reduce((max, p) => Math.max(max, p.id), 0)
  showNewProviderModal.value = true
}

watch(showNewProviderModal, async (visible) => {
  if (visible) return
  await providersStore.fetchList()
  const created = providersStore.list.find((p) => p.id > providerIdBeforeCreate)
  if (created) form.providerId = created.id
})

async function onTest(testType: 'basic' | 'streaming' | 'function_calling') {
  // providerModelName is optional — a blank value defaults to the model's
  // own name server-side (see modelValidators.ts's providerModelNameRule).
  if (!form.providerId) {
    message.error(t('models.fieldRequired'))
    return
  }
  testing.value = testType
  try {
    if (props.editingCandidate) {
      const result = await store.testCandidate(props.modelId, props.editingCandidate.id, testType)
      testResult.value = { ok: candidateTestPassed(testType, result), outcome: result.last_test_result, testType }
    } else {
      const result = await store.testMapping(props.modelId, form.providerId, form.providerModelName, testType)
      testResult.value = { ok: result.outcome === 0, outcome: result.outcome, testType }
    }
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    testing.value = null
  }
}

async function onSave(enable: boolean) {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  submitting.value = true
  try {
    if (props.editingCandidate) {
      await store.updateCandidate(props.modelId, props.editingCandidate.id, {
        provider_model_name: form.providerModelName,
        input_price: form.inputPrice,
        output_price: form.outputPrice,
        cache_write_price: form.cacheWritePrice,
        cache_read_price: form.cacheReadPrice,
        max_output: form.maxOutput,
      })
      // updateCandidate only persists fields — it never changes
      // management_status — so "save and enable" for an existing candidate
      // must flip the status explicitly (the create path below does this via
      // its own management_status field). The server refuses to enable an
      // unverified candidate, surfacing as an error the caller shows.
      if (enable) {
        await store.setCandidateStatus(props.modelId, props.editingCandidate.id, true)
      }
    } else {
      await store.createCandidate(props.modelId, {
        provider_id: form.providerId!,
        provider_model_name: form.providerModelName,
        input_price: form.inputPrice,
        output_price: form.outputPrice,
        cache_write_price: form.cacheWritePrice,
        cache_read_price: form.cacheReadPrice,
        max_output: form.maxOutput,
        management_status: enable ? 1 : 2,
      })
    }
    emit('saved')
    onUpdateShow(false)
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
/* Read-only context: which outward model this candidate is being mapped to.
   A blank provider model name defaults to this, so it doubles as the fallback
   reference the admin needs when choosing the provider-side model. */
.outward-model {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 20px;
  padding: 10px 12px;
  border-radius: 6px;
  background: var(--n-color-embedded, rgba(0, 0, 0, 0.03));
}

.outward-model__label {
  font-size: 13px;
  color: var(--n-text-color-3, rgba(0, 0, 0, 0.45));
}

.outward-model__value {
  font-weight: 600;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}

/* Keep the "new provider" shortcut on the label row so the select below can
   use the full modal width, matching every other field. */
.label-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
}

/* Separate the mapping-test actions from the form above with a hairline rule
   and a small caption, so the three test buttons read as one grouped step
   rather than floating controls. */
.test-section {
  margin-top: 4px;
  padding-top: 16px;
  border-top: 1px solid var(--n-divider-color, rgba(0, 0, 0, 0.09));
}

.test-section__label {
  margin-bottom: 8px;
  font-size: 13px;
  color: var(--n-text-color-3, rgba(0, 0, 0, 0.45));
}

/* Numeric fields (prices, max output) hold at most a handful of digits, so a
   full-width row each wastes horizontal space and stretches the modal. Lay
   them out two per row; each control fills its own cell. The select fields
   above stay full width. */
.price-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  column-gap: 16px;
}
</style>
