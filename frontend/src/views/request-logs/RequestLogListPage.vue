<!-- frontend/src/views/request-logs/RequestLogListPage.vue
     Request-log list. Server-side paged with a filter set
     matching what the backend handler actually accepts
     (request_log_handler.go): request_id / model_name / provider_id /
     status_class / is_stream / start / end. Owner_label and api_key prefix
     filtering is not wired up yet, because the backend
     exposes the filter as api_key_id (an admin-facing internal id, not a
     user-typable string) — owner/free-text filtering lands with a later
     backend add, not by silently wiring up a UI control that doesn't work.

     Click row → /request-logs/:requestId detail page. Export CSV streams
     the current filter via the same params. -->
<template>
  <div class="common-page">
    <PageHeader :eyebrow="t('requestLogs.eyebrow')" :title="t('requestLogs.pageTitle')" :description="t('requestLogs.pageDescription')">
      <template #actions>
        <NButton :loading="exporting" :disabled="exporting || loading" @click="onExport">
          <template #icon><Download :size="16" /></template>
          {{ t('requestLogs.exportCsv') }}
        </NButton>
      </template>
    </PageHeader>

    <!-- Filter row. NDatePicker / NSelect are not in main.ts's create()
         list, so they're imported explicitly below. Silently rendering as
         unknown elements is the worst-case failure mode here, not a
         typecheck error. -->
    <div class="filter-panel">
      <div class="filter-grid">
        <div class="filter-item filter-item--grow">
          <NInput
            v-model:value="filter.request_id"
            :placeholder="t('requestLogs.filterRequestId')"
            clearable
            size="small"
            @keyup.enter="onSearch"
            @update:value="onRequestIdInput"
          >
            <template #prefix><Search :size="14" /></template>
          </NInput>
        </div>
        <div class="filter-item filter-item--grow">
          <NInput
            v-model:value="filter.model_name"
            :placeholder="t('requestLogs.filterModel')"
            clearable
            size="small"
            @keyup.enter="onSearch"
            @update:value="onModelNameInput"
          />
        </div>
        <FilterSelectField
          :label="t('requestLogs.filterCaller')"
          :value="filter.api_key_id"
          :options="callerOptions"
          :placeholder="t('requestLogs.allFilterCaller')"
          filterable
          width="100%"
          @update:value="onCallerChange"
        />
        <FilterSelectField
          :label="t('requestLogs.filterProvider')"
          :value="filter.provider_id"
          :options="providerOptions"
          :placeholder="t('requestLogs.allFilterProvider')"
          width="100%"
          @update:value="onProviderChange"
        />
        <FilterSelectField
          :label="t('requestLogs.filterStatus')"
          :value="filter.status"
          :options="statusOptions"
          :placeholder="t('requestLogs.allFilterStatus')"
          width="100%"
          @update:value="onStatusChange"
        />
        <FilterSelectField
          :label="t('requestLogs.filterStream')"
          :value="streamSelect"
          :options="streamOptions"
          :placeholder="t('requestLogs.allFilterStream')"
          width="100%"
          @update:value="onStreamChange"
        />
        <FilterSelectField
          :label="t('requestLogs.filterCostKnown')"
          :value="costSelect"
          :options="costOptions"
          :placeholder="t('requestLogs.allFilterCostKnown')"
          width="100%"
          @update:value="onCostKnownChange"
        />
        <div class="filter-item filter-item--range">
          <!-- Desktop: a single datetimerange picker. On mobile the range
               variant is too wide to fit, so it's split into two standalone
               datetime pickers (start / end) driven by the same startTime /
               endTime refs the range picker writes through. -->
          <NDatePicker
            v-if="!isMobile"
            :value="timeRange"
            type="datetimerange"
            clearable
            size="small"
            :shortcuts="rangeShortcuts"
            :placeholder="t('requestLogs.filterTimeRange')"
            @update:value="onRangeChange"
          />
          <div v-else class="filter-range-split">
            <NDatePicker
              v-model:value="startTime"
              type="datetime"
              clearable
              size="small"
              :placeholder="t('requestLogs.filterStartTime')"
              :is-date-disabled="disableAfterEnd"
              @update:value="onSearch"
            />
            <NDatePicker
              v-model:value="endTime"
              type="datetime"
              clearable
              size="small"
              :placeholder="t('requestLogs.filterEndTime')"
              :is-date-disabled="disableBeforeStart"
              @update:value="onSearch"
            />
          </div>
        </div>
        <div class="filter-actions">
          <NButton size="small" type="primary" @click="onSearch">{{ t('requestLogs.search') }}</NButton>
          <NButton size="small" quaternary @click="onReset">{{ t('requestLogs.reset') }}</NButton>
        </div>
      </div>
    </div>

    <EmptyState v-if="!loading && rows.length === 0" :icon="FileSearch" :title="t('requestLogs.listEmpty')" />
    <div v-else class="data-table-wrapper">
      <ResponsiveDataTable
        :columns="columns"
        :data="rows"
        :loading="loading"
        :scroll-x="1630"
        :row-key="(row: RequestLogRow) => row.request_id"
        :row-props="rowProps"
        :pagination="pagination"
        remote
      >
        <template #empty>
          <EmptyState :icon="FileSearch" :title="t('requestLogs.listEmpty')" />
        </template>
      </ResponsiveDataTable>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
  NButton,
  NDatePicker,
  NInput,
  NTag,
  useMessage,
  type DataTableColumns,
  type PaginationProps,
  type SelectOption,
} from 'naive-ui'
import { Download, FileSearch, Search } from '@lucide/vue'
import {
  listRequestLogs,
  exportRequestLogsCSV,
  type RequestLogRow,
  type RequestLogListParams,
  type StatusClass,
} from '../../api/requestLogs'
import { listProviders, type Provider } from '../../api/providers'
import { listAPIKeys, toAPIKeyOptions, type APIKey } from '../../api/apiKeys'
import { displayMessage } from '../../api/client'
import { formatMicros } from '../../utils/money'
import { columnTitle } from '../../utils/columnTitle'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import FilterSelectField from '../../components/common/FilterSelectField.vue'
import ResponsiveDataTable from '../../components/common/ResponsiveDataTable.vue'
import StatusClassTag from '../../components/request-logs/StatusClassTag.vue'
import { useIsMobile } from '../../composables/useIsMobile'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const message = useMessage()

// Filter state — every field the backend actually accepts. `timeRange` is
// the matching pair (start/end) held together as a tuple because
// NDatePicker's datetimerange mode emits them as one value. We split the
// tuple into RFC3339 strings in buildListParams before sending.
interface ListFilter {
  request_id: string
  model_name: string
  api_key_id: number | null
  provider_id: number | null
  status: StatusClass | null
  is_stream: boolean | null
  cost_known: boolean | null
}
const filter = reactive<ListFilter>({
  request_id: '',
  model_name: '',
  api_key_id: null,
  provider_id: null,
  status: null,
  is_stream: null,
  cost_known: null,
})
// Stream filter UI value. null means "no filter" (cleared select, matches
// the placeholder's "all streams" wording); 'stream' / 'non-stream' map to
// filter.is_stream = true / false. Wired via :value + @update:value rather
// than v-model so the null → is_stream=null mapping happens in one place
// (onStreamChange).
const streamSelect = ref<'stream' | 'non-stream' | null>(null)
// Same controlled-input shape as streamSelect: the UI value decodes to
// filter.cost_known = true / false / null in one place.
const costSelect = ref<'known' | 'unknown' | null>(null)
// Start / end are the source of truth for the time filter. Desktop binds a
// single datetimerange picker through the `timeRange` computed below; mobile
// binds these two refs directly to standalone datetime pickers. Keeping the
// pair split (rather than a [start, end] tuple) lets the mobile UI set just
// one bound, and lets buildListParams send start / end independently.
const startTime = ref<number | null>(null)
const endTime = ref<number | null>(null)

// Reactive mobile flag — the same breakpoint composable the rest of the app
// uses. Drives the datetimerange (desktop) vs. two datetime pickers (mobile)
// switch in the template.
const isMobile = useIsMobile()

// Adapter for the desktop datetimerange picker: it holds a [start, end] tuple
// or null, so surface both bounds together and only when both are present.
const timeRange = computed<[number, number] | null>(() =>
  startTime.value != null && endTime.value != null ? [startTime.value, endTime.value] : null,
)

// datetimerange emits the whole tuple (or null on clear); fan it back out to
// the two source refs, then search. Shortcuts flow through here too.
function onRangeChange(v: [number, number] | null) {
  startTime.value = v ? v[0] : null
  endTime.value = v ? v[1] : null
  void onSearch()
}

// Cross-bound guards for the two mobile pickers so start can't exceed end and
// vice versa. NDatePicker's is-date-disabled works at day granularity, which
// is enough to keep the pair coherent.
function disableAfterEnd(ts: number): boolean {
  return endTime.value != null && ts > endTime.value
}
function disableBeforeStart(ts: number): boolean {
  return startTime.value != null && ts < startTime.value
}

// Flags tracking whether model_name / request_id originated from a URL
// query param (a deep link from a cost detail page). Values sourced that
// way are EXACT identifiers — analytics may carry intentional surrounding
// whitespace — so they must reach the backend verbatim. The submit-time
// .trim() in buildListParams serves typed input only, where stray
// whitespace is unintended; these flags branch that behavior.
const querySourcedModelName = ref(false)
const querySourcedRequestId = ref(false)

const rows = ref<RequestLogRow[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const loading = ref(false)
const exporting = ref(false)

// Providers come from the same admin providers endpoint used by the
// providers store; loaded once on mount. We don't go through the Pinia
// store here because the request-logs page is read-only w.r.t. providers
// and doesn't need the store's race-guard / mutation actions — a one-shot
// fetch is simpler and avoids coupling this page to provider CRUD state.
const providers = ref<Provider[]>([])
const providerOptions = computed<SelectOption[]>(() =>
  providers.value.map((p) => ({ label: p.name, value: p.id })),
)

// The "caller" filter reuses the existing api_key_id filter param — the
// backend already filters request_logs by api_key_id, so no backend change is
// needed. Options are the API keys (owner_label + key_prefix to disambiguate
// keys that share an owner label or have none). Revoked keys are kept in the
// list so historical logs of a since-revoked key stay filterable. One-shot
// fetch on mount, same rationale as loadProviders above; 200 covers every
// realistic v0.1 key count without a remote-search handshake.
const apiKeys = ref<APIKey[]>([])
const callerOptions = computed<SelectOption[]>(() => toAPIKeyOptions(apiKeys.value))

const statusOptions = computed<SelectOption[]>(() => ([
  { label: t('requestLogs.status_success'), value: 'success' },
  { label: t('requestLogs.status_failed'), value: 'failed' },
  { label: t('requestLogs.status_partial'), value: 'partial' },
  { label: t('requestLogs.status_cancelled'), value: 'cancelled' },
  { label: t('requestLogs.status_rejected'), value: 'rejected' },
]))

const streamOptions = computed<SelectOption[]>(() => ([
  { label: t('requestLogs.stream_true'), value: 'stream' },
  { label: t('requestLogs.stream_false'), value: 'non-stream' },
]))

const costOptions = computed<SelectOption[]>(() => ([
  { label: t('requestLogs.costKnown_true'), value: 'known' },
  { label: t('requestLogs.costKnown_false'), value: 'unknown' },
]))

// Preset shortcuts for the date-range picker: today / yesterday / last 7
// days / last 30 days. End is set to "now" for the rolling windows so the
// preset matches the admin's mental model ("last 7 days" includes today),
// not "midnight 7 days ago to midnight now".
const rangeShortcuts = computed<Record<string, () => [number, number]>>(() => ({
  [t('requestLogs.rangeToday')]: () => {
    const now = Date.now()
    const startOfToday = new Date()
    startOfToday.setHours(0, 0, 0, 0)
    return [startOfToday.getTime(), now]
  },
  [t('requestLogs.rangeYesterday')]: () => {
    const start = new Date()
    start.setDate(start.getDate() - 1)
    start.setHours(0, 0, 0, 0)
    const end = new Date()
    end.setHours(0, 0, 0, 0)
    return [start.getTime(), end.getTime()]
  },
  [t('requestLogs.range7d')]: () => [Date.now() - 7 * 24 * 60 * 60 * 1000, Date.now()],
  [t('requestLogs.range30d')]: () => [Date.now() - 30 * 24 * 60 * 60 * 1000, Date.now()],
}))

let searchTimer: ReturnType<typeof setTimeout> | null = null
onBeforeUnmount(() => {
  if (searchTimer) {
    clearTimeout(searchTimer)
    searchTimer = null
  }
})

// URL query keys this page knows how to ingest. Used by hasRelevantQuery
// to decide whether mount should apply the query before the first load.
const RELEVANT_QUERY_KEYS = [
  'request_id', 'model_name', 'api_key_id', 'provider_id',
  'status', 'is_stream', 'cost_known', 'start', 'end',
] as const

function hasRelevantQuery(): boolean {
  const q = route.query
  return RELEVANT_QUERY_KEYS.some((k) => {
    const v = q[k]
    return v != null && v !== ''
  })
}

// applyQueryFilter maps deep-link query params onto the local filter model.
// Cost detail pages emit /request-logs?api_key_id=X&start=...&end=... etc.
// — start/end arrive as RFC3339 strings and are converted to the epoch-ms
// tuple NDatePicker holds. is_stream's UI value mirrors the streamSelect
// ref ('stream' | 'non-stream' | null). model_name and request_id are
// preserved verbatim (see querySourced* flags).
function applyQueryFilter() {
  const q = route.query
  if (typeof q.request_id === 'string' && q.request_id) {
    filter.request_id = q.request_id
    querySourcedRequestId.value = true
  }
  if (typeof q.model_name === 'string' && q.model_name) {
    filter.model_name = q.model_name
    querySourcedModelName.value = true
  }
  if (typeof q.api_key_id === 'string' && q.api_key_id) {
    const n = Number(q.api_key_id)
    if (!Number.isNaN(n)) filter.api_key_id = n
  }
  if (typeof q.provider_id === 'string' && q.provider_id) {
    const n = Number(q.provider_id)
    if (!Number.isNaN(n)) filter.provider_id = n
  }
  if (typeof q.status === 'string' && q.status) {
    filter.status = q.status as StatusClass
  }
  if (typeof q.cost_known === 'string') {
    const v: 'known' | 'unknown' | null =
      q.cost_known === 'true' ? 'known'
        : q.cost_known === 'false' ? 'unknown'
          : null
    costSelect.value = v
    filter.cost_known = v === 'known' ? true : v === 'unknown' ? false : null
  }
  if (typeof q.is_stream === 'string') {
    const v: 'stream' | 'non-stream' | null =
      q.is_stream === 'true' ? 'stream'
        : q.is_stream === 'false' ? 'non-stream'
          : null
    streamSelect.value = v
    filter.is_stream = v === 'stream' ? true : v === 'non-stream' ? false : null
  }
  if (typeof q.start === 'string' && typeof q.end === 'string' && q.start && q.end) {
    const startMs = Date.parse(q.start)
    const endMs = Date.parse(q.end)
    if (!Number.isNaN(startMs) && !Number.isNaN(endMs)) {
      startTime.value = startMs
      endTime.value = endMs
    }
  }
}

onMounted(() => {
  // Ingest URL query params (deep links from cost detail pages) before the
  // first load. Guarded so a plain mount (no query) keeps its single
  // initial reload — applying an empty query would be a no-op but the
  // guard makes the intent explicit and protects against future side
  // effects creeping into applyQueryFilter.
  if (hasRelevantQuery()) {
    applyQueryFilter()
  }
  void reload().catch((err) => message.error(displayMessage(err, t)))
  void loadProviders().catch((err) => message.error(displayMessage(err, t)))
  void loadCallers().catch((err) => message.error(displayMessage(err, t)))
})

async function loadProviders() {
  const { list } = await listProviders()
  providers.value = list
}

async function loadCallers() {
  const { list } = await listAPIKeys({ q: '', owner: '', status: '', page: 1, pageSize: 200 })
  apiKeys.value = list
}

function buildListParams(): RequestLogListParams {
  const params: RequestLogListParams = {
    page: page.value,
    page_size: pageSize.value,
  }
  // request_id / model_name: when sourced from a URL query, preserve the
  // value verbatim (no trim) — analytics-sourced identifiers may carry
  // intentional surrounding whitespace. For typed input, keep the existing
  // trim to protect against stray whitespace producing empty params.
  if (querySourcedRequestId.value) {
    if (filter.request_id) params.request_id = filter.request_id
  } else if (filter.request_id.trim()) {
    params.request_id = filter.request_id.trim()
  }
  if (querySourcedModelName.value) {
    if (filter.model_name) params.model_name = filter.model_name
  } else if (filter.model_name.trim()) {
    params.model_name = filter.model_name.trim()
  }
  if (filter.api_key_id != null) params.api_key_id = filter.api_key_id
  if (filter.provider_id != null) params.provider_id = filter.provider_id
  if (filter.status) params.status = filter.status
  if (filter.is_stream != null) params.is_stream = filter.is_stream
  // start / end are independent bounds — on mobile the user may set only one.
  if (startTime.value != null) params.start = new Date(startTime.value).toISOString()
  if (endTime.value != null) params.end = new Date(endTime.value).toISOString()
  return params
}

// Monotonic fetch token: a stale list response can't clobber a newer one
// if the user fires a second search before the first resolves. Same guard
// pattern the API-key/models stores use, kept inline because this page
// doesn't have a Pinia store — the request-log list is page-local state.
let fetchId = 0
async function reload() {
  const currentId = ++fetchId
  loading.value = true
  try {
    const res = await listRequestLogs(buildListParams())
    if (currentId !== fetchId) return
    rows.value = res.list
    total.value = res.total
  } catch (err) {
    if (currentId !== fetchId) return
    throw err
  } finally {
    if (currentId === fetchId) loading.value = false
  }
}

// Debounced search for free-text inputs (request_id, model_name). The two
// NSelect filters call onSearch directly on @update:value, so this debounce
// only fires for keystroke-level changes.
function onFilterChange() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    void onSearch()
  }, 300)
}

// When the user edits a deep-linked request_id / model_name, the value is no
// longer the verbatim query-sourced one — clear its flag so the debounced
// search resumes the normal trim path (matching plain typed searches).
function onRequestIdInput() {
  querySourcedRequestId.value = false
  onFilterChange()
}
function onModelNameInput() {
  querySourcedModelName.value = false
  onFilterChange()
}

async function onSearch() {
  if (searchTimer) {
    clearTimeout(searchTimer)
    searchTimer = null
  }
  page.value = 1
  try {
    await reload()
  } catch (err) {
    message.error(displayMessage(err, t))
  }
}

function onReset() {
  filter.request_id = ''
  filter.model_name = ''
  filter.api_key_id = null
  filter.provider_id = null
  filter.status = null
  filter.is_stream = null
  streamSelect.value = null
  filter.cost_known = null
  costSelect.value = null
  startTime.value = null
  endTime.value = null
  // Drop the verbatim-no-trim override too, so post-reset typed searches
  // return to the normal submit-time trim behavior.
  querySourcedModelName.value = false
  querySourcedRequestId.value = false
  page.value = 1
  void reload().catch((err) => message.error(displayMessage(err, t)))
}

async function onExport() {
  exporting.value = true
  try {
    await exportRequestLogsCSV(buildListParams())
    message.success(t('requestLogs.exportSuccess'))
  } catch (err) {
    message.error(displayMessage(err, t) || t('requestLogs.exportFailed'))
  } finally {
    exporting.value = false
  }
}

// onStreamChange decodes the UI value into the boolean-or-null the backend
// expects, then fires a search. null = cleared select = no filter. Wired
// via :value + @update:value rather than v-model so the null → null mapping
// happens in one place.
function onStreamChange(v: 'stream' | 'non-stream' | null) {
  streamSelect.value = v
  filter.is_stream = v === 'stream' ? true : v === 'non-stream' ? false : null
  void onSearch()
}

function onCostKnownChange(v: 'known' | 'unknown' | null) {
  costSelect.value = v
  filter.cost_known = v === 'known' ? true : v === 'unknown' ? false : null
  void onSearch()
}

// FilterSelectField is a controlled input (no v-model), so each select's
// handler writes the reactive filter field and then searches — mirroring the
// old NSelect `@update:value="onSearch"` after v-model wrote the value.
function onCallerChange(v: number | null) {
  filter.api_key_id = v
  void onSearch()
}

function onProviderChange(v: number | null) {
  filter.provider_id = v
  void onSearch()
}

function onStatusChange(v: StatusClass | null) {
  filter.status = v
  void onSearch()
}

const pagination = computed<PaginationProps>(() => ({
  page: page.value,
  pageSize: pageSize.value,
  itemCount: total.value,
  showSizePicker: true,
  pageSizes: [10, 20, 50, 100],
  onChange: (p: number) => {
    page.value = p
    void reload().catch((err) => message.error(displayMessage(err, t)))
  },
  onUpdatePageSize: (ps: number) => {
    pageSize.value = ps
    page.value = 1
    void reload().catch((err) => message.error(displayMessage(err, t)))
  },
}))

function goDetail(requestId: string) {
  router.push(`/request-logs/${encodeURIComponent(requestId)}`)
}

function rowProps(row: RequestLogRow) {
  return {
    style: 'cursor: pointer',
    onClick: () => goDetail(row.request_id),
  }
}

// ---------- Render helpers ----------

function formatTime(iso: string): string {
  // Locale-aware short timestamp for table density; detail page uses a
  // longer format. The toLocaleString options are kept inline rather than
  // extracted to a util because the table + detail page intentionally use
  // different granularities.
  return new Date(iso).toLocaleString(undefined, {
    year: '2-digit',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

function streamCell(row: RequestLogRow) {
  return h(
    NTag,
    { size: 'small', bordered: false, type: row.is_stream ? 'info' : 'default' },
    { default: () => (row.is_stream ? t('requestLogs.stream_true') : t('requestLogs.stream_false')) },
  )
}

function tokenCell(row: RequestLogRow) {
  const main = h('span', { class: 'token-main' }, `${row.input_tokens} / ${row.output_tokens}`)
  // Second line surfaces cache write / read tokens, but only when the request
  // actually had cache activity — a bare "0 / 0" on every non-cached row would
  // be noise. Keeps the common (no-cache) row visually identical to before.
  if (row.cache_write_tokens === 0 && row.cache_read_tokens === 0) {
    return h('div', { class: 'token-cell' }, [main])
  }
  const cache = h(
    'span',
    { class: 'token-cache' },
    `${t('requestLogs.cacheTokensShort')} ${row.cache_write_tokens} / ${row.cache_read_tokens}`,
  )
  return h('div', { class: 'token-cell token-cell--stacked' }, [main, cache])
}

function costCell(row: RequestLogRow) {
  if (!row.cost_known) {
    return h(NTag, { size: 'small', bordered: false, type: 'default' }, { default: () => t('requestLogs.costUnknown') })
  }
  return h('span', { class: 'cost-cell' }, formatMicros(row.cost_micros))
}

function attemptsCell(row: RequestLogRow) {
  // The backend list-row DTO exposes a single `attempts` count — total
  // candidate tries, including both key rotations within a candidate and
  // candidate failovers. "key rotation" and "failover" are conceptually two
  // columns, but the wire schema collapses them into one number; the
  // detail page's attempts_detail array shows the full sequence so the
  // breakdown is still recoverable per-request. A zero-count badge helps
  // spot pre-route rejects (no attempt ever fired).
  if (row.attempts === 0) {
    return h(NTag, { size: 'small', bordered: false, type: 'default' }, { default: () => '0' })
  }
  // >1 means a switch happened; tag amber so the admin's eye lands on
  // failover chains. Exactly 1 = clean single-try success, no decoration.
  if (row.attempts > 1) {
    return h(NTag, { size: 'small', bordered: false, type: 'warning' }, { default: () => String(row.attempts) })
  }
  return h('span', { class: 'attempts-cell' }, String(row.attempts))
}

const columns = computed<DataTableColumns<RequestLogRow>>(() => [
  {
    title: columnTitle(t('requestLogs.col_created'), t('requestLogs.col_created_tip')),
    key: 'created_at',
    width: 180,
    render: (row) => h('span', { class: 'mono-cell' }, formatTime(row.created_at)),
  },
  {
    title: columnTitle(t('requestLogs.col_requestId'), t('requestLogs.col_requestId_tip')),
    key: 'request_id',
    minWidth: 200,
    render: (row) => h('span', { class: 'mono-cell request-id-cell' }, row.request_id),
  },
  {
    title: columnTitle(t('requestLogs.col_owner'), t('requestLogs.col_owner_tip')),
    key: 'owner_label',
    minWidth: 120,
    render: (row) => row.owner_label || '—',
  },
  {
    title: columnTitle(t('requestLogs.col_model'), t('requestLogs.col_model_tip')),
    key: 'model_name',
    minWidth: 160,
    render: (row) => h('span', { class: 'model-cell' }, row.model_name),
  },
  {
    title: columnTitle(t('requestLogs.col_provider'), t('requestLogs.col_provider_tip')),
    key: 'provider_name',
    minWidth: 140,
    render: (row) => row.provider_name || '—',
  },
  {
    title: columnTitle(t('requestLogs.col_stream'), t('requestLogs.col_stream_tip')),
    key: 'is_stream',
    width: 110,
    align: 'center',
    render: (row) => streamCell(row),
  },
  {
    title: columnTitle(t('requestLogs.col_status'), t('requestLogs.col_status_tip')),
    key: 'status_class',
    width: 130,
    align: 'center',
    render: (row) => h(StatusClassTag, { status: row.status_class }),
  },
  {
    title: columnTitle(t('requestLogs.col_attempts'), t('requestLogs.col_attempts_tip')),
    key: 'attempts',
    width: 110,
    align: 'center',
    render: (row) => attemptsCell(row),
  },
  {
    title: columnTitle(t('requestLogs.col_tokens'), t('requestLogs.col_tokens_tip')),
    key: 'tokens',
    width: 170,
    align: 'right',
    render: (row) => tokenCell(row),
  },
  {
    title: columnTitle(t('requestLogs.col_cost'), t('requestLogs.col_cost_tip')),
    key: 'cost',
    width: 110,
    align: 'right',
    render: (row) => costCell(row),
  },
  {
    title: columnTitle(t('requestLogs.col_duration'), t('requestLogs.col_duration_tip')),
    key: 'duration_ms',
    width: 100,
    align: 'right',
    render: (row) => h('span', { class: 'mono-cell' }, formatDuration(row.duration_ms)),
  },
  {
    // Actions column — no tooltip. The row
    // itself is already clickable end-to-end (rowProps), so this button is
    // an explicit affordance, not the only entry point.
    title: t('common.actions'),
    key: 'actions',
    width: 100,
    align: 'center',
    render: (row) =>
      h(
        'div',
        { onClick: (e: MouseEvent) => e.stopPropagation() },
        [
          h(
            NButton,
            {
              size: 'small',
              text: true,
              type: 'primary',
              onClick: () => goDetail(row.request_id),
            },
            { default: () => t('requestLogs.viewDetail') },
          ),
        ],
      ),
  },
])
</script>

<style scoped>
/* Filter-bar styles (.filter-panel / .filter-grid / .filter-item /
   .filter-actions) are the canonical shared classes in styles/global.less —
   this page is the reference every other list page's filter bar matches. */

/* Mobile-only: the datetimerange picker is split into two stacked datetime
   pickers so each fits the narrow viewport. .filter-item--range already goes
   full-width under the global mobile breakpoint (@mobile-breakpoint). */
.filter-range-split {
  display: flex;
  gap: var(--space-2);
  width: 100%;
}

.filter-range-split :deep(.n-date-picker) {
  width: 100%;
}

:deep(.mono-cell) {
  font-family: var(--font-mono, monospace);
  font-variant-numeric: tabular-nums;
  font-size: var(--text-xs);
  color: var(--color-text);
}

:deep(.request-id-cell) {
  color: var(--color-text-secondary);
}

:deep(.model-cell) {
  font-weight: 600;
  color: var(--color-text);
}

:deep(.token-cell),
:deep(.cost-cell),
:deep(.attempts-cell) {
  font-variant-numeric: tabular-nums;
  font-size: var(--text-xs);
}

:deep(.token-cell--stacked) {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  line-height: 1.3;
}

:deep(.token-cache) {
  color: var(--color-text-muted, var(--color-text-secondary));
  font-size: var(--text-2xs, 11px);
}

:deep(.cost-cell) {
  font-weight: 600;
}
</style>
