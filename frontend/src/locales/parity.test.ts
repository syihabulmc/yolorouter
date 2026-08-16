import { describe, expect, it } from 'vitest'
import zhCN from './zh-CN'
import en from './en'

// Locale parity gate: zh-CN and en must expose the exact same message tree.
// Keys are compared in both directions, and every leaf's named placeholders
// ({name}, {count}, ...) must match across locales — a missing placeholder
// renders as a literal hole in the UI and is otherwise silent.

type MessageTree = Record<string, unknown>

function leafPaths(tree: MessageTree, prefix = ''): Map<string, string> {
  const paths = new Map<string, string>()
  for (const [key, value] of Object.entries(tree)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (value !== null && typeof value === 'object') {
      for (const [nested, message] of leafPaths(value as MessageTree, path)) {
        paths.set(nested, message)
      }
    } else {
      paths.set(path, String(value))
    }
  }
  return paths
}

// Only identifier-shaped params ({name}, {0}) are part of the signature, so
// vue-i18n literal-brace escapes ({'{'}) never match; prose that merely
// looks like a placeholder still counts, which errs on the strict side.
function placeholderSignature(message: string): string {
  const names = [...message.matchAll(/\{([A-Za-z0-9_]+)\}/g)].map((m) => m[1])
  return [...new Set(names)].sort().join(',')
}

describe('locale parity', () => {
  const zh = leafPaths(zhCN as MessageTree)
  const enPaths = leafPaths(en as MessageTree)

  it('zh-CN has no keys missing from en', () => {
    const missing = [...zh.keys()].filter((path) => !enPaths.has(path))
    expect(missing, 'keys present in zh-CN but absent from en').toEqual([])
  })

  it('en has no keys missing from zh-CN', () => {
    const missing = [...enPaths.keys()].filter((path) => !zh.has(path))
    expect(missing, 'keys present in en but absent from zh-CN').toEqual([])
  })

  it('every message uses the same named placeholders in both locales', () => {
    const drifted: string[] = []
    for (const [path, message] of zh) {
      const other = enPaths.get(path)
      // Missing keys are the two tests above; here only pairs are compared.
      if (other === undefined) continue
      if (placeholderSignature(message) !== placeholderSignature(other)) drifted.push(path)
    }
    expect(drifted, 'placeholder sets differ between zh-CN and en').toEqual([])
  })
})
