<!-- frontend/src/components/CustomPromptEditor.vue
     Reusable editor for a custom system-prompt body: textarea bound via
     v-model:text, a pair of one-click example buttons, and a live character
     counter. The counter and maxlength mirror the backend's rune-based cap
     (MaxCustomSystemPromptLen), so runeCount iterates the string without
     allocating a temporary array on every keystroke. -->
<template>
  <div class="custom-prompt-editor">
    <div v-if="multiple" class="custom-prompt-examples">
      <NButton
        v-for="ex in customPromptExamples"
        :key="ex.label"
        size="tiny"
        secondary
        :type="customPromptExamplesActive.includes(ex.label) ? 'primary' : 'default'"
        @click="multipleClick(ex.text)"
      >
        {{ ex.label }}
      </NButton>
    </div>
    <div v-else class="custom-prompt-examples">
      <NButton
        v-for="ex in customPromptExamples"
        :key="ex.label"
        size="tiny"
        secondary
        @click="emit('update:text', ex.text)"
      >
        {{ ex.label }}
      </NButton>
    </div>
    <NInput
      v-if="showInput"
      :value="text"
      type="textarea"
      :rows="rows"
      :autosize="autosize"
      :placeholder="placeholder"
      @update:value="onUpdateValue"
    />
    <div  v-if="showInput" class="custom-prompt-footer" >
      <span class="custom-prompt-count">{{ t('costOptimization.charCount', { remaining: remainingChars }) }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NInput } from 'naive-ui'

interface Props {
  // v-model:text — the prompt body
  text: string
  // Mirror MaxCustomSystemPromptLen; the live counter and the textarea's
  // hard cap both key off this so the UI can't drift from the server check.
  maxlength?: number
  // Fixed row count (mutually exclusive with autosize). When neither is
  // supplied the textarea falls back to its own default height.
  rows?: number
  // Auto-grow configuration (mutually exclusive with rows).
  autosize?: { minRows: number; maxRows?: number }
  placeholder?: string
  showInput?: boolean
  multiple?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  maxlength: 2000,
  rows: undefined,
  autosize: undefined,
  placeholder: '',
  showInput: true,
  multiple: false,
})

const emit = defineEmits<{ (e: 'update:text', v: string): void }>()

const { t } = useI18n()

// Iterate the string as Unicode code points without materializing an array
// (Array.from would allocate per keystroke). The backend counts runes, so a
// surrogate-pair emoji still tallies as one character here.
function runeCount(s: string): number {
  let n = 0
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  for (const _ of s) n++
  return n
}

const remainingChars = computed(() => props.maxlength - runeCount(props.text))

const customPromptExamples = computed(() => [
  { label: t('costOptimization.exampleConciseLabel'), text: t('costOptimization.exampleConciseText') },
  { label: t('costOptimization.exampleMinimalCodeLabel'), text: t('costOptimization.exampleMinimalCodeText') },
])

const customPromptExamplesActive = computed(() => {
  return customPromptExamples.value.filter(e => props.text.includes(e.text)).map(e => e.label)
})

function multipleClick(str:string) {
  if (props.text.includes(str)) {
    emit('update:text', props.text.replace(str,''))
  } else {
    emit('update:text', props.text + str)
  }
}
function onUpdateValue(v: string) {
  // Enforce the cap by Unicode code point, not the textarea's native UTF-16
  // maxlength (which would block valid non-BMP input like emoji early: 2000
  // emoji are server-valid but native maxlength permits ~1000). The backend
  // counts runes, so truncate at maxlength code points here.
  if (runeCount(v) > props.maxlength) {
    let n = 0
    let out = ''
    for (const ch of v) {
      if (n >= props.maxlength) break
      out += ch
      n++
    }
    emit('update:text', out)
    return
  }
  emit('update:text', v)
}
</script>

<style scoped>
.custom-prompt-editor {
  width: 100%;
}

.custom-prompt-examples {
  display: flex;
  gap: var(--space-2);
  margin-bottom: var(--space-2);
}

.custom-prompt-footer {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  margin-top: var(--space-2);
}

.custom-prompt-count {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}
</style>
