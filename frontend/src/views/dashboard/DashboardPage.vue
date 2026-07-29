<!-- frontend/src/views/dashboard/DashboardPage.vue
     Overview dashboard. Renders the KPI cards, trend chart, top callers,
     recent failures, and upstream status for the selected time range — all
     from one GET /api/admin/dashboard round trip.

     All sections share a single loading state because the dashboard envelope
     is fetched atomically. On a failed reload the prior range's data is
     cleared (rather than left on screen under a new range label) so KPIs and
     the selector never disagree. -->
<template>
  <div class="dashboard-page">
    <PageHeader :eyebrow="t('dashboard.eyebrow')" :title="t('dashboard.pageTitle')" :description="t('dashboard.pageDescription')">
      <template #actions>
        <TimeRangeSelect v-model="timeRange" :preset="preset" @update:preset="onPresetChange" />
      </template>
    </PageHeader>

    <!-- Setup guidance banner. Shown only before any real traffic has been
         recorded, and adapts to the current setup step so a fresh deployment
         always has a clear next action instead of a blank overview. -->
    <div v-if="!loading && setupStep" class="setup-banner">
      <div class="icon-tile">
        <component :is="setupStep.icon" :size="22" :stroke-width="1.75" />
      </div>
      <div class="setup-banner__body">
        <h3 class="setup-banner__title">{{ t(setupStep.titleKey) }}</h3>
        <p class="setup-banner__desc">{{ t(setupStep.descKey) }}</p>
        <p v-if="setupStep.hint" class="setup-banner__desc">
          <span> {{ t(setupStep.hint) }}</span>
           <NButton
              size="tiny"
              secondary
              @click="onCopy()"
            >
            {{reqUrl }}
          </NButton>
        </p>
      </div>
      <NButton v-if="setupStep.ctaKey && setupStep.to" type="primary" @click="router.push(setupStep.to)">{{ t(setupStep.ctaKey) }}</NButton>
    </div>

    <!-- KPI cards row -->
    <div class="kpi-row">
      <div class="kpi">
        <div class="kpi__icon kpi__icon--accent">
          <Activity :size="18" />
        </div>
        <div class="kpi__body">
          <div class="kpi__label">
            <HelpLabel :tip="t('dashboard.callsCard_tip')">{{ t('dashboard.callsCard') }}</HelpLabel>
          </div>
          <div class="kpi__value">{{ formatNumber(data?.today.calls ?? 0) }}</div>
          <div class="kpi__sub">{{ t('dashboard.callsCard_sub') }}</div>
        </div>
      </div>

      <div class="kpi">
        <div class="kpi__icon kpi__icon--success">
          <Coins :size="18" />
        </div>
        <div class="kpi__body">
          <div class="kpi__label">
            <HelpLabel :tip="t('dashboard.costCard_tip')">{{ t('dashboard.costCard') }}</HelpLabel>
          </div>
          <div class="kpi__value">¥{{ formatMicros(data?.today.total_cost_micros ?? 0,2) }}</div>
          <div class="kpi__sub">{{ t('dashboard.costCard_sub') }}</div>
        </div>
      </div>

      <div class="kpi">
        <div class="kpi__icon kpi__icon--purple">
          <TrendingUp :size="18" />
        </div>
        <div class="kpi__body">
          <div class="kpi__label">
            <HelpLabel :tip="t('dashboard.successRateCard_tip')">{{ t('dashboard.successRateCard') }}</HelpLabel>
          </div>
          <div class="kpi__value">{{ formatRate(data?.today.success_rate ?? 0) }}</div>
          <div class="kpi__sub">{{ t('dashboard.successRateCard_sub') }}</div>
        </div>
      </div>

      <div class="kpi">
        <div class="kpi__icon kpi__icon--warning">
          <AlertTriangle :size="18" />
        </div>
        <div class="kpi__body">
          <div class="kpi__label">
            <HelpLabel :tip="t('dashboard.unknownCostCard_tip')">{{ t('dashboard.unknownCostCard') }}</HelpLabel>
          </div>
          <div class="kpi__value">{{ formatNumber(data?.today.unknown_cost_calls ?? 0) }}</div>
          <div class="kpi__sub">{{ t('dashboard.unknownCostCard_sub') }}</div>
        </div>
      </div>
    </div>

    <!-- Trend chart -->
    <section class="section-card">
      <header class="section-head">
        <h2 class="section-title">{{ t('dashboard.trendTitle') }}</h2>
        <span class="section-sub">{{ t('dashboard.trendSub') }}</span>
      </header>
      <TrendChart :points="data?.trend ?? []" />
    </section>

    <!-- Two-column: top callers + recent failures -->
    <div class="two-col">
      <section class="section-card">
        <header class="section-head">
          <h2 class="section-title">{{ t('dashboard.topCallersTitle') }}</h2>
          <span class="section-sub">{{ t('dashboard.topCallersSub') }}</span>
        </header>
        <EmptyState v-if="!data?.top_callers?.length" :icon="Activity" :title="t('dashboard.topCallersEmpty')" />
        <ul v-else class="caller-list">
          <li v-for="(c, i) in data.top_callers" :key="c.api_key_id" class="caller-row">
            <span class="caller-rank">{{ i + 1 }}</span>
            <span class="caller-label">{{ c.owner_label || t('dashboard.unknownCaller') }}</span>
            <span class="caller-meta">{{ formatNumber(c.calls) }} {{ t('dashboard.callsUnit') }}</span>
            <span class="caller-cost">¥{{ formatMicros(c.cost_micros, 2) }}</span>
          </li>
        </ul>
      </section>

      <section class="section-card">
        <header class="section-head">
          <h2 class="section-title">{{ t('dashboard.recentFailuresTitle') }}</h2>
          <span class="section-sub">{{ t('dashboard.recentFailuresSub') }}</span>
        </header>
        <EmptyState v-if="!data?.recent_failures?.length" :icon="AlertTriangle" :title="t('dashboard.recentFailuresEmpty')" />
        <ul v-else class="failure-list">
          <li
            v-for="f in data.recent_failures"
            :key="f.request_id"
            class="failure-row"
            :title="t('dashboard.viewRequestDetail')"
            @click="goToRequestLog(f.request_id)"
          >
            <div class="failure-main">
              <span class="failure-status" :class="failureStatusClass(f.status_code)">{{ f.status_code }}</span>
              <span class="failure-model">{{ f.model_name || '—' }}</span>
              <span class="failure-reason">{{ formatFailReason(f.fail_reason, t) }}</span>
            </div>
            <div class="failure-meta">
              <span>{{ formatRelativeTime(f.created_at) }}</span>
              <span>{{ f.duration_ms }}ms</span>
            </div>
          </li>
        </ul>
      </section>
    </div>

    <!-- Upstream status -->
    <section class="section-card">
      <header class="section-head">
        <h2 class="section-title">{{ t('dashboard.upstreamTitle') }}</h2>
      </header>
      <div class="upstream-row">
        <div class="upstream-item">
          <span class="upstream-value upstream-value--success">{{ data?.upstream_status.available_providers ?? 0 }}</span>
          <span class="upstream-label">
            <HelpLabel :tip="t('dashboard.upstreamProviders_tip')">{{ t('dashboard.upstreamProviders') }}</HelpLabel>
          </span>
        </div>
        <div class="upstream-item">
          <span class="upstream-value" :class="{ 'upstream-value--warning': (data?.upstream_status.abnormal_keys ?? 0) > 0 }">
            {{ data?.upstream_status.abnormal_keys ?? 0 }}
          </span>
          <span class="upstream-label">
            <HelpLabel :tip="t('dashboard.upstreamAbnormalKeys_tip')">{{ t('dashboard.upstreamAbnormalKeys') }}</HelpLabel>
          </span>
        </div>
        <div class="upstream-item">
          <span class="upstream-value" :class="{ 'upstream-value--danger': (data?.upstream_status.unavailable_models ?? 0) > 0 }">
            {{ data?.upstream_status.unavailable_models ?? 0 }}
          </span>
          <span class="upstream-label">
            <HelpLabel :tip="t('dashboard.upstreamUnavailableModels_tip')">{{ t('dashboard.upstreamUnavailableModels') }}</HelpLabel>
          </span>
        </div>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { NButton, useMessage } from 'naive-ui'
import type { Component } from 'vue'
import { Activity, AlertTriangle, Boxes, Coins, Hourglass, KeyRound, Server, TrendingUp } from '@lucide/vue'
import PageHeader from '../../components/PageHeader.vue'
import HelpLabel from '../../components/HelpLabel.vue'
import EmptyState from '../../components/EmptyState.vue'
import TimeRangeSelect, { type RangePreset, type TimeRange } from '../../components/analytics/TimeRangeSelect.vue'
import TrendChart from '../../components/dashboard/TrendChart.vue'
import { getDashboard, type DashboardData } from '../../api/analytics'
import { displayMessage } from '../../api/client'
import { formatMicros } from '../../utils/money'
import { formatNumber, formatRate } from '../../utils/format'
import { formatFailReason } from '../../utils/failReason'

const { t } = useI18n()
const router = useRouter()
const message = useMessage()

const data = ref<DashboardData | null>(null)
const loading = ref(true)

const preset = ref<RangePreset>('last7d')
const timeRange = ref<TimeRange>({ start: null, end: null })
function onPresetChange(v: RangePreset) {
  preset.value = v
}
const reqUrl =  window.location.origin

// The dashboard skeleton (KPI cards, trend, upstream status) always renders so
// a fresh deployment sees a real overview reading zero rather than a blank
// page. Before any request traffic exists we surface a setup-guidance banner
// that points at the single next action in the onboarding funnel: add a
// provider, then enable a model, then create an API key. The final "waiting for
// traffic" step has no CTA — everything is configured and there is nothing left
// to click. Provider/model/key readiness comes from raw existence counts; key
// verification health is surfaced separately by the upstream-status card.
interface SetupStep {
  titleKey: string
  descKey: string
  icon: Component
  ctaKey?: string
  hint?: string
  to?: string
}

const setupStep = computed<SetupStep | null>(() => {
  const d = data.value
  if (!d) return null
  const s = d.setup
  // Setup guidance is based on lifetime signals (providers, models, keys, and
  // total lifetime requests) — NOT on range-filtered traffic. Otherwise
  // selecting a quiet period on a fully-configured, active system would show
  // "waiting for first request" even though traffic exists in other periods.
  if (s.providers === 0) {
    return { titleKey: 'dashboard.setupProviderTitle', descKey: 'dashboard.setupProviderDesc', icon: Server, ctaKey: 'dashboard.setupProviderCta', to: '/providers' }
  }
  if (s.enabled_models === 0) {
    return { titleKey: 'dashboard.setupModelTitle', descKey: 'dashboard.setupModelDesc', icon: Boxes, ctaKey: 'dashboard.setupModelCta', to: '/models' }
  }
  if (s.api_keys === 0) {
    return { titleKey: 'dashboard.setupKeyTitle', descKey: 'dashboard.setupKeyDesc', icon: KeyRound, ctaKey: 'dashboard.setupKeyCta', to: '/api-keys' }
  }
  // All entities configured. Show "waiting for first request" only when there
  // has genuinely never been any traffic — total_requests is a lifetime count
  // independent of the selected range, so a quiet custom window on an active
  // system no longer triggers this banner.
  if (s.total_requests === 0) {
    return { titleKey: 'dashboard.setupWaitingTitle', descKey: 'dashboard.setupWaitingDesc', hint: t('dashboard.requestAddress'), icon: Hourglass }
  }
  return null
})

// Monotonic reload token: prevents a stale window's response from overwriting
// a newer one (same pattern the cost-stats and detail pages use).
let reloadSeq = 0

async function reload() {
  const mySeq = ++reloadSeq
  loading.value = true
  try {
    const filter = timeRange.value.start && timeRange.value.end
      ? { start: timeRange.value.start, end: timeRange.value.end }
      : undefined
    const result = await getDashboard(filter)
    if (mySeq !== reloadSeq) return
    data.value = result
  } catch (err) {
    if (mySeq !== reloadSeq) return
    // Clear the prior range's data so KPIs/trend never stay frozen on a
    // different range than the selector claims — a transient toast is not
    // enough to make stale financial totals honest.
    data.value = null
    message.error(displayMessage(err, t))
  } finally {
    if (mySeq === reloadSeq) loading.value = false
  }
}

// TimeRangeSelect emits its resolved initial window synchronously on mount
// via its own immediate watch, which sets timeRange and fires this watch —
// performing the first load. Calling reload() from onMounted too would double
// every request on first paint.
watch(timeRange, (r) => {
  // An incomplete range (one or both bounds null) only happens when the user
  // clears the custom date picker. Reloading with no bounds would mix the
  // backend's default windows (today KPI + 7-day trend) under a "custom"
  // label, so reset to a concrete preset — TimeRangeSelect then re-emits a
  // full window and this watch reloads with consistent bounds.
  if (!r.start || !r.end) {
    if (preset.value === 'custom') preset.value = 'last7d'
    return
  }
  void reload()
})

function failureStatusClass(code: number): string {
  if (code >= 500) return 'failure-status--error'
  if (code >= 400) return 'failure-status--warning'
  return 'failure-status--error' // any non-2xx in this list is a failure by definition
}

async function onCopy() {
  try {
    await navigator.clipboard.writeText(reqUrl)
    message.success(t('apiKeys.copied'))
  } catch {
    message.error(t('apiKeys.copyFailed'))
  }
}

// formatRelativeTime renders "5m ago" / "2h ago" / "3d ago" — the dashboard
// doesn't need exact timestamps at first glance, only "how stale is this
// failure". On hover the user can click into the detail page for the
// RFC3339 timestamp.
function formatRelativeTime(rfc3339: string): string {
  const ts = Date.parse(rfc3339)
  if (Number.isNaN(ts)) return ''
  const diffMs = Date.now() - ts
  const sec = Math.floor(diffMs / 1000)
  if (sec < 60) return t('dashboard.justNow')
  const min = Math.floor(sec / 60)
  if (min < 60) return t('dashboard.minutesAgo', { n: min })
  const hr = Math.floor(min / 60)
  if (hr < 24) return t('dashboard.hoursAgo', { n: hr })
  const day = Math.floor(hr / 24)
  return t('dashboard.daysAgo', { n: day })
}

function goToRequestLog(requestId: string) {
  // Route is registered by the main router; if the
  // route isn't there yet, vue-router will log a warning and stay put —
  // safe failure mode for a forward reference.
  router.push(`/request-logs/${requestId}`).catch(() => {
    message.error(t('dashboard.requestLogUnavailable'))
  })
}
</script>

<style scoped>
.dashboard-page {
  display: flex;
  flex-direction: column;
  gap: var(--space-6);
}

.setup-banner {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  padding: var(--space-4) var(--space-5);
  background: var(--color-surface);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-lg);
}

.setup-banner__body {
  flex: 1;
  min-width: 0;
}

.setup-banner__title {
  font-size: var(--text-base);
  font-weight: 700;
  color: var(--color-text);
}

.setup-banner__desc {
  margin-top: 2px;
  font-size: var(--text-sm);
  color: var(--color-text-muted);
}

@media (max-width: 640px) {
  .setup-banner {
    flex-direction: column;
    align-items: flex-start;
  }
}

.kpi-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: var(--space-4);
}

.kpi {
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
  padding: var(--space-4);
  background: var(--color-surface);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-lg);
  transition: border-color 150ms;
}

.kpi:hover {
  border-color: var(--color-border);
}

.kpi__icon {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  border-radius: var(--radius-md);
}

.kpi__icon--accent {
  background: var(--color-accent-subtle);
  color: var(--color-accent);
}

.kpi__icon--success {
  background: var(--color-success-subtle);
  color: var(--color-success);
}

.kpi__icon--purple {
  background: var(--color-purple-subtle);
  color: var(--color-purple);
}

.kpi__icon--warning {
  background: var(--color-warning-subtle);
  color: var(--color-warning);
}

.kpi__label {
  font-size: var(--text-xs);
  font-weight: 700;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--color-text-muted);
  margin-bottom: 4px;
}

.kpi__value {
  font-size: 1.625rem;
  font-weight: 800;
  line-height: 1;
  font-variant-numeric: tabular-nums;
  color: var(--color-text);
}

.kpi__sub {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  margin-top: 4px;
}

.section-card {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-5);
  background: var(--color-surface);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-lg);
}

.section-head {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
}

.section-title {
  font-size: var(--text-sm);
  font-weight: 700;
  color: var(--color-text);
}

.section-sub {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.two-col {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-4);
}

.caller-list,
.failure-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: 0;
  list-style: none;
}

.caller-row {
  display: grid;
  grid-template-columns: 24px 1fr auto auto;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  font-size: var(--text-sm);
}

.caller-row:hover {
  background: var(--color-surface-hover);
}

.caller-rank {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  background: var(--color-accent-subtle);
  color: var(--color-accent);
  font-size: var(--text-xs);
  font-weight: 700;
  border-radius: 50%;
}

.caller-label {
  font-weight: 600;
  color: var(--color-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.caller-meta {
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

.caller-cost {
  color: var(--color-text);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.failure-row {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  padding: var(--space-3);
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: background 120ms;
}

.failure-row:hover {
  background: var(--color-surface-hover);
}

.failure-main {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--text-sm);
}

.failure-status {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 36px;
  height: 22px;
  padding: 0 6px;
  border-radius: var(--radius-sm);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  font-weight: 700;
  color: #fff;
}

.failure-status--error {
  background: var(--color-danger);
}

.failure-status--warning {
  background: var(--color-warning);
}

.failure-model {
  flex: 1;
  color: var(--color-text);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.failure-reason {
  color: var(--color-text-muted);
  font-size: var(--text-xs);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.failure-meta {
  display: flex;
  gap: var(--space-3);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

.upstream-row {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: var(--space-4);
}

.upstream-item {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--space-1);
  padding: var(--space-3);
  background: var(--color-bg-soft);
  border-radius: var(--radius-md);
}

.upstream-value {
  font-size: 1.5rem;
  font-weight: 800;
  color: var(--color-text);
  font-variant-numeric: tabular-nums;
}

.upstream-value--success {
  color: var(--color-success);
}

.upstream-value--warning {
  color: var(--color-warning);
}

.upstream-value--danger {
  color: var(--color-danger);
}

.upstream-label {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

@media (max-width: 900px) {
  .kpi-row {
    grid-template-columns: repeat(2, 1fr);
  }

  .two-col {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 640px) {
  .kpi-row {
    grid-template-columns: 1fr;
  }

  .upstream-row {
    grid-template-columns: 1fr;
  }
}
</style>
