<!-- frontend/src/components/models/ModelEditModal.vue -->
<!-- Edits an existing model's public name, prefilled from the `model` prop.
     Structure (NModal card preset, v-model:show, @updated) mirrors
     ProviderEditModal.vue's show/save/emit pattern. -->
<template>
  <n-modal
    :show="show"
    preset="card"
    :title="t('models.editModel')"
    style="max-width: 520px"
    :mask-closable="false"
    :close-on-esc="false"
    @update:show="onUpdateShow"
  >
    <n-form require-mark-placement="left" ref="formRef" :model="form" :rules="rules">
      <n-form-item path="name">
        <template #label>
          <HelpLabel :tip="t('models.name_tip')">{{ t('models.name') }}</HelpLabel>
        </template>
        <n-input v-model:value="form.name" :placeholder="t('models.nameHint')" />
      </n-form-item>
    </n-form>
    <template #footer>
      <n-space justify="end">
        <n-button @click="onUpdateShow(false)">{{ t('models.cancel') }}</n-button>
        <n-button type="primary" :loading="submitting" @click="onSubmit">{{ t('models.save') }}</n-button>
      </n-space>
    </template>
  </n-modal>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage, type FormInst, type FormRules } from 'naive-ui'
import HelpLabel from '../HelpLabel.vue'
import { useModelsStore } from '../../store/models'
import { displayMessage } from '../../api/client'
import type { Model } from '../../api/models'
import { modelNameRule } from '../../utils/modelValidators'

const props = defineProps<{ show: boolean; model: Model | null }>()
const emit = defineEmits<{ 'update:show': [boolean]; updated: [] }>()

const { t } = useI18n()
const message = useMessage()
const store = useModelsStore()

const formRef = ref<FormInst | null>(null)
const submitting = ref(false)
const form = reactive({ name: '' })
const rules: FormRules = { name: modelNameRule(t) }

watch(
  [() => props.show, () => props.model],
  ([visible, model]) => {
    if (!visible || !model) return
    form.name = model.name
  },
)

function onUpdateShow(value: boolean) {
  emit('update:show', value)
}

async function onSubmit() {
  if (!props.model) return
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  submitting.value = true
  try {
    await store.update(props.model.id, form.name)
    message.success(t('models.saveSuccess'))
    emit('updated')
    onUpdateShow(false)
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    submitting.value = false
  }
}
</script>
