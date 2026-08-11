<!-- frontend/src/views/providers/ProviderListPage.vue -->
<template>
  <div class="common-page">
    <PageHeader :eyebrow="t('providers.eyebrow')" :title="t('providers.pageTitle')" :description="t('providers.pageDescription')">
      <template #actions>
        <n-button type="primary" @click="showCreate = true">
          <template #icon><Plus :size="16" /></template>
          {{ t('providers.createButton') }}
        </n-button>
      </template>
    </PageHeader>

    <EmptyState v-if="!store.loading && store.list.length === 0" :icon="Server" :title="t('providers.listEmpty')">
      <template #action>
        <n-button type="primary" @click="showCreate = true">{{ t('providers.createButton') }}</n-button>
      </template>
    </EmptyState>

    <template v-else>
      <div class="filter-panel">
        <div class="filter-grid">
          <div class="filter-item filter-item--search">
            <n-input
              v-model:value="filter.name"
              :placeholder="t('providers.filterName')"
              clearable
              size="small"
              @keyup.enter="onSearch"
            >
              <template #prefix><Search :size="14" /></template>
            </n-input>
          </div>
          <FilterSelectField
            v-model:value="filter.protocol"
            :label="t('providers.filterProtocol')"
            :options="protocolOptions"
            :placeholder="t('providers.filterProtocol')"
            size="small"
            width="100%"
            @update:value="onSearch"
          />
          <FilterSelectField
            v-model:value="filter.running"
            :label="t('providers.filterRunningStatus')"
            :options="runningStatusOptions"
            :placeholder="t('providers.filterRunningStatus')"
            size="small"
            width="100%"
            @update:value="onSearch"
          />
          <FilterSelectField
            v-model:value="filter.management"
            :label="t('providers.filterManagementStatus')"
            :options="managementStatusOptions"
            :placeholder="t('providers.filterManagementStatus')"
            size="small"
            width="100%"
            @update:value="onSearch"
          />
          <div class="filter-actions">
            <n-button size="small" type="primary" @click="onSearch">{{ t('providers.search') }}</n-button>
            <n-button size="small" quaternary @click="onReset">{{ t('providers.reset') }}</n-button>
          </div>
        </div>
      </div>

      <div class="data-table-wrapper">
        <ResponsiveDataTable
          :columns="columns"
          :data="filteredProviders"
          :loading="store.loading"
          :scroll-x="1010"
          :row-key="(row: Provider) => row.id"
          :row-props="rowProps"
          :pagination="pagination"
        />
      </div>
    </template>

    <!-- No @created handler needed: store.create() (called inside the
         modal) already refetches the list itself. -->
    <NewProviderModal v-model:show="showCreate" />
    <ProviderEditModal v-model:show="showEditProvider" :provider="editingProvider" @updated="onEdited" />
  </div>
</template>

<script setup lang="ts">
import { computed, h, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { NButton, NSwitch, NTag, useDialog, useMessage, type DataTableColumns } from 'naive-ui'
import { MoreHorizontal, Plus, Search, Server } from '@lucide/vue'
import { useProvidersStore } from '../../store/providers'
import { displayMessage } from '../../api/client'
import type { Provider } from '../../api/providers'
import { useConfirmedStatusToggle } from '../../composables/useConfirmedStatusToggle'
import { providerDisableCopy } from '../../utils/impactSummary'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import NewProviderModal from '../../components/providers/NewProviderModal.vue'
import ProviderEditModal from '../../components/providers/ProviderEditModal.vue'
import ResponsiveDataTable from '../../components/common/ResponsiveDataTable.vue'
import ResponsiveDropdown from '../../components/common/ResponsiveDropdown.vue'
import FilterSelectField from '../../components/common/FilterSelectField.vue'
import { columnTitle } from '../../utils/columnTitle'
import { useClientPagination } from '../../composables/useClientPagination'
import { ALL_PROTOCOLS, enabledProtocolEndpoints } from '../../utils/providerProtocol'

const { t } = useI18n()
const router = useRouter()
const dialog = useDialog()
const toggleStatusWithConfirm = useConfirmedStatusToggle(dialog)
const message = useMessage()
const store = useProvidersStore()
const showCreate = ref(false)
// Inline row edit: open the provider edit modal straight from the list so a
// quick change needs no navigation into the detail page.
const showEditProvider = ref(false)
const editingProvider = ref<Provider | null>(null)

function openEditProvider(row: Provider) {
  editingProvider.value = row
  showEditProvider.value = true
}

function onEdited() {
  void store.fetchList().catch((err) => message.error(displayMessage(err, t)))
}

// In-page filters over the fully-fetched list. `filter` is the live draft the
// inputs edit; `applied` is the snapshot the table filters by — so the text
// field only takes effect on Enter / the Search button, mirroring the
// request-logs page.
interface ProviderFilter {
  name: string
  protocol: string | null
  running: string | null
  management: number | null
}
const emptyFilter = (): ProviderFilter => ({ name: '', protocol: null, running: null, management: null })
const filter = reactive<ProviderFilter>(emptyFilter())
const applied = reactive<ProviderFilter>(emptyFilter())

const protocolOptions = computed(() =>
  ALL_PROTOCOLS.map((p) => ({ label: t(`providers.protocol_${p}`), value: p })),
)
const runningStatusOptions = computed(() =>
  Object.entries(RUNNING_STATUS_DISPLAY).map(([value, { i18nKey }]) => ({
    label: t(`providers.running${i18nKey}`),
    value,
  })),
)
const managementStatusOptions = computed(() => [
  { label: t('providers.statusEnabled'), value: 1 },
  { label: t('providers.statusDisabled'), value: 2 },
])

const filteredProviders = computed(() => {
  const q = applied.name.trim().toLowerCase()
  return store.list.filter((p) => {
    if (q && !p.name.toLowerCase().includes(q)) return false
    if (applied.protocol && p.provider_type !== applied.protocol) return false
    if (applied.running && p.running_status !== applied.running) return false
    if (applied.management !== null && p.management_status !== applied.management) return false
    return true
  })
})

// Client-side pagination: providers are few (admin-configured), so the full
// list is fetched once and sliced in the table rather than adding a
// server-side paged endpoint.
const { pagination } = useClientPagination()

// Applying a narrowed filter can leave the current page past the end of the
// results, so reset to the first page.
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
  router.push(`/providers/${id}`)
}

function rowProps(row: Provider) {
  return { style: 'cursor: pointer', onClick: () => goDetail(row.id) }
}

// Single lookup table keyed by the same 5 running-status values instead of
// a separate map + switch that were always consulted together for the
// same row.
const RUNNING_STATUS_DISPLAY: Record<string, { i18nKey: string; type: 'default' | 'success' | 'warning' | 'error' }> = {
  not_configured: { i18nKey: 'NotConfigured', type: 'default' },
  pending_test: { i18nKey: 'Pending', type: 'default' },
  available: { i18nKey: 'Available', type: 'success' },
  partial: { i18nKey: 'Partial', type: 'warning' },
  unavailable: { i18nKey: 'Unavailable', type: 'error' },
}

function runningStatusDisplay(status: string) {
  return RUNNING_STATUS_DISPLAY[status] ?? RUNNING_STATUS_DISPLAY.unavailable
}

// Every distinct address this provider actually serves on: the primary
// base_url plus each enabled additional-protocol endpoint (an endpoint with an
// empty URL reuses base_url, so it collapses into the same entry). Deduped so
// a provider whose extra protocols all reuse base_url still shows one address.
function serviceAddresses(row: Provider): string[] {
  const urls = [row.base_url]
  for (const { url } of enabledProtocolEndpoints(row.provider_type, row.protocol_endpoints)) {
    urls.push(url || row.base_url)
  }
  return [...new Set(urls)]
}

// Mirrors ProviderDetailPage.vue's onToggleProviderStatus, scoped to a list
// row instead of the single loaded detail — disabling still confirms first,
// enabling proceeds directly.
function onToggleStatus(row: Provider, enable: boolean) {
  const proceed = async () => {
    try {
      await store.setStatus(row.id, enable)
      await store.fetchList()
    } catch (err) {
      message.error(displayMessage(err, t))
    }
  }
  toggleStatusWithConfirm(
    enable,
    () => providerDisableCopy(row.id, t),
    proceed,
  )
}

// computed, not a plain const: this was previously captured once at setup
// time, so column TITLES (unlike each cell's
// own render(), which re-evaluates t() every render) never re-translated
// after a locale switch — the sibling ProviderDetailPage.vue's keyColumns
// already gets this right via computed().
const columns = computed<DataTableColumns<Provider>>(() => [
  {
    title: columnTitle(t('providers.name'), t('providers.name_tip')),
    key: 'name',
    minWidth: 200,
    render: (row) => h('span', { class: 'provider-name-cell' }, row.name),
  },
  {
    title: columnTitle(t('providers.baseUrl'), t('providers.baseUrl_tip')),
    key: 'base_url',
    minWidth: 240,
    render: (row) =>
      h(
        'div',
        { class: 'provider-url-cell' },
        serviceAddresses(row).map((url) => h('div', { key: url, class: 'provider-url-line' }, url)),
      ),
  },
  {
    title: columnTitle(t('providers.protocolPrimary'), t('providers.protocolPrimary_tip')),
    key: 'provider_type',
    width: 150,
    render: (row) =>
      h(NTag, { size: 'small', bordered: false, round: true }, { default: () => t(`providers.protocol_${row.provider_type}`) }),
  },
  {
    title: columnTitle(t('providers.runningStatusColumn'), t('providers.runningStatusColumn_tip')),
    key: 'running_status',
    width: 210,
    render: (row) => {
      const display = runningStatusDisplay(row.running_status)
      return h(
        NTag,
        { size: 'small', bordered: false, type: display.type },
        { default: () => t(`providers.running${display.i18nKey}`) },
      )
    },
  },
  {
    title: columnTitle(t('providers.managementStatusColumn'), t('providers.managementStatusColumn_tip')),
    key: 'management_status',
    width: 120,
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
    align: 'center',
    width: 90,
    render: (row) =>
      h(
        'div',
        { onClick: (e: MouseEvent) => e.stopPropagation() },
        [
          h(
            ResponsiveDropdown,
            {
              trigger: 'click',
              placement: 'bottom-end',
              triggerText: t('common.actions'),
              height: 150,
              options: [
                { label: t('providers.editProvider'), key: 'edit' },
                { label: t('costs.detail.viewCost'), key: 'viewCost' },
              ],
              onSelect: (key: string) => {
                if (key === 'edit') openEditProvider(row)
                else if (key === 'viewCost') router.push(`/costs/providers/${row.id}`)
              },
            },
            {
              default: () =>
                h(
                  NButton,
                  { size: 'small', quaternary: true, circle: true },
                  { icon: () => h(MoreHorizontal, { size: 16 }) },
                ),
            },
          ),
        ],
      ),
  },
])
</script>

<style scoped>
:deep(.provider-name-cell) {
  font-weight: 650;
  color: var(--color-text);
}

:deep(.provider-url-cell) {
  display: flex;
  flex-direction: column;
  gap: 4px;
  color: var(--color-text-muted);
  font-size: var(--text-xs);
  font-family: var(--font-mono);
}

:deep(.provider-url-line) {
  word-break: break-all;
}
</style>
