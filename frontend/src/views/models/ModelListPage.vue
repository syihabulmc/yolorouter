<!-- frontend/src/views/models/ModelListPage.vue -->
<template>
  <div class="models-page">
    <PageHeader :eyebrow="t('models.eyebrow')" :title="t('models.pageTitle')" :description="t('models.pageDescription')">
      <template #actions>
        <n-button type="primary" @click="showCreate = true">
          <template #icon><Plus :size="16" /></template>
          {{ t('models.createButton') }}
        </n-button>
      </template>
    </PageHeader>

    <EmptyState v-if="!store.loading && store.list.length === 0" :icon="Boxes" :title="t('models.listEmpty')">
      <template #action>
        <n-button type="primary" @click="showCreate = true">{{ t('models.createButton') }}</n-button>
      </template>
    </EmptyState>

    <template v-else>
      <div class="filter-panel">
        <div class="filter-grid">
          <div class="filter-item filter-item--search">
            <n-input
              v-model:value="filter.name"
              :placeholder="t('models.filterName')"
              clearable
              size="small"
              @keyup.enter="onSearch"
            >
              <template #prefix><Search :size="14" /></template>
            </n-input>
          </div>
          <div class="filter-item">
            <n-select
              v-model:value="filter.running"
              :options="runningStatusOptions"
              :placeholder="t('models.filterRunningStatus')"
              clearable
              size="small"
              @update:value="onSearch"
            />
          </div>
          <div class="filter-item">
            <n-select
              v-model:value="filter.management"
              :options="managementStatusOptions"
              :placeholder="t('models.filterManagementStatus')"
              clearable
              size="small"
              @update:value="onSearch"
            />
          </div>
          <div class="filter-actions">
            <n-button size="small" type="primary" @click="onSearch">{{ t('models.search') }}</n-button>
            <n-button size="small" quaternary @click="onReset">{{ t('models.reset') }}</n-button>
          </div>
        </div>
      </div>

      <div class="data-table-wrapper">
        <n-data-table
          :columns="columns"
          :data="filteredModels"
          :loading="store.loading"
          :bordered="false"
          :single-line="false"
          :row-key="(row: Model) => row.id"
          :row-props="rowProps"
          :pagination="pagination"
          @update:page="onPageChange"
          @update:page-size="onPageSizeChange"
        />
      </div>
    </template>

    <NewModelModal v-model:show="showCreate" />
    <ModelEditModal v-model:show="showEditModel" :model="editingModel" @updated="onEdited" />
  </div>
</template>

<script setup lang="ts">
import { computed, h, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { NButton, NSwitch, NTag, useDialog, useMessage,NDropdown, type DataTableColumns } from 'naive-ui'
import { Boxes, Plus, Search } from '@lucide/vue'
import { useModelsStore } from '../../store/models'
import { displayMessage } from '../../api/client'
import { toggleStatusWithConfirm } from '../../composables/useConfirmedStatusToggle'
import { modelRunningStatusDisplay, MODEL_RUNNING_STATUS_DISPLAY } from '../../utils/modelStatusDisplay'
import { columnTitle } from '../../utils/columnTitle'
import { useClientPagination } from '../../composables/useClientPagination'
import type { Model } from '../../api/models'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import NewModelModal from '../../components/models/NewModelModal.vue'
import ModelEditModal from '../../components/models/ModelEditModal.vue'

const { t } = useI18n()
const router = useRouter()
const dialog = useDialog()
const message = useMessage()
const store = useModelsStore()
const showCreate = ref(false)
// Inline row edit: reuse the same edit modal the detail page uses, opened
// straight from the list so a name change needs no navigation.
const showEditModel = ref(false)
const editingModel = ref<Model | null>(null)

function openEditModel(row: Model) {
  editingModel.value = row
  showEditModel.value = true
}

function onEdited() {
  void store.fetchList().catch((err) => message.error(displayMessage(err, t)))
}

// In-page filters over the fully-fetched list (name substring + running /
// management status). `filter` is the live draft the inputs edit; `applied`
// is the snapshot the table actually filters by — mirroring the
// search/reset flow on the request-logs page so the text field only takes
// effect on Enter / the Search button, not on every keystroke.
interface ModelFilter {
  name: string
  running: string | null
  management: number | null
}
const emptyFilter = (): ModelFilter => ({ name: '', running: null, management: null })
const filter = reactive<ModelFilter>(emptyFilter())
const applied = reactive<ModelFilter>(emptyFilter())

const runningStatusOptions = computed(() =>
  Object.entries(MODEL_RUNNING_STATUS_DISPLAY).map(([value, { i18nKey }]) => ({
    label: t(`models.running${i18nKey}`),
    value,
  })),
)
const managementStatusOptions = computed(() => [
  { label: t('models.statusEnabled'), value: 1 },
  { label: t('models.statusDisabled'), value: 2 },
])

const filteredModels = computed(() => {
  const q = applied.name.trim().toLowerCase()
  return store.list.filter((m) => {
    if (q && !m.name.toLowerCase().includes(q)) return false
    if (applied.running && m.running_status !== applied.running) return false
    if (applied.management !== null && m.management_status !== applied.management) return false
    return true
  })
})

// Client-side pagination: models are few (admin-configured), so the full list
// is fetched once and sliced in the table rather than adding a server-side
// paged endpoint.
const { pagination, onPageChange, onPageSizeChange } = useClientPagination()

// A narrowed filter can leave the current page past the end of the results —
// reset to the first page whenever a filter is applied.
function onSearch() {
  Object.assign(applied, filter)
  pagination.page = 1
}
function onReset() {
  Object.assign(filter, emptyFilter())
  Object.assign(applied, emptyFilter())
  pagination.page = 1
}

onMounted(() => {
  void store.fetchList().catch((err) => message.error(displayMessage(err, t)))
})

function goDetail(id: number) {
  router.push(`/models/${id}`)
}

function rowProps(row: Model) {
  return { style: 'cursor: pointer', onClick: () => goDetail(row.id) }
}

function onToggleStatus(row: Model, enable: boolean) {
  toggleStatusWithConfirm(
    dialog,
    enable,
    {
      title: t('models.confirmDisableModelTitle'),
      content: t('models.confirmDisableModelContent', { count: 0 }),
      positiveText: t('models.statusDisabled'),
      negativeText: t('models.cancel'),
    },
    async () => {
      try {
        await store.setStatus(row.id, enable)
        await store.fetchList()
      } catch (err) {
        message.error(displayMessage(err, t))
      }
    },
  )
}

const columns = computed<DataTableColumns<Model>>(() => [
  {
    title: columnTitle(t('models.name'), t('models.name_tip')),
    key: 'name',
    minWidth: 200,
    render: (row) => h('span', { class: 'model-name-cell' }, row.name),
  },
  {
    title: columnTitle(t('models.runningStatusColumn'), t('models.runningStatusColumn_tip')),
    key: 'running_status',
    width: 180,
    render: (row) => {
      const display = modelRunningStatusDisplay(row.running_status)
      return h(NTag, { size: 'small', bordered: false, type: display.tagType }, { default: () => t(`models.running${display.i18nKey}`) })
    },
  },
  {
    title: columnTitle(t('models.candidateCountColumn'), t('models.candidateCountColumn_tip')),
    key: 'candidates',
    width: 140,
    render: (row) => `${row.candidates.filter((c) => c.routable).length} / ${row.candidates.length}`,
  },
  {
    title: columnTitle(t('models.firstRouteColumn'), t('models.firstRouteColumn_tip')),
    key: 'first_route',
    minWidth: 200,
    render: (row) => {
      const first = row.candidates[0]
      return first ? `${first.provider_name} / ${first.provider_model_name}` : '-'
    },
  },
  {
    title: columnTitle(t('models.managementStatusColumn'), t('models.managementStatusColumn_tip')),
    key: 'management_status',
    width: 100,
    render: (row) =>
      h(
        'div',
        { onClick: (e: MouseEvent) => e.stopPropagation() },
        [
          h(NSwitch, {
            size: 'small',
            value: row.management_status === 1,
            'onUpdate:value': (v: boolean) => onToggleStatus(row, v),
          }),
        ],
      ),
  },
  {
    title: t('common.actions'),
    key: 'actions',
    width: 90,
    render: (row) =>
      h(
        'div',
        { onClick: (e: MouseEvent) => e.stopPropagation() },
        [
          h(
            NDropdown,
            {
              trigger: 'click',
              placement: 'bottom-end',
              options: [
                { label: t('models.editModel'), key: 'edit' },
                { label: t('models.importToCCS'), key: 'importCCSImport' },
              ],
              onSelect: (key: string) => {
                if (key === 'edit') openEditModel(row)
                else if (key === 'importCCSImport') importCCSImport(row)
              },
            },
            { default: () => h(NButton, { size: 'small', quaternary: true }, { default: () => '⋯' }) },
          ),
        ],
      ),
  },
])

let ccsOpenTimer: ReturnType<typeof setTimeout> | null = null
let ccsOpenCleanup: (() => void) | null = null

function buildCCSwitchImportUrl(row: APIKey,): string {
  const params = new URLSearchParams({
    resource: 'provider',
    app: 'claude',
    name: `YoloRouter${row.name ? ` - ${row.name}` : ''}`,
    endpoint: location.origin,
    apiKey: 'sk-',
    homepage: location.origin,
    model: row.name, 
  })
  return `ccswitch://v1/import?${params.toString()}`
}



function importCCSImport(row: Model) {
  let maybeOpened = false

  const cleanup = () => {
    window.removeEventListener('blur', markOpened)
    window.removeEventListener('pagehide', markOpened)
    document.removeEventListener('visibilitychange', handleVisibilityChange)
  }

  const markOpened = () => {
    maybeOpened = true
    cleanup()
  }

  const handleVisibilityChange = () => {
    if (document.hidden) markOpened()
  }

  if (ccsOpenTimer) {
    clearTimeout(ccsOpenTimer)
    ccsOpenTimer = null
  }
  if (ccsOpenCleanup) {
    ccsOpenCleanup()
    ccsOpenCleanup = null
  }

  window.addEventListener('blur', markOpened, { once: true })
  window.addEventListener('pagehide', markOpened, { once: true })
  document.addEventListener('visibilitychange', handleVisibilityChange)
  ccsOpenCleanup = cleanup

  message.info(t('models.apiKeyImportOpeningCCS'))
  window.location.href = buildCCSwitchImportUrl(row)

  ccsOpenTimer = setTimeout(() => {
    cleanup()
    ccsOpenTimer = null
    ccsOpenCleanup = null
    if (!maybeOpened && document.visibilityState === 'visible') {
      message.error(t('models.apiKeyImportOpenFailed'))
    } else {
      message.success(t('models.apiKeyImportOpenSuccess'))
    }
  }, 5000)
}

</script>

<style scoped>
.models-page {
  display: flex;
  flex-direction: column;
  gap: var(--space-6);
}

:deep(.model-name-cell) {
  font-weight: 650;
  color: var(--color-text);
}
</style>
