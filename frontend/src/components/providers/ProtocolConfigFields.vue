<!-- frontend/src/components/providers/ProtocolConfigFields.vue -->
<!-- Reusable primary-protocol + additional-protocol-endpoints editor, shared
     between NewProviderModal.vue and ProviderEditModal.vue. Purely a
     form-model editor: it maintains ProtocolConfigModel (see
     utils/providerProtocol.ts) and never serializes to the wire format
     itself — the parent form does that via serializeProtocolConfig() at
     submit time. -->
<template>
  <div class="protocol-config-fields">
    <n-form-item>
      <template #label>
        <HelpLabel :tip="t('providers.protocolPrimary_tip')">{{ t('providers.protocolPrimary') }}</HelpLabel>
      </template>
      <n-select :value="modelValue.providerType" :options="primaryOptions" @update:value="onPrimaryChange" />
    </n-form-item>

    <NCollapse class="multi-protocol-collapse">
      <NCollapseItem :title="t('providers.multiProtocolTitle')" name="multi-protocol">
        <div v-for="p in additionalProtocols" :key="p" class="protocol-row">
          <NCheckbox :checked="modelValue.endpoints[p].enabled" @update:checked="(checked: boolean) => onToggleEndpoint(p, checked)">
            {{ t('providers.alsoAccept', { name: t(`providers.protocol_${p}`) }) }}
          </NCheckbox>
          <n-form-item v-if="modelValue.endpoints[p].enabled" :rule="endpointUrlRule" :show-label="false" class="endpoint-url-item">
            <n-input
              :value="modelValue.endpoints[p].url"
              :placeholder="t('providers.endpointUrlPlaceholder')"
              @update:value="(url: string) => onEndpointUrlChange(p, url)"
            />
          </n-form-item>
          <div v-if="modelValue.endpoints[p].enabled" class="hint-text">{{ t('providers.endpointUrlHint') }}</div>
        </div>
      </NCollapseItem>
    </NCollapse>
  </div>
</template>

<script setup lang="ts">
// NCollapse/NCollapseItem/NCheckbox are NOT in main.ts's create() components
// list (only ~28 common ones are). Import them explicitly, or they silently
// render as unknown elements (vue-tsc / vite build stay green) — see
// HelpLabel.vue for the same pattern with NTooltip/NIcon.
import { NCollapse, NCollapseItem, NCheckbox } from 'naive-ui'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpLabel from '../HelpLabel.vue'
import { ALL_PROTOCOLS, type ProtocolId, type ProtocolConfigModel, type ProtocolEndpointEntry } from '../../utils/providerProtocol'
import { protocolEndpointUrlRule } from '../../utils/providerValidators'

const props = defineProps<{ modelValue: ProtocolConfigModel }>()
const emit = defineEmits<{ 'update:modelValue': [ProtocolConfigModel] }>()

const { t } = useI18n()

const endpointUrlRule = computed(() => protocolEndpointUrlRule(t))

const primaryOptions = computed(() => ALL_PROTOCOLS.map((p) => ({ label: t(`providers.protocol_${p}`), value: p })))

const additionalProtocols = computed(() => ALL_PROTOCOLS.filter((p) => p !== props.modelValue.providerType))

function cloneModel(model: ProtocolConfigModel): ProtocolConfigModel {
  return {
    providerType: model.providerType,
    endpoints: Object.fromEntries(ALL_PROTOCOLS.map((p) => [p, { ...model.endpoints[p] }])) as Record<
      ProtocolId,
      ProtocolEndpointEntry
    >,
  }
}

function onPrimaryChange(value: ProtocolId) {
  const next = cloneModel(props.modelValue)
  next.providerType = value
  // The primary protocol is always supported — it can never also be listed
  // as an "additional" endpoint, so disable it there if it was enabled
  // under the old primary.
  if (next.endpoints[value].enabled) {
    next.endpoints[value] = { ...next.endpoints[value], enabled: false }
  }
  emit('update:modelValue', next)
}

function onToggleEndpoint(protocol: ProtocolId, enabled: boolean) {
  const next = cloneModel(props.modelValue)
  next.endpoints[protocol] = { ...next.endpoints[protocol], enabled }
  emit('update:modelValue', next)
}

function onEndpointUrlChange(protocol: ProtocolId, url: string) {
  const next = cloneModel(props.modelValue)
  next.endpoints[protocol] = { ...next.endpoints[protocol], url }
  emit('update:modelValue', next)
}
</script>

<style scoped>
/* The collapse is not an n-form-item, so it misses the form's field rhythm —
   add the same breathing room so it never butts against the next field. */
.multi-protocol-collapse {
  margin-bottom: 18px;
}
.protocol-row {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 12px;
}
.endpoint-url-item {
  margin: 4px 0 0;
}
.hint-text {
  font-size: 12px;
  opacity: 0.45;
  margin-top: -8px;
}
</style>
