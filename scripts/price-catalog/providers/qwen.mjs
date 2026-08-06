// Qwen (通义千问 / 阿里百炼) price fetcher.
//
// The pricing page (help.aliyun.com/zh/model-studio/model-pricing) is server-
// side rendered — the full table ships in the initial HTML (~3 MB), no JS
// needed. Each model version is a row of <td> cells:
//   <td>非思考和思考模式</td><td>0&lt;Token≤1M</td><td>12元</td><td>36元</td><td>100万Token</td>
// i.e. [mode, token-range, input, output, unit]. We extract every price row,
// then map the dated version names (qwen3.7-max-2026-06-08) down to the
// canonical catalog names (qwen3.7-max) — the page marks aliases via
// "当前能力等同于 X" which we use to pick the canonical entry.

import { fetchWithTimeout, parseYuan } from "./_fetch.mjs";

const HOST = "dashscope.aliyuncs.com";
const PRICING_PAGE = "https://help.aliyun.com/zh/model-studio/model-pricing";

// canonicalName strips the dated snapshot suffix (-YYYY-MM-DD) and preview/alias
// qualifiers so qwen3.7-max-2026-06-08 → qwen3.7-max. The catalog keys on the
// canonical family name.
function canonicalName(versionName) {
  return versionName
    .toLowerCase()
    .replace(/-\d{4}-\d{2}-\d{2}$/, "") // date snapshot
    .replace(/-preview$/, "")            // preview variant
    .trim();
}

export async function fetchQwen() {
  const resp = await fetchWithTimeout(PRICING_PAGE);
  if (!resp.ok) throw new Error(`qwen: HTTP ${resp.status}`);
  const html = await resp.text();

  // Extract every <tr>...</tr> block, then its <td> cells. A price row is one
  // whose cells include at least two "N元" values (input + output). This filter
  // discards header rows, section rows, and the "Batch 调用 半价" note rows.
  const rowRe = /<tr[^>]*>([\s\S]*?)<\/tr>/g;
  const cellRe = /<td[^>]*>([\s\S]*?)<\/td>/g;
  const stripTags = (s) => s.replace(/<[^>]+>/g, "").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&").trim();

  const entries = new Map();
  let rowMatch;
  while ((rowMatch = rowRe.exec(html)) !== null) {
    const cells = [];
    let cm;
    const inner = rowMatch[1];
    while ((cm = cellRe.exec(inner)) !== null) {
      cells.push(stripTags(cm[1]));
    }
    // The model name cell is a pure ASCII identifier (qwen3.7-max-2026-06-08).
    // Cells that mix the name with Chinese notes ("qwen3.7-max当前能力等同于…")
    // are alias/description rows, not the canonical model row — reject them by
    // requiring the whole cell to be a clean qwen identifier.
    const nameCell = cells.find((c) => /^qwen[\w.-]*$/i.test(c));
    if (!nameCell) continue;
    const prices = cells.map(parseYuan).filter((p) => p != null);
    if (prices.length < 2) continue; // need input + output

    const name = canonicalName(nameCell);
    // Keep the first (cheapest-tier) row per canonical name; later rows for the
    // same name are higher token tiers.
    if (!entries.has(name)) {
      entries.set(name, { input: prices[0], output: prices[1], cacheWrite: null, cacheRead: null });
    }
  }

  if (entries.size === 0) {
    throw new Error("qwen: parsed 0 models — HTML table structure likely changed");
  }
  return { host: HOST, models: Object.fromEntries(entries) };
}
