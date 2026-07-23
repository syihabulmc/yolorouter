<!-- frontend/src/components/providers/NewProviderModal.vue -->
<template>
  <n-modal
    :show="show"
    preset="card"
    :title="t('providers.createButton')"
    style="max-width: 560px"
    :mask-closable="false"
    :close-on-esc="false"
    @update:show="onUpdateShow"
  >
    <div class="preset-section">
      <HelpLabel :tip="t('providers.presetHint')" class="preset-label">{{ t('providers.presetTitle') }}</HelpLabel>
      <div class="preset-grid">
        <button
          v-for="card in presetCards"
          :key="card.id"
          type="button"
          class="preset-card"
          :class="{ active: selectedPresetId === card.id }"
          @click="applyPreset(card)"
        >
          {{ card.name }}
        </button>
        <button
          type="button"
          class="preset-card"
          :class="{ active: selectedPresetId === '' }"
          @click="selectedPresetId = ''"
        >
          {{ t('providers.presetCustom') }}
        </button>
      </div>
    </div>

    <div class="section-divider" />

    <n-form
      require-mark-placement="left"
      ref="formRef"
      :model="form"
      :rules="rules"
      class="form provider-form-dense"
      label-placement="left"
      label-align="right"
      label-width="auto"
    >
      <n-form-item path="name">
        <template #label>
          <HelpLabel :tip="t('providers.name_tip')">{{ t('providers.name') }}</HelpLabel>
        </template>
        <n-input v-model:value="form.name" />
      </n-form-item>
      <n-form-item path="baseUrl">
        <template #label>
          <HelpLabel :tip="t('providers.baseUrl_tip')">{{ t('providers.baseUrl') }}</HelpLabel>
        </template>
        <n-input v-model:value="form.baseUrl" placeholder="https://api.example.com/v1" />
      </n-form-item>
      <ProtocolConfigFields v-model="form.protocol" />
      <n-form-item path="label">
        <template #label>
          <HelpLabel :tip="t('providers.keyLabel_tip')">{{ t('providers.keyLabel') }}</HelpLabel>
        </template>
        <n-input v-model:value="form.label" />
      </n-form-item>
      <n-form-item path="plaintext">
        <template #label>
          <HelpLabel :tip="t('providers.keyPlaintext_tip')">{{ t('providers.keyPlaintext') }}</HelpLabel>
        </template>
        <n-input v-model:value="form.plaintext" type="password" show-password-on="click" />
      </n-form-item>
      <ProviderModelTester
        v-model:value="form.testModel"
        :base-url="form.baseUrl"
        :api-key="form.plaintext"
        :provider-type="form.protocol.providerType"
      />
      <n-form-item path="note">
        <template #label>
          <HelpLabel :tip="t('providers.note_tip')">{{ t('providers.note') }}</HelpLabel>
        </template>
        <n-input v-model:value="form.note" type="textarea" />
      </n-form-item>
    </n-form>

    <template #footer>
      <n-space justify="end">
        <n-button @click="onUpdateShow(false)">{{ t('providers.cancel') }}</n-button>
        <n-button type="primary" :loading="submitting" @click="onSubmit">{{ t('providers.save') }}</n-button>
      </n-space>
    </template>
  </n-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage, type FormInst, type FormRules } from 'naive-ui'
import { useProvidersStore } from '../../store/providers'
import { displayMessage } from '../../api/client'
import HelpLabel from '../HelpLabel.vue'
import ProtocolConfigFields from './ProtocolConfigFields.vue'
import ProviderModelTester from './ProviderModelTester.vue'
import { providerNameRule, baseUrlRule, noteRule, keyLabelRule, keyPlaintextRule } from '../../utils/providerValidators'
import { emptyProtocolConfig, protocolEndpointsValid, serializeProtocolConfig, type ProtocolConfigModel } from '../../utils/providerProtocol'
import { PROVIDER_PRESETS } from '../../config/providerPresets'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ 'update:show': [boolean] }>()

const { t } = useI18n()
const message = useMessage()
const store = useProvidersStore()

// presetCards resolves each preset's localized display name once per locale
// (rather than re-running the i18n lookup for every card on every render).
const presetCards = computed(() =>
  PROVIDER_PRESETS.map((p) => ({ ...p, name: t(`providers.preset_${p.id}`) })),
)
type PresetCard = (typeof presetCards.value)[number]

// selectedPresetId: a preset's id when one is picked, '' for the explicit
// "custom" card. It only drives the card highlight — the form fields it
// filled remain freely editable afterwards.
const selectedPresetId = ref('')

const formRef = ref<FormInst | null>(null)
const submitting = ref(false)
const form = reactive({
  name: '',
  baseUrl: '',
  note: '',
  protocol: emptyProtocolConfig('openai') as ProtocolConfigModel,
  label: '',
  plaintext: '',
  testModel: '',
})

// Rule factories live in utils/providerValidators.ts. testModel's own rule is
// applied inside ProviderModelTester's form-item; the rest bind here.
const rules: FormRules = {
  name: providerNameRule(t),
  baseUrl: baseUrlRule(t),
  note: noteRule(t),
  label: keyLabelRule(t),
  plaintext: keyPlaintextRule(t, true),
}

// A picked card fills the form with its localized name (so the saved provider
// name matches the card) plus its address/protocol/default test model; the
// fields stay freely editable afterwards.
function applyPreset(card: PresetCard) {
  selectedPresetId.value = card.id
  form.name = card.name
  form.baseUrl = card.baseUrl
  form.protocol = emptyProtocolConfig(card.protocol)
  form.testModel = card.defaultTestModel
}

watch(
  () => props.show,
  (visible) => {
    if (!visible) return
    selectedPresetId.value = ''
    form.name = ''
    form.baseUrl = ''
    form.note = ''
    form.protocol = emptyProtocolConfig('openai')
    form.label = ''
    form.plaintext = ''
    form.testModel = ''
  },
)

function onUpdateShow(value: boolean) {
  emit('update:show', value)
}

async function onSubmit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  if (!protocolEndpointsValid(form.protocol)) {
    message.error(t('providers.protocolEndpointUrlInvalid'))
    return
  }
  submitting.value = true
  try {
    await store.create({
      name: form.name,
      base_url: form.baseUrl,
      note: form.note,
      key_label: form.label,
      key_plaintext: form.plaintext,
      test_model: form.testModel,
      management_status: 1, // this modal's fixed behavior: the first key is always submitted requesting enabled (server independently re-verifies before honoring it)
      ...serializeProtocolConfig(form.protocol),
    })
    onUpdateShow(false)
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
.preset-section {
  margin-bottom: 14px;
}
.preset-label {
  display: block;
  margin-bottom: 8px;
  font-size: 13px;
  color: var(--color-text-secondary);
}
.preset-grid {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.preset-card {
  padding: 5px 12px;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: transparent;
  font-size: 13px;
  line-height: 1.2;
  cursor: pointer;
  transition: border-color 0.15s, color 0.15s, background 0.15s;
  color: var(--color-text);
}
.preset-card:hover {
  border-color: var(--color-accent);
  color: var(--color-accent);
}
.preset-card.active {
  border-color: var(--color-accent);
  background: var(--color-accent-subtle);
  color: var(--color-accent);
  font-weight: 500;
}
.section-divider {
  height: 1px;
  margin: 0 0 16px;
  background: var(--color-border-subtle);
}
.form {
  margin-top: 0;
}
</style>
