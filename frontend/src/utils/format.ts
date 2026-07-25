// Shared number / rate formatters used across dashboard, analytics, and cost
// optimization pages. Kept here so every page renders numbers with the same
// locale-aware grouping and the same rate precision.

// formatNumber renders an integer with locale-aware grouping separators
// (e.g. 1234567 -> "1,234,567").
export function formatNumber(n: number): string {
  return n.toLocaleString()
}

// formatNumber renders a [0,1] ratio as a percentage string with one decimal
// place (e.g. 0.875 -> "87.5%"). One decimal keeps column width stable.
export function formatRate(r: number): string {
  return `${(r * 100).toFixed(1)}%`
}
