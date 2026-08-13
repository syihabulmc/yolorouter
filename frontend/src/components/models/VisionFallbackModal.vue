<!-- frontend/src/components/models/VisionFallbackModal.vue
     Global vision-fallback configuration: pick the model that describes
     images on behalf of text-only targets (clearing it turns the feature
     off) and optionally override the describe prompt. Load + version-CAS
     save mirrors OptimizationSettingsModal; a 409 asks for a reload since
     another admin committed first. Emits "saved" after a successful PUT. -->
<template>
  <ModalDrawer
    v-model:show="showModel"
    :title="t('models.visionFallbackTitle')"
    max-width="640px"
    :mask-closable="false"
    :close-on-esc="false"
    :confirm-text="t('models.save')"
    :cancel-text="t('models.cancel')"
    :loading="saving"
    :back-label="t('common.back')"
    @confirm="onSave"
  >
    <p class="vf-desc">{{ t('models.visionFallbackDesc') }}</p>
    <div v-if="load === 'loading'" class="vf-state">{{ t('common.loading') }}</div>
    <div v-else-if="load === 'error'" class="vf-state vf-state--err">
      <span>{{ t('models.visionFallbackLoadFailed') }}</span>
      <NButton size="small" @click="loadSetting">{{ t('models.retry') }}</NButton>
    </div>
    <!-- Display-only form (no validation rules): the backend rejects unknown
         model names and stale versions authoritatively, so NFormItems carry
         no path and NForm no model/rules. -->
    <NForm v-else>
      <NFormItem>
        <template #label>
          <HelpLabel :tip="t('models.visionFallbackModel_tip')">{{ t('models.visionFallbackModel') }}</HelpLabel>
        </template>
        <NSelect
          v-model:value="form.model"
          :options="modelOptions"
          :placeholder="t('models.visionFallbackModelPlaceholder')"
          clearable
          filterable
        />
      </NFormItem>
      <NFormItem>
        <template #label>
          <HelpLabel :tip="t('models.visionFallbackPrompt_tip')">{{ t('models.visionFallbackPrompt') }}</HelpLabel>
        </template>
        <NInput
          v-model:value="form.prompt"
          type="textarea"
          :autosize="{ minRows: 2, maxRows: 6 }"
        />
        <template #feedback>
          <span class="vf-default-hint">{{ t('models.visionFallbackPromptDefaultHint') }}</span>
        </template>
      </NFormItem>
    </NForm>
  </ModalDrawer>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NForm, NFormItem, NInput, NSelect, useMessage, type SelectOption } from 'naive-ui'
import HelpLabel from '../HelpLabel.vue'
import ModalDrawer from '../common/ModalDrawer.vue'
import { useModelsStore } from '../../store/models'
import { displayMessage } from '../../api/client'
import { getVisionFallback, updateVisionFallback } from '../../api/systemSettings'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ 'update:show': [boolean]; saved: [] }>()

const showModel = computed({
  get: () => props.show,
  set: (v) => emit('update:show', v),
})

const { t } = useI18n()
const message = useMessage()
const modelsStore = useModelsStore()

const load = ref<'loading' | 'error' | 'ready'>('loading')
const saving = ref(false)
const version = ref(0)
// NSelect emits null on clear; model null means "feature off" and is sent as
// the empty string the backend stores.
const form = reactive<{ model: string | null; prompt: string }>({ model: null, prompt: '' })

// Options are this gateway's own models — the describing model is routed like
// any other request, so anything configured here is selectable. Picking a
// model that cannot actually see images is the admin's responsibility; the
// tip says so.
const modelOptions = computed<SelectOption[]>(() =>
  modelsStore.list.map((m) => ({ label: m.name, value: m.name })),
)

async function loadSetting() {
  load.value = 'loading'
  try {
    const s = await getVisionFallback()
    form.model = s.model || null
    // A never-configured prompt prefills with the default in the admin's
    // console language — real editable content, not a placeholder, so what
    // is on screen is exactly what gets saved and sent at runtime.
    form.prompt = s.prompt || t('models.visionFallbackDefaultPrompt')
    version.value = s.version
    load.value = 'ready'
  } catch {
    load.value = 'error'
  }
}

watch(
  () => props.show,
  (visible) => {
    if (!visible) return
    void loadSetting()
    if (!modelsStore.list.length) {
      void modelsStore.fetchList().catch((err) => message.error(displayMessage(err, t)))
    }
  },
)

async function onSave() {
  // Nothing loaded means nothing valid to save: a PUT here would carry
  // version 0 and bounce off the backend's version check anyway.
  if (load.value !== 'ready') return
  saving.value = true
  try {
    const s = await updateVisionFallback({ model: form.model ?? '', prompt: form.prompt, version: version.value })
    version.value = s.version
    message.success(t('models.saveSuccess'))
    emit('saved')
    showModel.value = false
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    saving.value = false
  }
}
</script>

<style scoped lang="less">
.vf-desc {
  margin: 0 0 12px;
  color: var(--color-text-secondary);
  font-size: var(--text-sm);
}
.vf-state {
  display: flex;
  align-items: center;
  gap: 12px;
  color: var(--color-text-muted);
  font-size: var(--text-sm);
}
.vf-state--err {
  color: var(--color-text-secondary);
}
.vf-default-hint {
  color: var(--color-text-muted);
  font-size: var(--text-xs);
}
</style>
