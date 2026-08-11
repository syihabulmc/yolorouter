// hintTag renders the shared "compact tag whose reason lives in a hover
// tooltip" table cell. The tooltip only opens on hover, which keyboard and
// screen-reader users cannot do — so the tag itself is focusable and its
// accessible name carries the reason. Centralized because the pattern had
// started to drift: one copy shipped without any of the accessibility
// attributes.
import { h } from 'vue'
import { NTag, NTooltip } from 'naive-ui'

export interface HintTagOptions {
  /** Visible tag text (may be a bare glyph like '✗'). */
  text: string
  type: 'success' | 'warning' | 'error' | 'default'
  /** Tooltip body; empty renders the tag alone, still with an accessible name. */
  hint: string
  /** Accessible name; defaults to "text: hint" (or text alone when no hint). */
  ariaLabel?: string
}

export function hintTag(options: HintTagOptions) {
  const { text, type, hint } = options
  const ariaLabel = options.ariaLabel ?? (hint ? `${text}: ${hint}` : text)
  const tag = h(
    NTag,
    { size: 'small', bordered: false, type, role: 'img', 'aria-label': ariaLabel, tabindex: 0 },
    { default: () => text },
  )
  if (!hint) return tag
  return h(NTooltip, { trigger: 'hover' }, { trigger: () => tag, default: () => hint })
}
