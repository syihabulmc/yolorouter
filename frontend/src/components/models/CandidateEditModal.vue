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
      <n-form-item>
        <template #label>
          <HelpLabel :tip="t('models.statusEnabled_tip')">{{ t('models.statusEnabled') }}</HelpLabel>
        </template>
        <n-switch v-model:value="form.enabled" />
      </n-form-item>
    </n-form>

    <!-- Only rendered once there is something to report: while idle the modal
         stays a plain form, matching the provider and key dialogs. -->
    <div v-if="submitting || report" class="probe-section">
      <div v-if="submitting" class="probe-section__pending">
        <n-spin size="small" />
        <div class="probe-section__pending-text">
          <div>{{ t('models.probing') }}</div>
          <div class="probe-section__hint">{{ t('models.probingHint') }}</div>
        </div>
      </div>
      <template v-else-if="report">
        <div class="probe-section__label">{{ t('models.probeResultTitle') }}</div>
        <div v-for="row in probeRows" :key="row.label" class="probe-row">
          <span class="probe-row__icon" :class="`probe-row__icon--${row.tone}`">{{ row.icon }}</span>
          <span class="probe-row__label">{{ row.label }}</span>
          <span class="probe-row__verdict" :class="`probe-row__verdict--${row.tone}`">{{ row.verdict }}</span>
        </div>
        <n-alert v-if="alert" :type="alert.type" style="margin-top: 12px">
          {{ alert.text }}
        </n-alert>
      </template>
    </div>

    <template #footer>
      <n-space justify="end">
        <!-- Once the candidate is stored, the only remaining action is to close:
             re-submitting would hit the one-candidate-per-provider constraint and
             report "provider already used" for a row this modal just saved. -->
        <template v-if="persisted">
          <n-button type="primary" @click="onUpdateShow(false)">{{ t('models.done') }}</n-button>
        </template>
        <template v-else>
          <n-button :disabled="submitting" @click="onUpdateShow(false)">{{ t('models.cancel') }}</n-button>
          <n-tooltip v-if="basicFailedBlockingSave" trigger="hover" placement="top">
            <template #trigger>
              <n-button :loading="savingDisabled" :disabled="submitting" @click="onSaveAnywayDisabled">
                {{ t('models.saveAnywayDisabled') }}
              </n-button>
            </template>
            {{ t('models.saveAnywayDisabled_tip') }}
          </n-tooltip>
          <n-button type="primary" :loading="submitting" :disabled="savingDisabled" @click="onSave">
            {{ basicFailedBlockingSave ? t('models.retry') : t('models.save') }}
          </n-button>
        </template>
      </n-space>
    </template>
  </n-modal>

  <NewProviderModal v-model:show="showNewProviderModal" />
</template>

<script setup lang="ts">
import { computed, h, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
// NSpin and NSwitch are not in main.ts's create() registry, so they must be
// imported explicitly or Vue renders them as inert unknown elements.
import {
  NButton,
  NSpin,
  NSwitch,
  NTooltip,
  useMessage,
  type FormInst,
  type FormRules,
  type MessageReactive,
} from 'naive-ui'
import { useModelsStore } from '../../store/models'
import { useProvidersStore } from '../../store/providers'
import { displayMessage } from '../../api/client'
import { providerModelNameRule, nonNegativePriceRule } from '../../utils/modelValidators'
import { capabilityState } from '../../utils/modelStatusDisplay'
import { testOutcomeI18nKey, TEST_OUTCOME_VERIFICATION_UNSUPPORTED } from '../../utils/testOutcomeDisplay'
import HelpLabel from '../HelpLabel.vue'
import NewProviderModal from '../providers/NewProviderModal.vue'
import type { CandidateTestReport, ModelCandidate, ProbeReport } from '../../api/models'

const CANDIDATE_STATUS_ENABLED = 1
const CANDIDATE_STATUS_DISABLED = 2

const props = defineProps<{
  show: boolean
  modelId: number
  // modelName is the outward model this candidate maps to, shown read-only so
  // the admin knows which model they are configuring — and, since a blank
  // provider model name defaults to it, which name a mapping falls back to.
  modelName?: string
  editingCandidate?: ModelCandidate | null
}>()
const emit = defineEmits<{ 'update:show': [boolean]; saved: []; retest: [number] }>()

const { t } = useI18n()
const message = useMessage()
const store = useModelsStore()
const providersStore = useProvidersStore()

const formRef = ref<FormInst | null>(null)
// submitting covers the probe-and-save round trip; savingDisabled covers the
// separate no-probe "save as disabled anyway" call, so each button spins on its
// own action instead of both reacting to one shared flag.
const submitting = ref(false)
const savingDisabled = ref(false)
const report = ref<CandidateTestReport | null>(null)
// persisted records that this modal has already written the candidate. It gates
// the footer down to a single close action, because a second submit would try to
// create the same provider mapping again and fail on the uniqueness constraint.
const persisted = ref(false)
// saveSeq is bumped whenever the dialog opens, so a save whose probe outlives its
// own opening can tell that its results are no longer wanted.
let saveSeq = 0

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
  enabled: true,
})

// The basic probe having failed while the admin asked to enable is the one state
// that blocks saving: nothing was stored, so the footer offers a retry and the
// disabled-anyway escape hatch instead of a plain save.
// The mapping was probed and rejected. Drives the explanation shown to the
// operator, independently of whether anything was stored.
const basicProbeFailed = computed(() => {
  const r = report.value
  return r !== null && r.basic.ran && !probePassed(r.basic)
})

// Nothing could be stored: enablement was asked for and the mapping does not
// work, so the create path wrote nothing. Drives the footer's retry / save-as-
// disabled escape hatch, which only makes sense before anything is persisted.
const basicFailedBlockingSave = computed(
  () => !persisted.value && form.enabled && basicProbeFailed.value,
)

const hasUnknownCapability = computed(() => {
  const r = report.value
  if (!r) return false
  return [r.streaming, r.function_calling].some((p) => p.ran && p.supported === null)
})

// One alert explaining the outcome, picked by what actually went wrong so the
// operator is never pointed at a fault that is not theirs to fix.
const alert = computed<{ type: 'error' | 'warning' | 'info'; text: string } | null>(() => {
  const r = report.value
  if (!r) return null
  if (!r.basic.ran) {
    // Nothing was probed — the provider has no usable key yet, which is a
    // different problem from a mapping that was tested and rejected.
    return { type: 'warning', text: t('models.probeNotTestedHint') }
  }
  if (basicProbeFailed.value) {
    // A protocol the backend cannot certify will never pass, however the
    // mapping is edited, so offering the usual "check the name, key and
    // address" advice would send the operator in circles.
    if (r.basic.outcome === TEST_OUTCOME_VERIFICATION_UNSUPPORTED) {
      return { type: 'warning', text: t('models.probeCannotCertifyHint') }
    }
    // Two different situations: nothing was stored (create, enable requested),
    // or the edit was stored but could not be left enabled.
    return {
      type: 'error',
      text: persisted.value ? t('models.probeFailedSavedDisabledHint') : t('models.probeBasicFailedHint'),
    }
  }
  if (hasUnknownCapability.value) return { type: 'info', text: t('models.probeUnconfirmedHint') }
  return null
})

function probePassed(p: ProbeReport): boolean {
  return p.ran && p.supported === true
}

type ProbeTone = 'supported' | 'unsupported' | 'unknown' | 'untested'

// One display row per probe. The basic probe is binary (it either proves the
// mapping works or it does not), while the capability probes carry the tri-state
// so an inconclusive result never reads as a proven "not supported".
const probeRows = computed(() => {
  const r = report.value
  if (!r) return []
  return [
    basicRow(r.basic),
    capabilityRow(t('models.supportsStreaming'), r.streaming),
    capabilityRow(t('models.supportsFunctionCalling'), r.function_calling),
  ]
})

function basicRow(p: ProbeReport) {
  const label = t('models.basicText')
  if (probePassed(p)) {
    return { label, icon: '✓', tone: 'supported' as ProbeTone, verdict: t('models.testPassed') }
  }
  // A probe that never ran must not be rendered as a failure. The server
  // reports this when it could not probe at all — no usable provider key yet —
  // and claiming "test failed" would send the operator looking for a fault in
  // the model name or credential that the probe never examined.
  if (!p.ran) {
    return { label, icon: '—', tone: 'untested' as ProbeTone, verdict: t('models.probeNotTested') }
  }
  return { label, icon: '✗', tone: 'unsupported' as ProbeTone, verdict: failureVerdict(p) }
}

function capabilityRow(label: string, p: ProbeReport) {
  if (!p.ran) {
    return { label, icon: '—', tone: 'untested' as ProbeTone, verdict: t('models.probeNotTested') }
  }
  switch (capabilityState(p.supported)) {
    case 'confirmed':
      return { label, icon: '✓', tone: 'supported' as ProbeTone, verdict: t('models.probeConfirmed') }
    case 'unsupported':
      return { label, icon: '✗', tone: 'unsupported' as ProbeTone, verdict: t('models.probeUnsupported') }
    default:
      return { label, icon: '?', tone: 'unknown' as ProbeTone, verdict: t('models.probeUnconfirmed') }
  }
}

// Names the specific upstream reason when one is known, so a wrong model name is
// distinguishable from a bad key or an unreachable address.
function failureVerdict(p: ProbeReport): string {
  if (p.outcome === null || p.outcome === undefined) return t('models.testFailed')
  return `${t('models.testFailed')}: ${t(`providers.${testOutcomeI18nKey(p.outcome)}`)}`
}

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
    // Invalidate any probe still in flight from a previous opening. Without this
    // a run the operator abandoned by closing the dialog would land its verdicts,
    // its persisted flag and even its auto-close on whichever candidate is open
    // when it finally resolves, up to 30 seconds later.
    saveSeq += 1
    submitting.value = false
    savingDisabled.value = false
    report.value = null
    persisted.value = false
    if (props.editingCandidate) {
      form.providerId = props.editingCandidate.provider_id
      form.providerModelName = props.editingCandidate.provider_model_name
      form.inputPrice = props.editingCandidate.input_price
      form.outputPrice = props.editingCandidate.output_price
      form.cacheWritePrice = props.editingCandidate.cache_write_price ?? undefined
      form.cacheReadPrice = props.editingCandidate.cache_read_price ?? undefined
      form.maxOutput = props.editingCandidate.max_output
      form.enabled = props.editingCandidate.management_status === CANDIDATE_STATUS_ENABLED
    } else {
      form.providerId = null
      form.providerModelName = ''
      form.inputPrice = 0
      form.outputPrice = 0
      form.cacheWritePrice = undefined
      form.cacheReadPrice = undefined
      form.maxOutput = 0
      form.enabled = true
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

function candidatePayload() {
  return {
    provider_model_name: form.providerModelName,
    input_price: form.inputPrice,
    output_price: form.outputPrice,
    cache_write_price: form.cacheWritePrice,
    cache_read_price: form.cacheReadPrice,
    max_output: form.maxOutput,
    management_status: form.enabled ? CANDIDATE_STATUS_ENABLED : CANDIDATE_STATUS_DISABLED,
  }
}

// A run whose every probe came back affirmative needs no acknowledgement — the
// modal closes and a toast reports the outcome. Anything less stays on screen so
// the operator cannot miss it.
function reportIsAllClear(r: CandidateTestReport | null): boolean {
  if (!r) return true
  return probePassed(r.basic) && probePassed(r.streaming) && probePassed(r.function_calling)
}

// Called once the candidate is stored. An all-clear run closes the modal with a
// toast; anything less keeps it open so the verdicts are actually read, but the
// row is already saved, so persisted flips and the footer becomes a single
// close action rather than a save that would collide with itself.
function finishSave(enabled: boolean, r: CandidateTestReport | null, editingId: number | null) {
  persisted.value = true
  emit('saved')
  if (!reportIsAllClear(r)) return
  onUpdateShow(false)
  // A null report means the edit could not have invalidated the stored verdicts
  // — a price or max-output change on a candidate whose target and enablement
  // both stayed put — so nothing was probed. Saying "saved and enabled" here
  // would imply a verification that never happened, so the outcome is reported
  // honestly and a retest is offered for an operator who wants one anyway.
  // Disabling also skips the probe, but there the plain "saved as disabled"
  // already tells the whole story and a retest nudge would just be noise.
  if (r === null && enabled && editingId !== null) {
    toastSavedWithoutRetest(editingId)
    return
  }
  message.success(enabled ? t('models.savedEnabled') : t('models.savedDisabled'))
}

function toastSavedWithoutRetest(candidateId: number) {
  // The click handler needs to dismiss the toast it lives inside, so the handle
  // is held in a box the closure reads at click time rather than being captured
  // by name — message.info has not returned yet while render is being built.
  const toast: { handle: MessageReactive | null } = { handle: null }
  toast.handle = message.info(t('models.savedNoRetest'), {
    duration: 8000,
    // Named msg rather than props so it does not shadow the component's props.
    render: (msg) =>
      h('div', { style: 'display: flex; align-items: center; gap: 12px' }, [
        h('span', null, msg.content as string),
        h(
          NButton,
          {
            text: true,
            type: 'primary',
            size: 'small',
            onClick: () => {
              toast.handle?.destroy()
              emit('retest', candidateId)
            },
          },
          { default: () => t('models.retestNow') },
        ),
      ]),
  })
}

async function onSave() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  report.value = null
  submitting.value = true
  const seq = saveSeq
  const enabled = form.enabled
  const editingId = props.editingCandidate?.id ?? null
  try {
    if (editingId !== null) {
      const result = await store.updateCandidate(props.modelId, editingId, candidatePayload())
      if (seq !== saveSeq) return
      report.value = result.report
      // The edit always persists — a probe result only decides enablement — so
      // this counts as saved regardless of how the probes turned out.
      finishSave(enabled, result.report, editingId)
      return
    }
    const result = await store.testAndCreateCandidate(props.modelId, {
      provider_id: form.providerId!,
      ...candidatePayload(),
    })
    if (seq !== saveSeq) return
    report.value = result.report
    if (result.created) finishSave(enabled, result.report, null)
  } catch (err) {
    if (seq !== saveSeq) return
    message.error(displayMessage(err, t))
  } finally {
    // Only the run that still owns the dialog may clear the spinner; a superseded
    // run doing so would unstick a spinner that belongs to the current save.
    if (seq === saveSeq) submitting.value = false
  }
}

// The escape hatch after a failed probe run: store the mapping disabled without
// probing again. The verdicts just shown are deliberately not sent along — a
// client able to assert its own verification status could create a candidate
// that reads as verified and then enable it through the status endpoint.
async function onSaveAnywayDisabled() {
  // Validated like the primary save: this path writes the same fields, so
  // skipping it would let an invalid form through the side door.
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  savingDisabled.value = true
  const seq = saveSeq
  try {
    if (props.editingCandidate) {
      await store.updateCandidate(props.modelId, props.editingCandidate.id, {
        ...candidatePayload(),
        management_status: CANDIDATE_STATUS_DISABLED,
      })
    } else {
      await store.createCandidate(props.modelId, {
        provider_id: form.providerId!,
        ...candidatePayload(),
        management_status: CANDIDATE_STATUS_DISABLED,
      })
    }
    if (seq !== saveSeq) return
    persisted.value = true
    emit('saved')
    message.success(t('models.savedDisabled'))
    onUpdateShow(false)
  } catch (err) {
    if (seq !== saveSeq) return
    message.error(displayMessage(err, t))
  } finally {
    if (seq === saveSeq) savingDisabled.value = false
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

/* Separate the probe results from the form above with a hairline rule, so the
   three verdicts read as one reported outcome rather than floating text. */
.probe-section {
  margin-top: 4px;
  padding-top: 16px;
  border-top: 1px solid var(--n-divider-color, rgba(0, 0, 0, 0.09));
}

.probe-section__label {
  margin-bottom: 8px;
  font-size: 13px;
  color: var(--n-text-color-3, rgba(0, 0, 0, 0.45));
}

.probe-section__pending {
  display: flex;
  align-items: center;
  gap: 12px;
}

.probe-section__pending-text {
  font-size: 13px;
}

.probe-section__hint {
  margin-top: 2px;
  font-size: 12px;
  color: var(--n-text-color-3, rgba(0, 0, 0, 0.45));
}

.probe-row {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 3px 0;
  font-size: 13px;
}

.probe-row__icon {
  width: 14px;
  text-align: center;
  font-weight: 600;
}

.probe-row__label {
  min-width: 88px;
}

.probe-row__verdict {
  color: var(--n-text-color-3, rgba(0, 0, 0, 0.45));
}

/* Hex rather than CSS variables so the tones hold up in both themes without
   depending on which naive-ui variables happen to be in scope here. */
.probe-row__icon--supported,
.probe-row__verdict--supported {
  color: #18a058;
}

.probe-row__icon--unsupported,
.probe-row__verdict--unsupported {
  color: #d03050;
}

/* An inconclusive probe is a warning, not a failure: routing still happens and
   the remedy is to retest, so it must not look like a proven "not supported". */
.probe-row__icon--unknown,
.probe-row__verdict--unknown {
  color: #f0a020;
}

.probe-row__icon--untested,
.probe-row__verdict--untested {
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
