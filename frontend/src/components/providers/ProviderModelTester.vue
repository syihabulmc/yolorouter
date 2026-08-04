<!-- frontend/src/components/providers/ProviderModelTester.vue
     Shared test-model picker + connection tester, used by the create-provider
     and add/edit-key dialogs. It fetches the upstream model catalogue for the
     entered credential (falling back to free-text entry when the upstream
     exposes no list), lets the admin pick or type a model, runs a one-shot
     connection test, and shows a categorized result with the raw upstream
     error available on demand. Requires baseUrl + apiKey (plaintext) to be
     present before its buttons enable; the parent owns those fields. -->
<template>
  <div class="model-tester">
    <n-form-item path="testModel" :rule="testModelValidationRule">
      <template #label>
        <HelpLabel :tip="t('providers.testModel_tip')">{{ t('providers.testModel') }}</HelpLabel>
      </template>
      <div class="tester-row" :class="{ 'tester-row--mobile': isMobile }">
        <!-- Desktop: the original filterable + tag select (pick from the fetched
             catalogue or type a model not in the list). -->
        <NSelect
          v-if="!isMobile"
          :value="value"
          filterable
          tag
          clearable
          :options="modelOptions"
          :placeholder="t('providers.testModelSelectPlaceholder')"
          class="model-select"
          @update:value="onModelChange"
        />

        <!-- Mobile with a fetched catalogue: a trigger that raises the shared
             bottom-sheet picker. -->
        <FilterSelectField
          v-else-if="fetchedModels.length"
          :label="t('providers.testModel')"
          :value="value || null"
          :options="modelOptions"
          :placeholder="t('providers.testModelSelectPlaceholder')"
          width="100%"
          size="medium"
          class="model-select"
          :clearable="false"
          @update:value="onModelChange"
        />

        <!-- Mobile with no catalogue (upstream exposes no list, or not fetched
             yet): a plain input to type the model name directly. -->
        <n-input
          v-else
          :value="value"
          :placeholder="t('providers.testModelSelectPlaceholder')"
          class="model-select"
          @update:value="onModelChange"
        />

        <div class="tester-actions">
          <n-button :loading="fetching" :disabled="!canProbe" @click="onFetchModels">
            {{ t('providers.fetchModels') }}
          </n-button>
          <n-button type="primary" ghost :loading="testing" :disabled="!canTest" @click="onTest">
            {{ t('providers.testConnection') }}
          </n-button>
        </div>
      </div>
    </n-form-item>

    <n-alert v-if="testOutcome !== null" :type="testOk ? 'success' : 'error'" :bordered="false" class="result">
      <div class="result-title">{{ resultTitle }}</div>
      <div v-if="!testOk && resultHint" class="result-hint">{{ resultHint }}</div>
      <div v-if="testDetail" class="detail-toggle">
        <n-button text size="tiny" @click="showDetail = !showDetail">
          {{ showDetail ? t('providers.errorDetailHide') : t('providers.errorDetailShow') }}
        </n-button>
        <pre v-if="showDetail" class="detail-text">{{ testDetail }}</pre>
      </div>
    </n-alert>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NSelect, useMessage, type FormItemRule } from 'naive-ui'
import { useProvidersStore } from '../../store/providers'
import { displayMessage } from '../../api/client'
import { testOutcomeI18nKey, testOutcomeLabel, isTestSuccess } from '../../utils/testOutcomeDisplay'
import { testModelRule } from '../../utils/providerValidators'
import HelpLabel from '../HelpLabel.vue'
import FilterSelectField from '../common/FilterSelectField.vue'
import { useIsMobile } from '../../composables/useIsMobile'

const props = defineProps<{
  value: string
  baseUrl: string
  apiKey: string
  providerType: string
}>()
const emit = defineEmits<{ 'update:value': [string] }>()

const { t, te } = useI18n()
const message = useMessage()
const store = useProvidersStore()

const isMobile = useIsMobile()

// fetchedModels is the upstream catalogue for the current credential; empty
// until the admin fetches it (or when the upstream exposes no list).
const fetchedModels = ref<string[]>([])
const fetching = ref(false)
const testing = ref(false)
// testOutcome holds the last test's outcome int (backend TestOutcome enum),
// null until a test runs; testDetail is the raw upstream error, shown on
// demand under the categorized result.
const testOutcome = ref<number | null>(null)
const testDetail = ref('')
const showDetail = ref(false)

// The full testModel rule set (required + max-length), mirroring the create
// request's binding. n-form-item's :rule accepts an array, so both apply.
const testModelValidationRule = computed<FormItemRule[]>(() => testModelRule(t))

// A fetch/test needs a destination and a credential; without both the buttons
// stay disabled so the admin isn't left guessing why a call did nothing.
const canProbe = computed(() => props.baseUrl.trim() !== '' && props.apiKey.trim() !== '')
const canTest = computed(() => canProbe.value && props.value.trim() !== '')

// Options merge the fetched catalogue with the current value, so a manually
// typed (tag) model still renders as the selected option before any fetch.
const modelOptions = computed(() => {
  const names = new Set(fetchedModels.value)
  if (props.value) names.add(props.value)
  return Array.from(names).map((m) => ({ label: m, value: m }))
})

const testOk = computed(() => testOutcome.value !== null && isTestSuccess(testOutcome.value))
const resultTitle = computed(() => {
  if (testOutcome.value === null) return ''
  if (testOk.value) return t('providers.testSuccess')
  return `${t('providers.testFailed')}: ${testOutcomeLabel(t, testOutcome.value)}`
})
// Actionable one-liner for the failure category (e.g. "check the key"), kept
// separate from the raw upstream detail below it. Not every category has a
// hint, so gate on te() rather than rendering a raw key.
const resultHint = computed(() => {
  if (testOutcome.value === null || testOk.value) return ''
  const key = `providers.${testOutcomeI18nKey(testOutcome.value)}_hint`
  return te(key) ? t(key) : ''
})

function clearResult() {
  testOutcome.value = null
  testDetail.value = ''
  showDetail.value = false
}

// credentialSnapshot captures the destination + credential a fetch/test was
// issued against, so a response that resolves after those inputs have changed
// can detect it is stale and drop itself instead of overwriting current state.
// (A test additionally snapshots the model, checked separately.) Comparing the
// values themselves — rather than a counter — is intentional: if an input is
// edited away and back to the same value mid-flight, the response IS valid for
// the current inputs, so accepting it is correct.
function credentialSnapshot() {
  return { baseUrl: props.baseUrl, apiKey: props.apiKey, providerType: props.providerType }
}
function credentialMatches(snap: ReturnType<typeof credentialSnapshot>): boolean {
  return snap.baseUrl === props.baseUrl && snap.apiKey === props.apiKey && snap.providerType === props.providerType
}

// A credential/destination change invalidates the shown result AND the fetched
// catalogue — both belonged to the old credential. Any in-flight fetch/test
// drops its own late response via the snapshot check above.
watch([() => props.baseUrl, () => props.apiKey, () => props.providerType], () => {
  fetchedModels.value = []
  clearResult()
})
// Changing the selected model only invalidates the shown test result; the
// catalogue is credential-scoped, not model-scoped, so it stays.
watch(() => props.value, clearResult)

function onModelChange(v: string | null) {
  emit('update:value', v ?? '')
}

async function onFetchModels() {
  const snap = credentialSnapshot()
  // A catalogue is credential-scoped; it is stale if the destination or
  // credential changed while the request was in flight. Checked on both the
  // success and error paths, so a late response never overwrites current state.
  const stale = () => !credentialMatches(snap)
  fetching.value = true
  try {
    const result = await store.listModelsPreview(snap.baseUrl, snap.apiKey, snap.providerType)
    if (stale()) return
    if (!isTestSuccess(result.outcome)) {
      // The catalogue call itself failed (bad key / unreachable / no list) —
      // surface the category and let the admin fall back to typing a model.
      message.error(`${t('providers.fetchModelsFailed')}: ${testOutcomeLabel(t, result.outcome)}`)
      return
    }
    fetchedModels.value = result.models
    if (result.models.length === 0) {
      message.info(t('providers.fetchModelsEmpty'))
      return
    }
    message.success(t('providers.fetchModelsSuccess', { count: result.models.length }))
  } catch (err) {
    if (stale()) return
    message.error(displayMessage(err, t))
  } finally {
    fetching.value = false
  }
}

async function onTest() {
  const snap = credentialSnapshot()
  const model = props.value
  // A test result describes one credential+model pair; it is stale if either
  // changed mid-flight. Checked on both success and error paths.
  const stale = () => !credentialMatches(snap) || model !== props.value
  testing.value = true
  clearResult()
  try {
    const result = await store.testKeyPreview(snap.baseUrl, snap.apiKey, model, snap.providerType)
    if (stale()) return
    testOutcome.value = result.outcome
    testDetail.value = result.detail
  } catch (err) {
    if (stale()) return
    message.error(displayMessage(err, t))
  } finally {
    testing.value = false
  }
}
</script>

<style scoped>
.tester-row {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
}
.tester-actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}
/* On a phone the select takes the full width on its own line and the two
   buttons wrap onto a second row, each splitting the width so both stay
   comfortably tappable. */
.tester-row--mobile {
  flex-direction: column;
  align-items: stretch;
}
.tester-row--mobile .tester-actions {
  width: 100%;
}
.tester-row--mobile .tester-actions > * {
  flex: 1;
}
.model-select {
  min-width: 140px;
  flex: 1;
}
.result {
  margin-top: 4px;
  margin-bottom: 12px;
}
.result-hint {
  margin-top: 4px;
  font-size: 13px;
  opacity: 0.75;
}
.detail-toggle {
  margin-top: 8px;
}
.detail-text {
  margin: 6px 0 0;
  padding: 8px;
  border-radius: 4px;
  background: var(--color-bg-soft);
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
