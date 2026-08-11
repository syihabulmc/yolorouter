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
import { useDialog, useMessage, type FormInst, type FormRules } from 'naive-ui'
import HelpLabel from '../HelpLabel.vue'
import ModalDrawer from '../common/ModalDrawer.vue'
import { useModelsStore } from '../../store/models'
import { displayMessage } from '../../api/client'
import type { Model } from '../../api/models'
import { modelNameRule } from '../../utils/modelValidators'
import { modelRenameContent } from '../../utils/impactSummary'

const props = defineProps<{ show: boolean; model: Model | null }>()
const emit = defineEmits<{ 'update:show': [boolean]; updated: [] }>()

// ModalDrawer owns a v-model:show; bridge it to this component's existing
// :show / @update:show contract so the parent doesn't have to change.
const showModel = computed({
  get: () => props.show,
  set: (v) => emit('update:show', v),
})

const { t } = useI18n()
const dialog = useDialog()
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
  // An unchanged name is not a rename — close without a scare dialog.
  if (form.name === props.model.name) {
    showModel.value = false
    return
  }
  // Renaming breaks callers, not key allowlists (those follow the model id),
  // so the confirm leads with the live-traffic number for the old name.
  // submitting covers the fetch too: a second click while the impact loads
  // would stack a second confirm dialog.
  submitting.value = true
  let content: string
  try {
    content = await modelRenameContent(props.model.id, props.model.name, t)
  } finally {
    submitting.value = false
  }
  // The cancel button stays live while the preview loads, so the editor may
  // already be closed by the time it arrives. A confirm dialog for a rename
  // the user walked away from must not appear — accepting it would rename
  // anyway.
  if (!props.show || !props.model) return
  dialog.warning({
    title: t('models.confirmRenameModelTitle'),
    content,
    style: 'white-space: pre-line',
    positiveText: t('models.save'),
    negativeText: t('models.cancel'),
    onPositiveClick: () => {
      void doRename()
    },
  })
}

async function doRename() {
  if (!props.model) return
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
