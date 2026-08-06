<!-- frontend/src/views/apikeys/ApiKeyListPage.vue -->
<template>
  <div class="common-page">
    <PageHeader :eyebrow="t('apiKeys.eyebrow')" :title="t('apiKeys.pageTitle')" :description="t('apiKeys.pageDescription')">
      <template #actions>
        <n-button type="primary" @click="showCreate = true">
          <template #icon><Plus :size="16" /></template>
          {{ t('apiKeys.createButton') }}
        </n-button>
      </template>
    </PageHeader>

    <div class="filter-panel">
      <div class="filter-grid">
        <div class="filter-item filter-item--search">
          <n-input
            v-model:value="draft.query"
            :placeholder="t('apiKeys.searchPlaceholder')"
            clearable
            size="small"
            @keyup.enter="onSearch"
          >
            <template #prefix><Search :size="14" /></template>
          </n-input>
        </div>
        <div class="filter-item">
          <n-input
            v-model:value="draft.owner"
            :placeholder="t('apiKeys.filterOwner')"
            clearable
            size="small"
            @keyup.enter="onSearch"
          />
        </div>
        <FilterSelectField
          v-model:value="draft.status"
          :label="t('apiKeys.filterStatus')"
          :options="statusOptions"
          :placeholder="t('apiKeys.filterStatus')"
          size="small"
          width="100%"
          @update:value="onSearch"
        />
        <div class="filter-actions">
          <n-button size="small" type="primary" @click="onSearch">{{ t('apiKeys.search') }}</n-button>
          <n-button size="small" quaternary @click="onReset">{{ t('apiKeys.reset') }}</n-button>
        </div>
      </div>
    </div>

    <EmptyState v-if="!store.loading && store.list.length === 0 && !store.total && !draftValueLength" :icon="KeyRound" :title="t('apiKeys.listEmpty')">
      <template #action>
        <n-button type="primary" @click="showCreate = true">{{ t('apiKeys.createButton') }}</n-button>
      </template>
    </EmptyState>

    <div v-else class="data-table-wrapper">
      <ResponsiveDataTable
        :columns="columns"
        :data="store.list"
        :loading="store.loading"
        :scroll-x="1040"
        :row-key="(row: APIKey) => row.id"
        :pagination="pagination"
        remote
      />
    </div>

    <CreateKeyModal v-model:show="showCreate" @created="onCreated" />
    <EditKeyModal v-if="editingId" :key="editingId" :show="showEdit" :api-key-id="editingId" @update:show="onEditShow" @saved="onSaved" />
    <KeyOptimize
      v-if="compressKeyId"
      :key="compressKeyId"
      :show="showCompress"
      :api-key-id="compressKeyId"
      @update:show="openOptimizeShow"
      @saved="openOptimizeSaved"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, h, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { NButton, NInput, NTag, NTooltip, useDialog, useMessage, type DataTableColumns, type DropdownOption, type PaginationProps } from 'naive-ui'
import { KeyRound, Plus, Search, MoreHorizontal, Copy } from '@lucide/vue'
import { useApiKeysStore } from '../../store/apiKeys'
import { displayMessage } from '../../api/client'
import { columnTitle, STATUS_COL_WIDTH } from '../../utils/columnTitle'
import { formatMicros } from '../../utils/money'
import { useCCSwitchImport } from '../../composables/useCCSwitchImport'
import { copyToClipboard } from '../../utils/clipboard'
import { listModels, type Model } from '../../api/models'
import type { APIKey } from '../../api/apiKeys'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import CreateKeyModal from '../../components/apikeys/CreateKeyModal.vue'
import EditKeyModal from '../../components/apikeys/EditKeyModal.vue'
import KeyOptimize from '../../components/apikeys/KeyOptimize.vue'
import ResponsiveDataTable from '../../components/common/ResponsiveDataTable.vue'
import ResponsiveDropdown from '../../components/common/ResponsiveDropdown.vue'
import FilterSelectField from '../../components/common/FilterSelectField.vue'

const { t } = useI18n()
const router = useRouter()
const dialog = useDialog()
const message = useMessage()
const store = useApiKeysStore()
const { importToCCS } = useCCSwitchImport()
const showCreate = ref(false)
const showEdit = ref(false)
const editingId = ref<number | null>(null)
const showCompress = ref(false)
const compressKeyId = ref<number | null>(null)
const models = ref<Model[]>([])
// Live draft of the filter controls; the server filters only update when the
// user hits Enter or the Search button, matching the request-logs page.
const draft = reactive({ query: store.query, owner: store.owner, status: (store.status || null) as string | null })

const statusOptions = computed(() => [
  { label: t('apiKeys.statusActive'), value: 'active' },
  { label: t('apiKeys.statusExpired'), value: 'expired' },
  { label: t('apiKeys.statusBudgetExhausted'), value: 'budget_exhausted' },
  { label: t('apiKeys.statusRevoked'), value: 'revoked' },
])

const draftValueLength = computed(() => {
  return Object.values(draft).filter(e => !!e).length
})

onMounted(() => {
  void Promise.all([store.fetchList(), fetchModels()]).catch((err) => message.error(displayMessage(err, t)))
})

async function fetchModels() {
  const { list } = await listModels()
  models.value = list
}

async function reload() {
  try {
    await store.fetchList()
  } catch (err) {
    message.error(displayMessage(err, t))
  }
}

// setFilters resets the store to page 1, so a search always lands on the
// first page of results.
function onSearch() {
  store.setFilters({ query: draft.query.trim(), owner: draft.owner.trim(), status: draft.status ?? ''})
  void reload()
}
function onReset() {
  draft.query = ''
  draft.owner = ''
  draft.status = null
  store.setFilters({ query: '', owner: '', status: '' })
  void reload()
}

const pagination = computed<PaginationProps>(() => ({
  page: store.page,
  pageSize: store.pageSize,
  itemCount: store.total,
  showSizePicker: true,
  pageSizes: [10, 20, 50, 100],
  onChange: (page: number) => {
    store.setPage(page)
    void reload()
  },
  onUpdatePageSize: (pageSize: number) => {
    store.setPageSize(pageSize)
    void reload()
  },
}))

function budgetCell(row: APIKey): string {
  const spent = formatMicros(row.budget_spent_micros)
  if (row.budget_limit_micros == null) return `${spent} / ${t('apiKeys.unlimited')}`
  return `${spent} / ${formatMicros(row.budget_limit_micros)}`
}

function expiresCell(row: APIKey): string {
  if (row.expires_at == null) return t('apiKeys.noExpiry')
  return new Date(row.expires_at).toLocaleString()
}

function statusTagType(s: string): 'success' | 'warning' | 'error' {
  if (s === 'active') return 'success'
  if (s === 'revoked') return 'error'
  return 'warning'
}

function statusLabel(s: string): string {
  if (s === 'active') return t('apiKeys.statusActive')
  if (s === 'expired') return t('apiKeys.statusExpired')
  if (s === 'budget_exhausted') return t('apiKeys.statusBudgetExhausted')
  return t('apiKeys.statusRevoked')
}

function openEdit(id: number) {
  editingId.value = id
  showEdit.value = true
}

// Cancel/X close the modal via update:show=false — clear editingId too so
// v-if="editingId" flips off and the next openEdit (same row or another)
// remounts the modal and re-runs onMounted/fill. Without this, reopening the
// same row would reuse the stale form from the previous open.
function onEditShow(v: boolean) {
  showEdit.value = v
  if (!v) editingId.value = null
}

function openOptimize(row: APIKey) {
  compressKeyId.value = row.id
  showCompress.value = true
}

function openOptimizeShow(v: boolean) {
  showCompress.value = v
  if (!v) compressKeyId.value = null
}

function openOptimizeSaved() {
  showCompress.value = false
  compressKeyId.value = null
  message.success(t('apiKeys.saveSuccess'))
  void reload()
}

function confirmRevoke(row: APIKey) {
  dialog.warning({
    title: t('apiKeys.confirmRevokeTitle'),
    content: t('apiKeys.confirmRevokeContent'),
    positiveText: t('apiKeys.revoke'),
    negativeText: t('common.cancel'),
    onPositiveClick: async () => {
      try {
        await store.revoke(row.id)
        message.success(t('apiKeys.revokeSuccess'))
        await reload()
      } catch (err) {
        message.error(displayMessage(err, t))
      }
    },
  })
}

// copyPlaintext fetches the full key from the reveal endpoint and writes it to
// the clipboard. The backend returns 11016 for keys that predate the
// encrypted_key column — displayMessage surfaces that localized message; other
// failures fall through to the generic error toast.
//
// One reveal at a time across the whole table, not one per row. Two in flight
// write to the same clipboard in whatever order they come back, so the key the
// user ends up holding is the slower request's, not the row they clicked last —
// and either request's finally would clear the other's loading state.
const revealingId = ref<number | null>(null)
async function copyPlaintext(row: APIKey) {
  if (revealingId.value !== null) return
  revealingId.value = row.id
  try {
    const res = await store.fetchPlaintext(row.id)
    // copyToClipboard handles the non-secure-context (plain HTTP) fallback to
    // execCommand internally; a false return means the write truly failed.
    if (await copyToClipboard(res.plaintext_key)) {
      message.success(t('apiKeys.copied'))
    } else {
      showPlaintextToCopyByHand(res.plaintext_key)
    }
  } catch (err) {
    // Fetch-side failure (incl. the legacy-key 11016) — distinct from a
    // clipboard-write failure, which is handled above.
    message.error(displayMessage(err, t))
  } finally {
    revealingId.value = null
  }
}

// showPlaintextToCopyByHand puts the key somewhere the user can select it.
//
// Every automatic path has already failed by the time this runs: the Clipboard
// API is unavailable or refused, and execCommand did not work either. Telling
// somebody to "select and copy manually" while the key exists only in a local
// variable leaves them with a button that does nothing and no way to try again
// — the reveal itself is repeatable, but they have no reason to think a second
// click behaves differently.
function showPlaintextToCopyByHand(plaintext: string) {
  dialog.warning({
    title: t('apiKeys.copyFailed'),
    content: () =>
      h(NInput, {
        value: plaintext,
        readonly: true,
        type: 'textarea',
        autosize: { minRows: 2, maxRows: 4 },
        onFocus: (e: FocusEvent) => (e.target as HTMLTextAreaElement | null)?.select(),
      }),
    positiveText: t('common.close'),
  })
}

function onCreated() {
  message.success(t('apiKeys.createSuccess'))
  void reload()
}

function onSaved() {
  showEdit.value = false
  // Reset editingId so the modal unmounts (v-if="editingId"); reopening it
  // remounts and re-runs onMounted/fill instead of showing stale form state.
  editingId.value = null
  message.success(t('apiKeys.saveSuccess'))
  void reload()
}

function firstUsableModel(row: APIKey): string | undefined {
  if (row.allow_all_models) return models.value[0]?.name
  const firstAllowedModelId = row.model_ids[0]
  return models.value.find((model) => model.id === firstAllowedModelId)?.name
}

function importKeyToCCS(row: APIKey) {
  importToCCS({
    name: `YoloRouter${row.owner_label ? ` - ${row.owner_label}` : ''}`,
    model: firstUsableModel(row),
  })
}

function rowActions(row: APIKey): DropdownOption[] {
  // Revoked keys only keep cost view; config, optimize, import, and revoke drop out.
  const revoked = row.display_status === 'revoked'
  return [
    ...(revoked ? [] : [
      { label: t('apiKeys.editLimits'), key: 'edit' },
    ]),
    { label: t('costs.detail.viewCost'), key: 'look' },
    ...(revoked ? [] : [
      { label: t('costOptimization.title'), key: 'optimize' },
      { label: t('ccswitch.importAction'), key: 'importCCSImport' },
      { type: 'divider', key: 'd' },
      { label: t('apiKeys.revoke'), key: 'delete', props: { style: 'color: var(--color-danger)' } },
    ]),
  ]
}

const columns = computed<DataTableColumns<APIKey>>(() => [
  {
    title: columnTitle(t('apiKeys.keyPrefixColumn'), t('apiKeys.keyPrefixColumn_tip')),
    key: 'key_prefix',
    minWidth: 180,
    render: (row) =>
      h('div', { class: 'prefix-cell' }, [
        h('span', { class: 'mono-cell' }, `${row.key_prefix}…`),
        h(
          NTooltip,
          { trigger: 'hover' },
          {
            trigger: () =>
              h(
                NButton,
                {
                  size: 'tiny',
                  quaternary: true,
                  circle: true,
                  loading: revealingId.value === row.id,
                  onClick: () => copyPlaintext(row),
                },
                { icon: () => h(Copy, { size: 14 }) },
              ),
            default: () => t('apiKeys.copyFullKey'),
          },
        ),
      ]),
  },
  {
    title: columnTitle(t('apiKeys.ownerColumn'), t('apiKeys.ownerColumn_tip')),
    key: 'owner_label',
    minWidth: 120,
    render: (row) => row.owner_label || '—',
  },
  {
    title: columnTitle(t('apiKeys.remarkColumn'), t('apiKeys.remarkColumn_tip')),
    key: 'remark',
    minWidth: 160,
    render: (row) => row.remark || '—',
  },
  {
    title: columnTitle(t('apiKeys.statusColumn'), t('apiKeys.statusColumn_tip')),
    key: 'display_status',
    width: STATUS_COL_WIDTH,
    render: (row) =>
      h(NTag, { size: 'small', bordered: false, type: statusTagType(row.display_status) }, { default: () => statusLabel(row.display_status) }),
  },
  {
    title: columnTitle(t('apiKeys.budgetColumn'), t('apiKeys.budgetColumn_tip')),
    key: 'budget',
    width: 170,
    render: (row) => budgetCell(row),
  },
  {
    title: columnTitle(t('apiKeys.expiresColumn'), t('apiKeys.expiresColumn_tip')),
    key: 'expires_at',
    width: 200,
    render: (row) => expiresCell(row),
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
          options: rowActions(row),
          onSelect: (key: string) => {
            if (key === 'edit') openEdit(row.id)
            else if (key === 'look') router.push(`/costs/keys/${row.id}`)
            else if (key === 'optimize') openOptimize(row)
            else if (key === 'delete') confirmRevoke(row)
            else if (key === 'importCCSImport') importKeyToCCS(row)
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
  }
])
</script>

<style scoped>
:deep(.prefix-cell) {
  display: flex;
  align-items: center;
  gap: 6px;
}
:deep(.mono-cell) {
  font-family: var(--font-mono, monospace);
  font-weight: 600;
  color: var(--color-text);
}
</style>
