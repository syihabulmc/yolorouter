import { describe, expect, it } from 'vitest'
import { parseBulkKeyLines } from './providerValidators'

describe('parseBulkKeyLines', () => {
  it('parses a single label:key line', () => {
    const result = parseBulkKeyLines('prod-1:sk-abc123def')
    expect(result.valid).toEqual([{ label: 'prod-1', plaintext: 'sk-abc123def' }])
    expect(result.duplicates).toEqual([])
    expect(result.errors).toEqual([])
  })

  it('treats a line with no colon as auto-label and uses the whole line as key', () => {
    const result = parseBulkKeyLines('sk-abc123def')
    expect(result.valid).toEqual([{ label: '', plaintext: 'sk-abc123def' }])
  })

  it('splits on the first colon only, keeping later colons in the key', () => {
    const result = parseBulkKeyLines('prod-1:abc:def:ghi')
    expect(result.valid).toEqual([{ label: 'prod-1', plaintext: 'abc:def:ghi' }])
  })

  it('parses multiple lines, dropping blanks and trimming whitespace', () => {
    const result = parseBulkKeyLines('  a:sk-abcdef1 \n\n  sk-abcdef2  \n  b:sk-abcdef3  ')
    expect(result.valid).toEqual([
      { label: 'a', plaintext: 'sk-abcdef1' },
      { label: '', plaintext: 'sk-abcdef2' },
      { label: 'b', plaintext: 'sk-abcdef3' },
    ])
  })

  it('rejects an empty key with a line-numbered error', () => {
    const result = parseBulkKeyLines('a:\nb:sk-abcdef2')
    expect(result.valid).toEqual([{ label: 'b', plaintext: 'sk-abcdef2' }])
    expect(result.errors).toEqual([{ line: 1, reason: 'empty_key' }])
  })

  it('rejects a key shorter than 8 chars with a line-numbered error', () => {
    const result = parseBulkKeyLines('a:short7')
    expect(result.valid).toEqual([])
    expect(result.errors).toEqual([{ line: 1, reason: 'key_too_short' }])
  })

  it('rejects a label longer than 30 chars with a line-numbered error', () => {
    const longLabel = 'x'.repeat(31)
    const result = parseBulkKeyLines(`${longLabel}:sk-validkey`)
    expect(result.valid).toEqual([])
    expect(result.errors).toEqual([{ line: 1, reason: 'label_too_long' }])
  })

  it('detects a duplicate plaintext within the batch (label-less counted as raw key)', () => {
    const result = parseBulkKeyLines('a:sk-samekey\nb:sk-samekey')
    expect(result.valid).toEqual([{ label: 'a', plaintext: 'sk-samekey' }])
    expect(result.duplicates).toEqual([{ line: 2, plaintext: 'sk-samekey' }])
  })

  it('treats a label-less duplicate (same key twice) as one valid + one duplicate', () => {
    const result = parseBulkKeyLines('sk-samekey\nsk-samekey')
    expect(result.valid).toEqual([{ label: '', plaintext: 'sk-samekey' }])
    expect(result.duplicates).toEqual([{ line: 2, plaintext: 'sk-samekey' }])
  })

  it('returns empty arrays for empty / whitespace-only input', () => {
    expect(parseBulkKeyLines('')).toEqual({ valid: [], duplicates: [], errors: [] })
    expect(parseBulkKeyLines('   \n\n  ')).toEqual({ valid: [], duplicates: [], errors: [] })
  })
})
