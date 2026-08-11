<!-- frontend/src/views/providers/ProviderDetailPage.vue -->
<template>
  <div class="common-page" v-if="provider">
    <PageHeader class="actions-placeholder" :eyebrow="t('providers.eyebrow')" :title="provider.name" :description="provider.base_url">
      <template #actions>
        <template v-if="!isMobile">
          <n-button size="small" @click="showEditProvider = true">{{ t('providers.editProvider') }}</n-button>
          <n-button size="small" @click="router.push(`/costs/providers/${provider.id}`)">
            {{ t('costs.detail.viewCost') }}
          </n-button>
          <n-button size="small" @click="onToggleProviderStatus">
            {{ provider.management_status === 1 ? t('providers.statusDisabled') : t('providers.statusEnabled') }}
          </n-button>
        </template>

        <ResponsiveDropdown
          v-else
          trigger="click"
          placement="bottom-end"
          :trigger-text="t('common.actions')"
          :loading="testingAll"
          :options="headerActionOptions"
          @select="onHeaderAction"
        />
      </template>
    </PageHeader>

    <n-tabs v-model:value="activeTab" type="line" animated>
      <n-tab-pane name="keys" :tab="t('providers.tabKeys')">
        <div class="keys-toolbar">
          <span v-if="pendingCount !== null" class="keys-toolbar__count">
            {{ t('providers.testAllPendingCount', { count: pendingCount }) }}
          </span>
          <n-space v-if="!isMobile">
            <n-button @click="showAddKey = true">
              <template #icon><Plus :size="16" /></template>
              {{ t('providers.addKey') }}
            </n-button>
            <n-button type="primary" :loading="testingAll" @click="onTestAll">
              <template #icon><PlayCircle :size="16" /></template>
              {{ t('providers.testAllButton') }}
            </n-button>
          </n-space>
        </div>

        <div class="data-table-wrapper">
          <ResponsiveDataTable
            :columns="keyColumns"
            :data="provider.keys"
            :loading="testingAll"
            :scroll-x="930"
            :row-key="(row: ProviderKey) => row.id"
            :pagination="keysPagination"
          />
        </div>

        <n-alert v-if="batchSummary" type="info" class="summary">{{ batchSummary }}</n-alert>
      </n-tab-pane>

      <n-tab-pane name="models" :tab="t('providers.tabModels')">
        <EmptyState v-if="modelsStore.error" :title="t('common.networkError')" />
        <EmptyState v-else-if="!modelsStore.loading && linkedModelRows.length === 0" :title="t('providers.modelsEmpty')" />
        <div v-else class="data-table-wrapper">
          <ResponsiveDataTable
            :columns="modelColumns"
            :data="linkedModelRows"
            :loading="modelsStore.loading"
            :scroll-x="480"
            :row-key="(row: LinkedModelRow) => row.candidateId"
            :pagination="modelsPagination"
          />
        </div>
      </n-tab-pane>
    </n-tabs>

    <KeyEditModal
      v-model:show="showAddKey"
      :provider-id="provider.id"
      :base-url="provider.base_url"
      :provider-type="provider.provider_type"
      :destination-count="destinationCount"
      @saved="reload"
    />
    <KeyEditModal
      v-model:show="showEditKey"
      :provider-id="provider.id"
      :base-url="provider.base_url"
      :provider-type="provider.provider_type"
      :destination-count="destinationCount"
      :editing-key="editingKey"
      @saved="onKeySaved"
    />
    <ProviderEditModal v-model:show="showEditProvider" :provider="provider" @updated="reload" />
  </div>
</template>

<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { NButton, NSpace, NSwitch, NTag, useDialog, useMessage, type DataTableColumns } from 'naive-ui'
import { ChevronDown, ChevronUp, MoreHorizontal, Plus, PlayCircle } from '@lucide/vue'
import { useProvidersStore } from '../../store/providers'
import { useModelsStore } from '../../store/models'
import type { ModelCandidate } from '../../api/models'
import { displayMessage } from '../../api/client'
import { useConfirmedStatusToggle } from '../../composables/useConfirmedStatusToggle'
import { providerDisableCopy } from '../../utils/impactSummary'
import type { BatchTestResult, Provider, ProviderKey } from '../../api/providers'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import KeyEditModal from '../../components/providers/KeyEditModal.vue'
import ProviderEditModal from '../../components/providers/ProviderEditModal.vue'
import ResponsiveDataTable from '../../components/common/ResponsiveDataTable.vue'
import ResponsiveDropdown from '../../components/common/ResponsiveDropdown.vue'
import { columnTitle, STATUS_COL_WIDTH } from '../../utils/columnTitle'
import { isTestSuccess, testOutcomeI18nKey, testOutcomeLabel, TEST_OUTCOME_MODEL_NOT_FOUND, TEST_OUTCOME_UPSTREAM_ERROR } from '../../utils/testOutcomeDisplay'
import { hintTag } from '../../utils/hintTag'
import { verificationDestinationCount } from '../../utils/providerProtocol'
import { useSingleRowAction } from '../../composables/useSingleRowAction'
import { useClientPagination } from '../../composables/useClientPagination'
import { useIsMobile } from '../../composables/useIsMobile'

const { t, te } = useI18n()
const route = useRoute()
const router = useRouter()
const dialog = useDialog()
const toggleStatusWithConfirm = useConfirmedStatusToggle(dialog)
const message = useMessage()
const store = useProvidersStore()
const modelsStore = useModelsStore()
const isMobile = useIsMobile()

// Independent client-side pagination for the two tables on this page — both are
// fully-fetched admin lists that can grow long (many keys, or many models
// mapped to one provider).
const {
  pagination: keysPagination,
} = useClientPagination()
const {
  pagination: modelsPagination,
} = useClientPagination()

const providerId = Number(route.params.id)
const provider = ref<Provider | null>(null)
const activeTab = ref('keys')
const showAddKey = ref(false)
const showEditKey = ref(false)
const editingKey = ref<ProviderKey | null>(null)
const showEditProvider = ref(false)
const testingAll = ref(false)
// Tracks the single key currently running its own "Test Connection" (distinct from
// testingAll's batch run) so the actions button can show a spinner instead
// of silently doing nothing until the request resolves.
const testingKeyId = ref<number | null>(null)
// Single-flight reorder: only one key can be moving at a time, and the
// clicked arrow shows a spinner (activeId + direction) while it runs.
const reorderAction = useSingleRowAction()
const batchSummary = ref('')
// Keyed by provider_key.id — populated once per completed batch test,
// cleared at the start of the next one (see onTestAll). Rendered as a
// per-key badge in the template above.
const batchResultByKeyId = ref<Record<number, BatchTestResult>>({})

// "N keys pending" = the keys batch test will actually hit. Batch test
// covers every key that isn't awaiting re-entry (regardless of enabled
// status — a fresh key is disabled until it passes), so this count must
// match that scope, not just the enabled subset.
const pendingCount = computed(() => {
  if (!provider.value) return null
  return provider.value.keys.filter((k) => !k.needs_reentry).length
})

// How many destinations one key test walks server-side. It multiplies the
// request budget, so a two-endpoint provider is not cut off halfway.
const destinationCount = computed(() =>
  provider.value ? verificationDestinationCount(provider.value.provider_type, provider.value.protocol_endpoints) : 1,
)

// On mobile the header buttons and the keys-toolbar buttons collapse into a
// single ResponsiveDropdown, so the toggle-status row's label follows the
// provider's current management_status and Test All disables while a run is in
// flight (the trigger button also spins via :loading="testingAll").
const headerActionOptions = computed(() => [
  { label: t('providers.editProvider'), key: 'edit' },
  { label: t('costs.detail.viewCost'), key: 'viewCost' },
  { label: t('providers.addKey'), key: 'addKey' },
  { label: t('providers.testAllButton'), key: 'testAll', disabled: testingAll.value },
  {
    label: provider.value?.management_status === 1 ? t('providers.statusDisabled') : t('providers.statusEnabled'),
    key: 'toggleStatus',
  },
])

function onHeaderAction(key: string) {
  if (key === 'edit') showEditProvider.value = true
  else if (key === 'viewCost') router.push(`/costs/providers/${provider.value!.id}`)
  else if (key === 'addKey') showAddKey.value = true
  else if (key === 'testAll') void onTestAll()
  else if (key === 'toggleStatus') onToggleProviderStatus()
}

function batchResultLabel(result: BatchTestResult): string {
  if (result.needs_reentry) return t('providers.needsReentry')
  // Must precede the skipped branch: a not-run key is skipped too, and
  // "test failed" would be a verdict on a credential nothing tried.
  if (result.not_run) return t('providers.notRun')
  if (result.skipped || result.outcome === null) return t('providers.testFailed')
  return testOutcomeLabel(t, result.outcome) + ` (${result.duration_ms}ms)`
}

function batchResultTagType(result: BatchTestResult): 'success' | 'warning' | 'error' {
  if (result.needs_reentry || result.skipped) return 'warning'
  return result.outcome === 0 ? 'success' : 'error'
}

// Some category hints are written for the test-connection dialog and direct
// the operator at controls that only exist there (an expandable raw-error
// panel, a "Fetch models" button); those categories get row-specific copy
// instead. Keyed by outcome int, not by derived i18n key — the key lookup
// falls back for unknown values and would silently extend an override to
// categories it was never written for.
const ROW_HINT_OVERRIDES: Record<number, string> = {
  [TEST_OUTCOME_UPSTREAM_ERROR]: 'providers.outcomeUpstreamError_rowHint',
  [TEST_OUTCOME_MODEL_NOT_FOUND]: 'providers.outcomeModelNotFound_rowHint',
}

// The stored outcome category of the key's last test, rendered so the
// specific failure ("rate limited" vs "unreachable" vs "quota unavailable")
// survives a page reload instead of living only in a transient toast. A
// successful last test adds nothing the Passed verification tag doesn't
// already say, so only non-success categories get a tag. A needs_reentry key
// shows nothing either: its stored result was authorized against a
// superseded destination and presenting it as current would mislead.
function lastTestResultTag(row: ProviderKey) {
  if (row.last_test_result === null || isTestSuccess(row.last_test_result) || row.needs_reentry) return null
  const label = testOutcomeLabel(t, row.last_test_result)
  const hintKey = ROW_HINT_OVERRIDES[row.last_test_result] ?? `providers.${testOutcomeI18nKey(row.last_test_result)}_hint`
  // Gated on te(): a future category without a hint must degrade to the bare
  // label, not read its raw message key to a screen reader.
  const hint = te(hintKey) ? t(hintKey) : ''
  return hintTag({ text: label, type: 'warning', hint })
}

onMounted(async () => {
  try {
    await reload()
  } catch (err) {
    message.error(displayMessage(err, t))
    return
  }
  try {
    await modelsStore.fetchList()
  } catch (err) {
    message.error(displayMessage(err, t))
  }
})

async function reload() {
  provider.value = await store.fetchDetail(providerId)
}

// Models referencing this provider as a candidate (the "model mapping" tab). This
// tab was previously an EmptyState placeholder; this joins modelsStore.list
// (every model with its candidates) on candidate.provider_id.
type LinkedModelRow = { candidateId: number; modelName: string; candidate: ModelCandidate }

const linkedModelRows = computed<LinkedModelRow[]>(() => {
  if (!provider.value) return []
  const rows: LinkedModelRow[] = []
  for (const m of modelsStore.list) {
    for (const c of m.candidates) {
      if (c.provider_id === providerId) {
        rows.push({ candidateId: c.id, modelName: m.name, candidate: c })
      }
    }
  }
  return rows
})

const modelColumns = computed<DataTableColumns<LinkedModelRow>>(() => [
  {
    title: columnTitle(t('models.name'), t('models.name_tip')),
    key: 'modelName',
    minWidth: 160,
  },
  {
    title: columnTitle(t('models.providerModelName'), t('models.providerModelName_tip')),
    key: 'providerModelName',
    minWidth: 160,
    render: (row) => row.candidate.provider_model_name || '-',
  },
  {
    // Reads row.candidate.management_status (candidate-level), NOT the
    // model-level status — so the tooltip must describe candidate
    // semantics ("skips this candidate only"), not model semantics
    // ("model rejects all requests"). Reusing models.managementStatusColumn
    // here would mislabel the column.
    title: columnTitle(t('providers.candidateStatus'), t('providers.candidateStatus_tip')),
    key: 'management_status',
    width: 100,
    align: 'center',
    render: (row) =>
      h(
        NTag,
        { size: 'small', bordered: false, type: row.candidate.management_status === 1 ? 'success' : 'default' },
        { default: () => (row.candidate.management_status === 1 ? t('providers.statusEnabled') : t('providers.statusDisabled')) },
      ),
  },
])

function verificationLabel(status: number): string {
  if (status === 1) return t('providers.verificationPassed')
  if (status === 2) return t('providers.verificationFailed')
  return t('providers.verificationUntested')
}

function verificationTagType(status: number): 'success' | 'error' | 'default' {
  if (status === 1) return 'success'
  if (status === 2) return 'error'
  return 'default'
}

// A real NDataTable with defined columns (matching the reference
// project's ApiKeysPage.vue convention) rather than a hand-rolled list of
// flex rows — kept as a computed so the columns re-render when the active
// locale or batchResultByKeyId changes.
const keyColumns = computed<DataTableColumns<ProviderKey>>(() => [
  { title: columnTitle(t('providers.keyLabel'), t('providers.keyLabel_tip')), key: 'label', minWidth: 140 },
  {
    title: columnTitle(t('providers.keyPlaintext'), t('providers.keyPlaintext_tip')),
    key: 'key_prefix',
    minWidth: 140,
    render: (row) => h('span', { class: 'key-prefix-cell' }, `${row.key_prefix}***`),
  },
  {
    title: columnTitle(t('providers.testModel'), t('providers.testModel_tip')),
    key: 'test_model',
    minWidth: 140,
  },
  {
    title: columnTitle(t('providers.statusColumn'), t('providers.statusColumn_tip')),
    key: 'status',
    minWidth: 220,
    render: (row) => {
      const tags = [
        h(
          NTag,
          { size: 'small', bordered: false, type: verificationTagType(row.verification_status) },
          { default: () => verificationLabel(row.verification_status) },
        ),
      ]
      if (row.needs_reentry) {
        tags.push(
          h(NTag, { type: 'warning', size: 'small', bordered: false }, { default: () => t('providers.needsReentry') }),
        )
      }
      const batchResult = batchResultByKeyId.value[row.id]
      if (!batchResult) {
        // The fresh in-session batch verdict outranks the stored one — the
        // row's last_test_result is stale until the next reload.
        const stored = lastTestResultTag(row)
        if (stored) tags.push(stored)
      } else {
        tags.push(
          h(
            NTag,
            { type: batchResultTagType(batchResult), size: 'small', bordered: false },
            { default: () => batchResultLabel(batchResult) },
          ),
        )
      }
      return h(NSpace, { size: 4 }, { default: () => tags })
    },
  },
  {
    title: columnTitle(t('providers.managementStatusColumn'), t('providers.managementStatusColumn_tip')),
    key: 'management_status',
    width: STATUS_COL_WIDTH,
    align: 'center',
    render: (row) =>
      h(NSwitch, {
        value: row.management_status === 1,
        'onUpdate:value': (v: boolean) => onToggleKeyStatus(row.id, v),
      }),
  },
  {
    title: t('providers.reorderColumn'),
    key: 'reorder',
    width: 70,
    align: 'center',
    render: (row, index) => {
      const count = provider.value?.keys.length ?? 0
      const active = reorderAction.activeId.value
      const reordering = active !== null
      const upLoading = active === row.id && reorderAction.direction.value === 'up'
      const downLoading = active === row.id && reorderAction.direction.value === 'down'
      return h('div', { style: 'display:inline-flex;align-items:center;gap:2px;justify-content:center' }, [
        h(
          NButton,
          { size: 'small', quaternary: true, circle: true, disabled: reordering || index === 0, loading: upLoading, title: t('providers.moveUp'), onClick: () => onReorder(row.id, 'up') },
          { icon: () => h(ChevronUp, { size: 16 }) },
        ),
        h(
          NButton,
          { size: 'small', quaternary: true, circle: true, disabled: reordering || index >= count - 1, loading: downLoading, title: t('providers.moveDown'), onClick: () => onReorder(row.id, 'down') },
          { icon: () => h(ChevronDown, { size: 16 }) },
        ),
      ])
    },
  },
  {
    // Matches the reference project's ApiKeysPage.vue actions-column
    // convention: a single compact "···" dropdown rather than several
    // inline text buttons — the inline-button version made this column
    // wide enough to force the whole table into horizontal scroll.
    title: t('common.actions'),
    key: 'actions',
    width: 60,
    align: 'center',
    render: (row) =>
      h(
        ResponsiveDropdown,
        {
          trigger: 'click',
          placement: 'bottom-end',
          triggerText: t('common.actions'),
          disabled: testingKeyId.value === row.id,
          loading: testingKeyId.value === row.id,
          height: 140,
          options: [
            { label: t('providers.editKey'), key: 'edit' },
            { label: t('providers.testConnection'), key: 'test', disabled: row.needs_reentry },
          ],
          onSelect: (key: string) => {
            if (key === 'edit') onEditKey(row)
            else if (key === 'test') onTestOneKey(row.id)
          },
        },
        {
          default: () =>
            h(
              NButton,
              { size: 'small', quaternary: true, circle: true, loading: testingKeyId.value === row.id, disabled: testingKeyId.value === row.id },
              { icon: () => h(MoreHorizontal, { size: 16 }) },
            ),
        },
      ),
  },
])

function onEditKey(key: ProviderKey) {
  editingKey.value = key
  showEditKey.value = true
}

// A key edit re-tests the credential server-side only when a new plaintext
// was submitted; only then is the batch run's verdict for it out of date. A
// rename-only save keeps the batch entry — evicting it would erase
// distinctions that exist nowhere else, like "skipped" or "not run".
function onKeySaved(retested: boolean) {
  if (retested && editingKey.value) delete batchResultByKeyId.value[editingKey.value.id]
  void reload()
}

async function onTestOneKey(keyId: number) {
  testingKeyId.value = keyId
  try {
    const updated = await store.testKey(providerId, keyId, destinationCount.value)
    // This key's batch verdict is now out of date, and leaving it in place
    // would keep outranking the fresh stored result in the status cell.
    delete batchResultByKeyId.value[keyId]
    await reload()
    // Two-tier feedback so the click is never silent: pass (green) vs
    // everything else (yellow) named by its specific outcome reason
    // (e.g. "unreachable"). Deliberately not mirroring the backend's
    // definitive-fail vs inconclusive split — that lives in
    // classifyTestResult and would drift if duplicated here.
    const outcome = updated.last_test_result
    if (outcome === null) return
    const label = testOutcomeLabel(t, outcome)
    if (outcome === 0) message.success(label)
    else message.warning(label)
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    testingKeyId.value = null
  }
}

async function onReorder(keyId: number, direction: 'up' | 'down') {
  await reorderAction.run(keyId, async () => {
    try {
      await store.reorderKey(providerId, keyId, direction)
      await reload()
    } catch (err) {
      message.error(displayMessage(err, t))
    }
  }, direction)
}

// A key actually contributes to routing only when it's enabled AND has
// passed verification AND doesn't need re-entry — "enabled" alone
// (management_status === 1) is not the same thing, and the warning
// below previously used the weaker enabled-count check, so it silently skipped the
// "you're about to disable the only key actually keeping this provider
// available" warning whenever another merely-enabled-but-unverified key
// was also present.
function isKeyAvailable(k: ProviderKey): boolean {
  return k.management_status === 1 && k.verification_status === 1 && !k.needs_reentry
}

function onToggleKeyStatus(keyId: number, enable: boolean) {
  const targetKey = provider.value?.keys.find((k) => k.id === keyId)
  const isLastAvailable =
    !enable &&
    provider.value !== null &&
    targetKey !== undefined &&
    isKeyAvailable(targetKey) &&
    provider.value.keys.filter(isKeyAvailable).length === 1
  const proceed = async () => {
    try {
      await store.setKeyStatus(providerId, keyId, enable)
      await reload()
    } catch (err) {
      message.error(displayMessage(err, t))
    }
  }
  if (isLastAvailable) {
    dialog.warning({
      title: t('providers.confirmDisableLastKeyTitle'),
      content: t('providers.confirmDisableLastKeyContent'),
      positiveText: t('providers.statusDisabled'),
      negativeText: t('providers.cancel'),
      onPositiveClick: proceed,
    })
    return
  }
  void proceed()
}

function onToggleProviderStatus() {
  if (!provider.value) return
  const enabling = provider.value.management_status !== 1
  const proceed = async () => {
    try {
      await store.setStatus(providerId, enabling)
      await reload()
    } catch (err) {
      message.error(displayMessage(err, t))
    }
  }
  toggleStatusWithConfirm(
    enabling,
    () => providerDisableCopy(providerId, t),
    proceed,
  )
}

async function onTestAll() {
  if (!provider.value) return
  testingAll.value = true
  batchSummary.value = ''
  batchResultByKeyId.value = {}
  try {
    const { results } = await store.testAll(providerId)
    // `skipped` and `outcome === 0` are not mutually exclusive: a result
    // can be both TestSuccess AND skipped (its CAS write was lost to a
    // concurrent edit — the test itself succeeded, but nothing was
    // persisted). `passed` must exclude skipped results, or a skipped+
    // successful result gets double-counted and `failed` goes negative.
    const skipped = results.filter((r) => r.skipped).length
    const passed = results.filter((r) => !r.skipped && r.outcome === 0).length
    const failed = results.length - passed - skipped
    batchSummary.value = t('providers.testAllSummary', { passed, failed, skipped })
    // Keys the run's budget never reached are the one case where the operator
    // has something to do next, so say it outright instead of leaving them to
    // infer it from the skipped count.
    const notRun = results.filter((r) => r.not_run).length
    if (notRun > 0) {
      batchSummary.value += ' ' + t('providers.testAllBudgetExhausted', { count: notRun })
    }
    batchResultByKeyId.value = Object.fromEntries(results.map((r) => [r.key_id, r]))
    await reload()
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    testingAll.value = false
  }
}
</script>

<style scoped>
.keys-toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: var(--space-4);
}

.keys-toolbar__count {
  color: var(--color-text-secondary);
  font-size: var(--text-sm);
}

:deep(.key-prefix-cell) {
  color: var(--color-text-muted);
  font-size: var(--text-xs);
  font-family: var(--font-mono);
}

.summary {
  margin-top: var(--space-3);
}
</style>
