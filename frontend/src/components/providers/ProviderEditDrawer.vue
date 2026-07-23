<!-- frontend/src/components/providers/ProviderEditDrawer.vue -->
<!-- Mirrors NewProviderDrawer.vue's step-1 form (name/baseUrl/note +
     ProtocolConfigFields) but with no key step — this drawer only edits an
     EXISTING provider's basic info and protocol config, prefilled from the
     `provider` prop. Structure (NDrawer/NDrawerContent, v-model:show,
     @updated) mirrors KeyEditDrawer.vue's own show/save/emit pattern. -->
<template>
  <n-drawer :show="show" width="480" @update:show="onUpdateShow">
    <n-drawer-content :title="t('providers.editProvider')" closable>
      <n-alert type="warning" class="reverify-warning">
        {{ t('providers.editProtocolReverifyWarning') }}
      </n-alert>
      <n-form require-mark-placement="left" ref="formRef" :model="form" :rules="rules" class="form">
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
    </n-drawer-content>
  </n-drawer>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage, type FormInst, type FormRules } from 'naive-ui'
import { useProvidersStore } from '../../store/providers'
import { displayMessage } from '../../api/client'
import type { Provider } from '../../api/providers'
import HelpLabel from '../HelpLabel.vue'
import ProtocolConfigFields from './ProtocolConfigFields.vue'
import { providerNameRule, baseUrlRule, noteRule } from '../../utils/providerValidators'
import {
  emptyProtocolConfig,
  parseProtocolConfig,
  protocolEndpointsValid,
  serializeProtocolConfig,
  type ProtocolConfigModel,
} from '../../utils/providerProtocol'

const props = defineProps<{ show: boolean; provider: Provider | null }>()
const emit = defineEmits<{ 'update:show': [boolean]; updated: [] }>()

const { t } = useI18n()
const message = useMessage()
const store = useProvidersStore()

const formRef = ref<FormInst | null>(null)
const submitting = ref(false)
const form = reactive({ name: '', baseUrl: '', note: '', protocol: emptyProtocolConfig('openai') as ProtocolConfigModel })

// Rule factories live in utils/providerValidators.ts (shared with
// NewProviderDrawer.vue / KeyEditDrawer.vue).
const rules: FormRules = {
  name: providerNameRule(t),
  baseUrl: baseUrlRule(t),
  note: noteRule(t),
}

watch(
  [() => props.show, () => props.provider],
  ([visible, provider]) => {
    if (!visible || !provider) return
    form.name = provider.name
    form.baseUrl = provider.base_url
    form.note = provider.note
    form.protocol = parseProtocolConfig(provider.provider_type, provider.protocol_endpoints)
  },
)

function onUpdateShow(value: boolean) {
  emit('update:show', value)
}

async function onSubmit() {
  if (!props.provider) return
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
    await store.update(props.provider.id, {
      name: form.name,
      base_url: form.baseUrl,
      note: form.note,
      ...serializeProtocolConfig(form.protocol),
    })
    message.success(t('providers.saveSuccess'))
    emit('updated')
    onUpdateShow(false)
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
.reverify-warning {
  margin-bottom: 16px;
}
.form {
  margin-top: 8px;
}
</style>
