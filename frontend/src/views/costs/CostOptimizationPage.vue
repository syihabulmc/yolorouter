<!-- frontend/src/views/costs/CostOptimizationPage.vue
     Global cost-optimization settings. Currently houses the custom system
     prompt that is prepended to every routed request when enabled; the
     Input Compression card is a reserved placeholder for the next module.

     Three-state load guards against a stale-default footgun: before the
     GET resolves the form stays non-editable, so a network failure can't
     trick the admin into saving empty defaults over the real config. The
     PUT uses version as an optimistic-lock token; a 409 (errcode 11012)
     means someone else edited the prompt concurrently — we surface that
     and reload the authoritative state instead of letting two sessions
     fight. -->
<template>
  <div class="cost-optimization-page">
    <PageHeader
      :eyebrow="t('costOptimization.eyebrow')"
      :title="t('costOptimization.title')"
      :description="t('costOptimization.description')"
    />

    <!-- loading: GET in flight, no editable form yet (avoid saving defaults over real config) -->
    <div v-if="loadState === 'loading'" class="section-card loading-card">
      <NSpin />
    </div>

    <!-- load error: retry, still no editable form -->
    <div v-else-if="loadState === 'error'" class="section-card error-card">
      <p class="error-card__msg">{{ t('costOptimization.loadFailed') }}</p>
      <NButton size="small" @click="load">{{ t('costOptimization.retry') }}</NButton>
    </div>

    <!-- loaded: editable -->
    <template v-else>
      <section class="section-card">
        <div class="section-card__head">
          <HelpLabel :tip="t('costOptimization.cspTitle_tip')">{{ t('costOptimization.cspTitle') }}</HelpLabel>
        </div>
        <p class="section-card__desc">{{ t('costOptimization.cspDesc') }}</p>
        <NForm label-placement="top" :show-require-mark="false">
          <NFormItem path="enabled">
            <template #label>
              <HelpLabel :tip="t('costOptimization.enabled_tip')">{{ t('costOptimization.enabled') }}</HelpLabel>
            </template>
            <NSwitch v-model:value="form.enabled" />
          </NFormItem>
          <NFormItem path="text">
            <template #label>
              <HelpLabel :tip="t('costOptimization.text_tip')">{{ t('costOptimization.text') }}</HelpLabel>
            </template>
            <CustomPromptEditor
              v-model:text="form.text"
              :rows="8"
              :placeholder="t('costOptimization.textPlaceholder')"
            />
          </NFormItem>
        </NForm>
        <div class="card-footer">
          <span class="version-tag">v{{ form.version }}</span>
          <NSpace justify="end">
            <NButton type="primary" :loading="saving" :disabled="!setting" @click="save">
              {{ t('costOptimization.save') }}
            </NButton>
          </NSpace>
        </div>
      </section>

      <!-- placeholder for Input Compression (next module); not editable yet -->
      <section class="section-card placeholder-card">
        <div class="section-card__head">
          <HelpLabel :tip="t('costOptimization.inputCompressionTitle_tip')">
            {{ t('costOptimization.inputCompressionTitle') }}
          </HelpLabel>
        </div>
        <p class="placeholder-card__body">{{ t('costOptimization.comingSoon') }}</p>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NForm, NFormItem, NSpace, NSpin, NSwitch, useMessage } from 'naive-ui'
import PageHeader from '../../components/PageHeader.vue'
import HelpLabel from '../../components/HelpLabel.vue'
import CustomPromptEditor from '../../components/CustomPromptEditor.vue'
import { APIError, displayMessage } from '../../api/client'
import {
  getCustomSystemPrompt,
  updateCustomSystemPrompt,
  type CustomSystemPromptSetting,
} from '../../api/systemSettings'
import { CUSTOM_SYSTEM_PROMPT_CONFLICT } from '../../api/errcodes'

const { t } = useI18n()
const message = useMessage()

// Three-state load: 'loading' keeps the form hidden so a failed GET can't
// expose an editable default that would overwrite the real row on save;
// 'error' surfaces a retry; 'loaded' renders the form against real data.
const loadState = ref<'loading' | 'error' | 'loaded'>('loading')
const setting = ref<CustomSystemPromptSetting | null>(null)
const form = reactive({ enabled: false, text: '', version: 0 })
const saving = ref(false)

async function load() {
  loadState.value = 'loading'
  try {
    const s = await getCustomSystemPrompt()
    setting.value = s
    form.enabled = s.enabled
    form.text = s.text
    form.version = s.version
    loadState.value = 'loaded'
  } catch (err) {
    // Don't surface the raw envelope message verbatim — the user only needs
    // to know the load failed and that retry is the recovery path.
    setting.value = null
    loadState.value = 'error'
    if (!(err instanceof APIError)) {
      // Network/parse failures get the localized network error; APIError is
      // already surfaced via the error card's retry CTA, so don't double-toast.
      message.error(displayMessage(err, t))
    }
  }
}

async function save() {
  // Hard guard: never let a save fire before a successful GET — otherwise
  // a click during the error state could submit the empty defaults and
  // wipe whatever is actually stored server-side.
  if (!setting.value || saving.value) return
  saving.value = true
  try {
    const updated = await updateCustomSystemPrompt({
      enabled: form.enabled,
      text: form.text,
      version: form.version,
    })
    // Adopt the server's new version immediately so a second save uses the
    // right base — without this, back-to-back saves would 409 on the stale
    // version that the first save just invalidated.
    setting.value = updated
    form.version = updated.version
    form.enabled = updated.enabled
    form.text = updated.text
    message.success(t('costOptimization.saved'))
  } catch (err) {
    if (err instanceof APIError && err.code === CUSTOM_SYSTEM_PROMPT_CONFLICT) {
      // Concurrent edit: the row was changed by someone else (or in another
      // tab) since our GET. Don't let the user fight theirs — surface the
      // conflict and reload authoritative state.
      message.error(t('costOptimization.conflict'))
      void load()
    } else {
      // Non-conflict failures: surface the already-localized errcode message
      // (e.g. 11010 too-long / 11011 empty-when-enabled) via displayMessage,
      // falling back to common.networkError for non-API failures.
      message.error(displayMessage(err, t))
    }
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  void load()
})
</script>

<style scoped>
.cost-optimization-page {
  display: flex;
  flex-direction: column;
  gap: var(--space-6);
}

.loading-card {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 160px;
}

.error-card {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--space-3);
  padding: var(--space-6);
}

.error-card__msg {
  margin: 0;
  color: var(--color-text-secondary);
}

/* Section heads reuse the shared .section-card shell (global.less) but the
   heading row itself mirrors CostStatsPage's treatment so both cost pages
   read as one family. */
.section-card__head {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  margin-bottom: var(--space-4);
  font-weight: 700;
  color: var(--color-text);
}

.section-card__desc {
  margin: 0 0 var(--space-4);
  color: var(--color-text-secondary);
  line-height: 1.6;
}

.card-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  margin-top: var(--space-2);
}

.version-tag {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

.placeholder-card__body {
  margin: 0;
  color: var(--color-text-muted);
}
</style>
