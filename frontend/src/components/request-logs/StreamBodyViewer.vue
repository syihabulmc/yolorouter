<!-- StreamBodyViewer renders the captured stream-body transcript served
     from disk via body/stream (request-log detail). The body is the raw
     SSE the gateway wrote while relaying a streaming response, so the
     shapes it carries are:

     - An OpenAI-style SSE stream (lines of `data: {...}` separated by
       blank lines, terminated by `data: [DONE]`) — the common case.
     - A single JSON value (non-SSE upstream captured inline).
     - Plain text (an upstream error page, an HTML error, etc.).
     - Empty (not captured).

     For SSE streams, this shows an "assembled content" preview
     (concatenated `delta.content`) above the per-chunk tree, so the
     user can read what was actually streamed without expanding every
     chunk's `choices` array. The SSE detection scans the first few
     non-empty lines so a leading BOM, blank line, or `:comment` line
     doesn't hide a real stream. -->
<template>
  <div class="stream-body-viewer">
    <EmptyState v-if="!body" type="compact" :icon="FileText" :title="t('requestLogs.bodyNotRecorded')" />
    <div v-else-if="tooLarge" class="body-pre-fallback">
      <div class="body-too-large-hint">{{ t('requestLogs.bodyTooLargeHint') }}</div>
      <pre class="body-pre"><code>{{ body }}</code></pre>
    </div>
    <template v-else-if="isSSE">
      <div v-if="assembled !== null" class="sse-assembled">
        <div class="sse-assembled-label">{{ t('requestLogs.sseAssembledContent') }}</div>
        <pre class="sse-assembled-content">{{ assembled || t('requestLogs.sseAssembledEmpty') }}</pre>
      </div>
      <div v-if="truncated" class="sse-truncated-hint">
        {{ t('requestLogs.sseTruncatedHint', { shown: CHUNK_LIMIT, total: items.length }) }}
      </div>
      <div class="sse-stream">
        <div v-for="(item, i) in visibleItems" :key="i" class="sse-chunk">
          <template v-if="item.type === 'data'">
            <span v-if="item.isDone" class="sse-done">data: [DONE]</span>
            <template v-else-if="item.jsonRaw">
              <span class="sse-prefix">data:</span>
              <BodyViewer class="sse-json" :raw="item.jsonRaw" :deep="1" :raw-hint="t('requestLogs.bodyRawHint')" />
            </template>
            <span v-else class="sse-raw">{{ item.raw }}</span>
          </template>
          <span v-else class="sse-raw sse-raw--other">{{ item.raw }}</span>
        </div>
      </div>
    </template>
    <BodyViewer v-else :raw="body" :deep="1" :raw-hint="t('requestLogs.bodyRawHint')" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { FileText } from '@lucide/vue'
import EmptyState from '../EmptyState.vue'
import BodyViewer from './BodyViewer.vue'

const props = defineProps<{
  body: string
}>()

const { t } = useI18n()

const SIZE_LIMIT = 500_000
const CHUNK_LIMIT = 200

interface SSEItem {
  type: 'data' | 'other'
  raw: string
  jsonRaw: string
  isDone: boolean
}

// Strip a leading BOM (the gateway reads bytes; a UTF-8 BOM at the start
// of the body would defeat the `data:` prefix check otherwise).
function stripBOM(s: string): string {
  return s.charCodeAt(0) === 0xfeff ? s.slice(1) : s
}

const tooLarge = computed(() => props.body.length > SIZE_LIMIT)

// SSE detection: scan the first few non-empty lines for SSE field
// markers, so a leading BOM / blank line / `:comment` line doesn't hide
// a real stream. Returns true on the first SSE-shaped line; bails false
// after PROBE_LIMIT non-empty lines that aren't SSE-shaped.
const PROBE_LIMIT = 3
function isSSEFormat(raw: string): boolean {
  if (!raw) return false
  const cleaned = stripBOM(raw)
  let probed = 0
  for (const line of cleaned.split('\n')) {
    const trimmed = line.trim()
    if (!trimmed) continue
    if (
      trimmed.startsWith('data:') ||
      trimmed.startsWith('event:') ||
      trimmed.startsWith('id:') ||
      trimmed.startsWith('retry:') ||
      trimmed.startsWith(':')
    ) {
      return true
    }
    if (++probed >= PROBE_LIMIT) return false
  }
  return false
}

function parseSSEItems(raw: string): SSEItem[] {
  const items: SSEItem[] = []
  const cleaned = stripBOM(raw)
  for (const line of cleaned.split('\n')) {
    const trimmed = line.trim()
    if (!trimmed) continue
    if (!trimmed.startsWith('data:')) {
      items.push({ type: 'other', raw: trimmed, jsonRaw: '', isDone: false })
      continue
    }
    const payload = trimmed.startsWith('data: ') ? trimmed.slice(6) : trimmed.slice(5)
    if (payload === '[DONE]') {
      items.push({ type: 'data', raw: trimmed, jsonRaw: '', isDone: true })
      continue
    }
    try {
      items.push({ type: 'data', raw: trimmed, jsonRaw: JSON.stringify(JSON.parse(payload), null, 2), isDone: false })
    } catch {
      items.push({ type: 'data', raw: trimmed, jsonRaw: '', isDone: false })
    }
  }
  return items
}

const isSSE = computed(() => !tooLarge.value && isSSEFormat(props.body))

const items = computed<SSEItem[]>(() => (isSSE.value ? parseSSEItems(props.body) : []))

const visibleItems = computed(() => items.value.slice(0, CHUNK_LIMIT))

const truncated = computed(() => items.value.length > CHUNK_LIMIT)

// Assembled content: for OpenAI-style chat completion chunks, concat
// `delta.content` across all chunks. Returns null for non-OpenAI SSE
// streams (no `choices[].delta.content` seen), so the preview stays
// hidden — the per-chunk tree below already covers raw inspection.
const assembled = computed<string | null>(() => {
  if (!isSSE.value) return null
  let content = ''
  let hasOpenAIChunk = false
  for (const item of items.value) {
    if (item.isDone || !item.jsonRaw) continue
    try {
      const parsed = JSON.parse(item.jsonRaw) as { choices?: unknown }
      const choices = parsed?.choices
      if (!Array.isArray(choices)) continue
      hasOpenAIChunk = true
      if (choices.length === 0) continue
      const delta = (choices[0] as { delta?: { content?: unknown } } | undefined)?.delta
      if (delta && typeof delta.content === 'string') content += delta.content
    } catch {
      // jsonRaw is the formatted payload — parse shouldn't fail, ignore if it does
    }
  }
  return hasOpenAIChunk ? content : null
})
</script>

<style scoped lang="less">
.stream-body-viewer {
  font-size: var(--text-xs);
}

.body-pre-fallback {
  max-height: 480px;
  overflow: auto;
}

.body-too-large-hint {
  padding: var(--space-2) var(--space-4);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  border-bottom: 1px solid var(--color-border-subtle);
}

.body-pre {
  margin: 0;
  padding: var(--space-3) var(--space-4);
  white-space: pre-wrap;
  word-break: break-word;
  font-family: var(--font-mono, monospace);
  font-size: var(--text-xs);
  line-height: 1.6;
}

.sse-assembled {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--color-border-subtle);
  background: var(--color-accent-subtle);
}

.sse-assembled-label {
  font-size: var(--text-xs);
  font-weight: 600;
  color: var(--color-text-secondary);
}

.sse-assembled-content {
  margin: 0;
  max-height: 320px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  font-family: var(--font-mono, monospace);
  font-size: var(--text-xs);
  line-height: 1.6;
  color: var(--color-text);
}

.sse-truncated-hint {
  padding: var(--space-2) var(--space-4);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  border-bottom: 1px solid var(--color-border-subtle);
}

.sse-stream {
  max-height: 480px;
  overflow: auto;
  padding: var(--space-1) 0;
  font-family: var(--font-mono, monospace);
  font-size: var(--text-xs);
  line-height: 1.6;
}

.sse-chunk {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  padding: 3px var(--space-4);
  border-bottom: 1px solid var(--color-border-subtle);
}

.sse-chunk:last-child {
  border-bottom: none;
}

.sse-prefix {
  flex-shrink: 0;
  color: var(--color-text-muted);
  padding-top: 1px;
}

.sse-json {
  flex: 1;
  min-width: 0;
}

.sse-raw {
  color: var(--color-text-secondary);
  word-break: break-all;
}

.sse-raw--other,
.sse-done {
  color: var(--color-text-muted);
}

.sse-done {
  font-style: italic;
}
</style>
