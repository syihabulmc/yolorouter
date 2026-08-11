<!-- frontend/src/views/models/ModelDetailPage.vue -->
<template>
  <div class="common-page" v-if="modelData">
    <PageHeader class="actions-placeholder" :eyebrow="t('models.eyebrow')" :title="modelData.name" :description="`${t('models.runningStatusColumn')}: ${t(`models.running${runningStatusKey}`)}`">
      <template #actions>
        <template v-if="!isMobile">
          <n-button size="small" @click="showEditModel = true">{{ t('models.editModel') }}</n-button>
          <n-button size="small" @click="router.push(modelCostDetailLocation(modelData.name))">
            {{ t('costs.detail.viewCost') }}
          </n-button>
          <n-button size="small" @click="onToggleModelStatus">
            {{ modelData.management_status === 1 ? t('models.statusDisabled') : t('models.statusEnabled') }}
          </n-button>
        </template>

        <ResponsiveDropdown
          v-else
          trigger="click"
          placement="bottom-end"
          :height="240"
          :trigger-text="t('common.actions')"
          :options="headerActionOptions"
          @select="onHeaderAction"
        />
      </template>
    </PageHeader>

    <n-tabs v-model:value="activeTab" type="line" animated>
      <n-tab-pane name="route" :tab="t('models.tabRoute')">
        <div v-if="!isMobile" class="route-toolbar">
          <n-button @click="showAddCandidate = true">
            <template #icon><Plus :size="16" /></template>
            {{ t('models.addCandidate') }}
          </n-button>
        </div>

        <EmptyState v-if="modelData.candidates.length === 0" :title="t('models.routeChainEmpty')">
          <template #action>
            <n-button type="primary" @click="showAddCandidate = true">{{ t('models.addCandidate') }}</n-button>
          </template>
        </EmptyState>

        <div v-else class="data-table-wrapper">
          <ResponsiveDataTable
            :columns="candidateColumns"
            :data="modelData.candidates"
            :scroll-x="920"
            :row-key="(row: ModelCandidate) => row.id"
            :pagination="candidatePagination"
          />
        </div>
      </n-tab-pane>

      <n-tab-pane name="impact" :tab="t('models.tabImpact')">
        <div class="section-card" style="white-space: pre-line">{{ impactText }}</div>
      </n-tab-pane>
    </n-tabs>

    <CandidateEditModal v-model:show="showAddCandidate" :model-id="modelData.id" :model-name="modelData.name" @saved="reload" />
    <CandidateEditModal
      v-model:show="showEditCandidate"
      :model-id="modelData.id"
      :model-name="modelData.name"
      :editing-candidate="editingCandidate"
      @saved="reload"
      @retest="onRetestCandidate"
    />
    <ModelEditModal v-model:show="showEditModel" :model="modelData" @updated="reload" />
  </div>
</template>

<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
// NTooltip is not in main.ts's create() registry, so it must be imported
// explicitly or Vue renders it as an inert unknown element.
import { NButton, NSwitch, NTag, NTooltip, useDialog, useMessage, type DataTableColumns } from 'naive-ui'
import { ChevronDown, ChevronUp, MoreHorizontal, Plus } from '@lucide/vue'
import { useModelsStore } from '../../store/models'
import { displayMessage } from '../../api/client'
import { useConfirmedStatusToggle } from '../../composables/useConfirmedStatusToggle'
import { modelDisableCopy, modelImpactOverview } from '../../utils/impactSummary'
import { hintTag } from '../../utils/hintTag'
import { candidateTestResultText, capabilityState, modelRunningStatusDisplay } from '../../utils/modelStatusDisplay'
import { isTestSuccess } from '../../utils/testOutcomeDisplay'
import { modelCostDetailLocation } from '../../utils/modelCostLocation'
import { getModelImpact, type Model, type ModelCandidate, type ModelImpact } from '../../api/models'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import CandidateEditModal from '../../components/models/CandidateEditModal.vue'
import ModelEditModal from '../../components/models/ModelEditModal.vue'
import ResponsiveDataTable from '../../components/common/ResponsiveDataTable.vue'
import ResponsiveDropdown from '../../components/common/ResponsiveDropdown.vue'
import { columnTitle, STATUS_COL_WIDTH } from '../../utils/columnTitle'
import { useClientPagination } from '../../composables/useClientPagination'
import { useSingleRowAction } from '../../composables/useSingleRowAction'
import { useIsMobile } from '../../composables/useIsMobile.ts'
const { t, te } = useI18n()
const route = useRoute()
const router = useRouter()
const dialog = useDialog()
const toggleStatusWithConfirm = useConfirmedStatusToggle(dialog)
const message = useMessage()
const store = useModelsStore()
const isMobile = useIsMobile()

const modelId = Number(route.params.id)
const modelData = ref<Model | null>(null)
const activeTab = ref('route')
const showAddCandidate = ref(false)
const showEditCandidate = ref(false)
const showEditModel = ref(false)

// Client-side pagination for the route-chain candidate table — a single
// model's candidate list is short, so slice in-page rather than paging
// server-side.
const {
  pagination: candidatePagination,
} = useClientPagination()
const editingCandidate = ref<ModelCandidate | null>(null)
// Tracks the single candidate currently running its own capability test so
// the actions button can show a spinner instead of silently doing nothing
// until the request resolves (mirrors ProviderDetailPage.vue's testingKeyId).
const testingCandidateId = ref<number | null>(null)
const reorderAction = useSingleRowAction()

const runningStatusKey = computed(() => modelRunningStatusDisplay(modelData.value?.running_status ?? 'not_configured').i18nKey)

// On mobile the header buttons collapse into a single ResponsiveDropdown, so the
// toggle-status row's label follows the model's current management_status.
const headerActionOptions = computed(() => [
  { label: t('models.editModel'), key: 'edit' },
  { label: t('costs.detail.viewCost'), key: 'viewCost' },
  { label: t('models.addCandidate'), key: 'addCandidate' },
  {
    label: modelData.value?.management_status === 1 ? t('models.statusDisabled') : t('models.statusEnabled'),
    key: 'toggleStatus',
  },
])

function onHeaderAction(key: string) {
  if (key === 'edit') showEditModel.value = true
  else if (key === 'viewCost') router.push(modelCostDetailLocation(modelData.value!.name))
  else if (key === 'addCandidate') showAddCandidate.value = true
  else if (key === 'toggleStatus') onToggleModelStatus()
}

onMounted(() => {
  void reload().catch((err) => message.error(displayMessage(err, t)))
})

async function reload() {
  modelData.value = await store.fetchDetail(modelId)
  // Refreshed alongside the detail so the impact tab tracks status changes;
  // a failed fetch degrades the tab, never the page.
  try {
    impact.value = await getModelImpact(modelId)
    impactFailed.value = false
  } catch {
    impact.value = null
    impactFailed.value = true
  }
}

const impact = ref<ModelImpact | null>(null)
const impactFailed = ref(false)
const impactText = computed(() => {
  if (impact.value) return modelImpactOverview(t, impact.value, true)
  return impactFailed.value ? t('models.impactLoadFailed') : t('common.loading')
})

function onEditCandidate(candidate: ModelCandidate) {
  editingCandidate.value = candidate
  showEditCandidate.value = true
}

async function onRetestCandidate(candidateId: number) {
  testingCandidateId.value = candidateId
  try {
    const updated = await store.retestCandidate(modelId, candidateId)
    await reload()
    // Two-tier feedback so the click is never silent: pass (green) vs. the
    // specific outcome reason (yellow).
    //
    // Judged on last_test_result — THIS run's basic-probe outcome — not on
    // verification_status. An inconclusive run (rate limited, unreachable)
    // deliberately leaves a previously passing status alone, so reading the
    // status would report "test passed" for a run the provider actually refused.
    const passed = updated.last_test_result !== null && isTestSuccess(updated.last_test_result)
    message[passed ? 'success' : 'warning'](candidateTestResultText(t, passed, updated.last_test_result))
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    testingCandidateId.value = null
  }
}

async function onReorder(candidateId: number, direction: 'up' | 'down') {
  await reorderAction.run(candidateId, async () => {
    try {
      await store.reorderCandidate(modelId, candidateId, direction)
      await reload()
    } catch (err) {
      message.error(displayMessage(err, t))
    }
  }, direction)
}

function onToggleCandidateStatus(candidateId: number, enable: boolean) {
  void (async () => {
    try {
      await store.setCandidateStatus(modelId, candidateId, enable)
      await reload()
    } catch (err) {
      message.error(displayMessage(err, t))
    }
  })()
}

function onDeleteCandidate(candidate: ModelCandidate) {
  const remainingRoutable = modelData.value!.candidates.filter((c) => c.routable && c.id !== candidate.id).length
  dialog.warning({
    title: t('models.confirmDeleteCandidateTitle'),
    content: candidate.sort_order === 1
      ? t('models.confirmDeleteFirstCandidateContent')
      : t('models.confirmDeleteCandidateContent', { count: remainingRoutable }),
    positiveText: t('models.save'),
    negativeText: t('models.cancel'),
    onPositiveClick: async () => {
      try {
        await store.deleteCandidate(modelId, candidate.id)
        await reload()
      } catch (err) {
        message.error(displayMessage(err, t))
      }
    },
  })
}

function onToggleModelStatus() {
  if (!modelData.value) return
  const enabling = modelData.value.management_status !== 1
  toggleStatusWithConfirm(
    enabling,
    () => modelDisableCopy(modelId, t),
    async () => {
      try {
        await store.setStatus(modelId, enabling)
        await reload()
      } catch (err) {
        message.error(displayMessage(err, t))
      }
    },
  )
}

// Capability cells render each state distinctly. An unconfirmed capability is not
// a failure — routing ignores these flags entirely — it just means no probe has
// confirmed it yet, so the tag is clickable to retest rather than alarming.
// A candidate that cannot be routed to is shown with the reason, not just the
// fact: each reason names a different repair — switch something back on, add a
// key, fill in a name, run a probe — and the fact alone leaves an operator to
// guess which of them applies.
function renderRoutable(row: ModelCandidate) {
  if (row.routable) {
    return h(NTag, { size: 'small', type: 'success', bordered: false }, { default: () => '✓' })
  }
  // A reason the locale does not know (a newer backend, an older frontend)
  // must not surface as a raw message key; the generic fallback covers it.
  const reasonKey = `models.blockedBy.${row.blocked_by}`
  const reason = row.blocked_by && te(reasonKey) ? t(reasonKey) : t('models.blockedBy.unknown')
  return hintTag({ text: '✗', type: 'warning', hint: reason, ariaLabel: reason })
}

function renderCapability(row: ModelCandidate, flag: boolean | null) {
  const busy = testingCandidateId.value === row.id
  switch (capabilityState(flag)) {
    case 'confirmed':
      return h(NTag, { size: 'small', type: 'success', bordered: false }, { default: () => '✓' })
    case 'unsupported':
      return h(NTag, { size: 'small', type: 'error', bordered: false }, { default: () => '✗' })
    default:
      return h(
        NTooltip,
        { trigger: 'hover' },
        {
          trigger: () =>
            h(
              NTag,
              {
                size: 'small',
                type: 'warning',
                bordered: false,
                style: busy ? 'cursor: progress' : 'cursor: pointer',
                // Clickable, so button semantics rather than the display-only
                // hintTag shape: focusable, named by the hint, and operable
                // from the keyboard.
                role: 'button',
                'aria-label': t('models.probeUnconfirmedHint'),
                tabindex: 0,
                onClick: () => {
                  if (!busy) void onRetestCandidate(row.id)
                },
                onKeydown: (e: KeyboardEvent) => {
                  if ((e.key === 'Enter' || e.key === ' ') && !busy) {
                    e.preventDefault()
                    void onRetestCandidate(row.id)
                  }
                },
              },
              { default: () => '?' },
            ),
          default: () => t('models.probeUnconfirmedHint'),
        },
      )
  }
}

// Actions column collapses into an NDropdown — a convention established
// after flat buttons pushed the table into horizontal scroll.
const candidateColumns = computed<DataTableColumns<ModelCandidate>>(() => [
  { title: columnTitle(t('models.provider'), t('models.provider_tip')), key: 'provider_name', minWidth: 140 },
  { title: columnTitle(t('models.providerModelName'), t('models.providerModelName_tip')), key: 'provider_model_name', minWidth: 160 },
  {
    title: columnTitle(t('models.managementStatusColumn'), t('models.managementStatusColumn_tip')),
    key: 'management_status',
    width: STATUS_COL_WIDTH,
    align: 'center',
    render: (row) => h(NSwitch, { value: row.management_status === 1, 'onUpdate:value': (v: boolean) => onToggleCandidateStatus(row.id, v) }),
  },
  {
    title: columnTitle(t('models.routableColumn'), t('models.routableColumn_tip')),
    key: 'routable',
    width: STATUS_COL_WIDTH,
    align: 'center',
    render: (row) => renderRoutable(row),
  },
  {
    title: columnTitle(t('models.supportsStreaming'), t('models.supportsStreaming_tip')),
    key: 'supports_streaming',
    width: STATUS_COL_WIDTH,
    align: 'center',
    render: (row) => renderCapability(row, row.supports_streaming),
  },
  {
    title: columnTitle(t('models.supportsFunctionCalling'), t('models.supportsFunctionCalling_tip')),
    key: 'supports_function_calling',
    width: STATUS_COL_WIDTH,
    align: 'center',
    render: (row) => renderCapability(row, row.supports_function_calling),
  },
  {
    title: t('models.reorderColumn'),
    key: 'reorder',
    width: 70,
    align: 'center',
    render: (row, index) => {
      const count = modelData.value?.candidates.length ?? 0
      const r = reorderAction.activeId.value
      const reordering = r !== null
      const upLoading = r === row.id && reorderAction.direction.value === 'up'
      const downLoading = r === row.id && reorderAction.direction.value === 'down'
      return h('div', { style: 'display:inline-flex;align-items:center;gap:2px;justify-content:center' }, [
        h(
          NButton,
          { size: 'small', quaternary: true, circle: true, disabled: reordering || index === 0, loading: upLoading, title: t('models.moveUp'), onClick: () => onReorder(row.id, 'up') },
          { icon: () => h(ChevronUp, { size: 16 }) },
        ),
        h(
          NButton,
          { size: 'small', quaternary: true, circle: true, disabled: reordering || index >= count - 1, loading: downLoading, title: t('models.moveDown'), onClick: () => onReorder(row.id, 'down') },
          { icon: () => h(ChevronDown, { size: 16 }) },
        ),
      ])
    },
  },
  {
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
          disabled: testingCandidateId.value === row.id,
          loading: testingCandidateId.value === row.id,
          height: 200,
          options: [
            { label: t('models.editCandidate'), key: 'edit' },
            // Titled so the dropdown says what a retest actually does — it
            // reruns all three probes and rewrites the stored verdicts.
            { label: t('models.retest'), key: 'retest', props: { title: t('models.retest_tip') } },
            { type: 'divider', key: 'd' },
            { label: t('models.deleteCandidate'), key: 'delete', props: { style: 'color: var(--color-danger)' } },
          ],
          onSelect: (key: string) => {
            if (key === 'edit') onEditCandidate(row)
            else if (key === 'retest') onRetestCandidate(row.id)
            else if (key === 'delete') onDeleteCandidate(row)
          },
        },
        {
          default: () =>
            h(
              NButton,
              { size: 'small', quaternary: true, circle: true, loading: testingCandidateId.value === row.id, disabled: testingCandidateId.value === row.id },
              { icon: () => h(MoreHorizontal, { size: 16 }) },
            ),
        },
      ),
  },
])
</script>

<style scoped>
.route-toolbar {
  display: flex;
  justify-content: flex-end;
  margin-bottom: var(--space-4);
}
</style>
