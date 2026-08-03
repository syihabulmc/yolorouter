<!-- frontend/src/components/models/ModelEditModal.vue -->
<!-- Edits an existing model's public name, prefilled from the `model` prop.
     Structure (NModal card preset, v-model:show, @updated) mirrors
     ProviderEditModal.vue's show/save/emit pattern. -->
<template>
  <ModalDrawer
    v-model:show="showModel"
    :title="t('models.editModel')"
    max-width="520px"
    :mask-closable="false"
    :close-on-esc="false"
    :confirm-text="t('models.save')"
    :cancel-text="t('models.cancel')"
    :loading="submitting"
    :back-label="t('common.back')"
    @confirm="onSubmit"
  >
    <n-form require-mark-placement="left" ref="formRef" :model="form" :rules="rules">
      <n-form-item path="name">
        <template #label>
          <HelpLabel :tip="t('models.name_tip')">{{ t('models.name') }}</HelpLabel>
        </template>
        <n-input v-model:value="form.name" :placeholder="t('models.nameHint')" />
      </n-form-item>
    </n-form>
  </ModalDrawer>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage, type FormInst, type FormRules } from 'naive-ui'
import HelpLabel from '../HelpLabel.vue'
import ModalDrawer from '../common/ModalDrawer.vue'
import { useModelsStore } from '../../store/models'
import { displayMessage } from '../../api/client'
import type { Model } from '../../api/models'
import { modelNameRule } from '../../utils/modelValidators'

const props = defineProps<{ show: boolean; model: Model | null }>()
const emit = defineEmits<{ 'update:show': [boolean]; updated: [] }>()

// ModalDrawer owns a v-model:show; bridge it to this component's existing
// :show / @update:show contract so the parent doesn't have to change.
const showModel = computed({
  get: () => props.show,
  set: (v) => emit('update:show', v),
})

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
    showModel.value = false
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    submitting.value = false
  }
}
</script>
