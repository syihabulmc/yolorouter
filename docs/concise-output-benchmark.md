# Concise-Output Benchmark: How the Projected-Saving Figure Is Measured

The cost-optimization console shows a projected saving for the **Concise
Output** switch (a global custom system prompt that asks models to keep
replies short), normalized as **per 1 million output tokens**:

```
saving per 1M output tokens = (traffic-weighted output price) x coefficient
coefficient                 = 8.9%   (median, see below)
```

This page documents exactly how that coefficient was measured and publishes
the complete raw data, so the number can be challenged on its evidence
rather than taken on faith.

## Methodology

- **Fixed 10-question set** (below), covering output-length-sensitive task
  families: code generation, code explanation/review, log & long-text
  analysis, knowledge Q&A, translation, and rewriting. The set is frozen —
  it does not change between releases.
- **Paired on/off measurement**: for every (question, round, model) triple,
  the same request ran once with the switch off and once with it on,
  through a live gateway, back to back. The ON state used the example
  prompt shipped in the settings modal:
  `回答请保持简洁，去掉客套话和不必要的铺垫，保留完整语法和技术细节。`
  ("Keep replies concise. Skip pleasantries and filler; preserve full
  grammar and technical detail.")
- **10 questions x 3 rounds x 2 states x 5 models = 300 calls, 0 failures**,
  yielding **150 pairs**. Sampling parameters were each model's defaults.
- **Metric**: per pair, `r = (off_tokens − on_tokens) / off_tokens`, where
  tokens are the upstream-reported `completion_tokens` — **including
  reasoning tokens** on models that emit them, because that is what gets
  billed.
- **Aggregation**: the single global coefficient is the **median** of all
  150 pair ratios.

Models (routed through a live gateway): `claude-opus-4-7`,
`deepseek-v4-flash`, `deepseek-v4-pro`, `glm-5.1`, `qwen3.5-flash`.
Measured 2026-08-24.

## Results

| Statistic | Value |
|---|---|
| Median (the shipped coefficient) | **+8.9%** |
| 25th / 75th percentile | −9.4% / +25.1% |
| Min / max | −145.5% / +89.5% |
| Negative pairs | 37.3% (56 of 150) |

Per-model medians:

| Model | Median reduction |
|---|---|
| claude-opus-4-7 | +10.2% |
| deepseek-v4-flash | +46.8% |
| deepseek-v4-pro | **−12.0%** |
| glm-5.1 | +5.5% |
| qwen3.5-flash | +5.8% |

### How to read this

- The spread is wide and real. Asking for concision reliably shortens the
  prose, but **reasoning models can spend MORE thinking tokens** when asked
  to be concise — deepseek-v4-pro got longer on median, and the same
  question on the same model swung both ways across rounds (e.g. −130% and
  +45% on the SQL-explanation question). This is sampling variance in the
  reasoning chain, not a measurement error.
- A per-model coefficient table was considered and rejected: models change
  faster than such a table ages, and "why is my model in that band" is a
  question an estimate cannot settle. The median of a published, reproducible
  distribution is the honest single number.
- The projection assumes the custom system prompt actually asks for concise
  output. An unrelated prompt text voids the figure.
- Everything here is an estimate for operators, **not a financial figure**.

## The frozen question set

1. **[codegen]** Implement in Go a function `WordCount(text string, topN int)`
   that counts case-insensitively how often each word appears in an English
   text and returns the top `topN` `(word, count)` pairs by descending
   count. Provide complete runnable code including imports and a small
   `main` example.
2. **[codegen]** Write a Python CLI script that recursively walks a given
   directory, finds files larger than 10 MB modified within the last 7
   days, and prints their paths and sizes (human-readable) in descending
   size order. Provide complete code and usage instructions.
3. **[code-explain]** Explain what this SQL does and point out possible
   performance problems and improvements:
   `SELECT u.id, u.username, u.email, COUNT(o.id) AS order_count, SUM(o.amount) AS total FROM users u LEFT JOIN orders o ON o.user_id = u.id AND o.status = 'paid' WHERE u.created_at > '2026-01-01' AND u.status <> 'deleted' GROUP BY u.id, u.username, u.email HAVING COUNT(o.id) > 5 ORDER BY order_count DESC LIMIT 100;`
4. **[code-review]** Review this Go code and list every problem you find
   (error handling, resource leaks, edge cases):
   `func readConfig(path string) ([]byte, error) { f, err := os.Open(path); if err != nil { return nil, err } b, err := io.ReadAll(f); return b, err }`
5. **[log-analysis]** Given this one-hour sample of API access logs
   (format: `time method path status latency_ms`), analyze the traffic
   pattern, surface anomalies, and give operational recommendations:
   `10:01 GET /v1/models 200 12; 10:01 POST /v1/chat/completions 200 3421; 10:02 POST /v1/chat/completions 429 5; 10:02 POST /v1/chat/completions 200 5410; 10:03 GET /v1/models 200 9; 10:03 POST /v1/chat/completions 500 78; 10:04 POST /v1/chat/completions 200 4102; 10:05 GET /health 200 1; 10:05 POST /v1/chat/completions 200 6230; 10:06 POST /v1/chat/completions 429 4; 10:07 POST /v1/chat/completions 200 3871; 10:08 GET /v1/models 200 11; 10:09 POST /v1/chat/completions 200 5540; 10:10 POST /v1/chat/completions 502 120`
6. **[summary]** Read the product introduction below and produce a
   structured summary (target users, core features, pricing model,
   differentiation — at most three bullets each): “YoloRouter is a
   developer-facing AI model routing gateway. It aggregates multiple
   upstream model services behind one OpenAI-compatible endpoint; callers
   switch models by pointing base_url at the gateway. Provider-level
   failover is built in: one model can map to several providers and
   requests degrade automatically to the next one, invisibly to the caller.
   It also provides API key management, per-account usage analytics and
   budget caps. Deployment is a single binary with SQLite built in, or
   PostgreSQL. Pricing is upstream cost + 5%, no monthly fee.”
7. **[knowledge]** Why does TCP need a three-way handshake rather than two?
   Explain from the protocol's design goals (preventing historical
   connections, synchronizing initial sequence numbers, confirming
   two-way communication) and what breaks with two.
8. **[knowledge]** Explain the four database transaction isolation levels:
   what each solves, which anomalies remain (dirty read, non-repeatable
   read, phantom read), and MySQL InnoDB's default level and how it is
   implemented.
9. **[translate]** Translate this English technical documentation into
   Chinese, preserving terminology: “The gateway normalizes every inbound
   request into an intermediate representation before dispatching it to an
   upstream provider. This decouples the ingress protocol spoken by the
   caller from the egress protocol spoken by the provider, so a new
   provider can be added without touching any caller-side code. Streaming
   responses are relayed chunk-by-chunk with backpressure, and usage
   reported by the upstream is reconciled into the audit log at settlement
   time.”
10. **[rewrite]** Rewrite this rambling email to be concise and
    professional, keeping every key fact (time, place, agenda, prep work):
    “hi 大家好，是这样的，我们本来定在下周三下午的开会时间，因为会议室被占了，所以现在改到周四上午十点了，地点还是老地方 B 栋 301。这次会议主要想跟大家同步一下二季度的进度，然后讨论一下下个季度的计划，另外呢，麻烦大家提前把自己负责模块的数据准备好，最好是能发我一份，我在会上统一汇总，谢谢大家配合，有什么问题随时找我。”

## Raw data — all 150 pairs

`off` / `on` are upstream-reported `completion_tokens` (reasoning tokens
included); `r = (off − on) / off`. Positive `r` means the concise prompt
shortened the output.

| Model | Question | Round | off | on | r |
|---|---|---|---|---|---|
| claude-opus-4-7 | q1-codegen-wordcount | 1 | 627 | 658 | -4.9% |
| claude-opus-4-7 | q1-codegen-wordcount | 2 | 730 | 606 | 17.0% |
| claude-opus-4-7 | q1-codegen-wordcount | 3 | 611 | 623 | -2.0% |
| claude-opus-4-7 | q2-codegen-walk | 1 | 911 | 763 | 16.2% |
| claude-opus-4-7 | q2-codegen-walk | 2 | 1077 | 1055 | 2.0% |
| claude-opus-4-7 | q2-codegen-walk | 3 | 987 | 1119 | -13.4% |
| claude-opus-4-7 | q3-sql-explain | 1 | 1184 | 1072 | 9.5% |
| claude-opus-4-7 | q3-sql-explain | 2 | 1204 | 1277 | -6.1% |
| claude-opus-4-7 | q3-sql-explain | 3 | 1207 | 1189 | 1.5% |
| claude-opus-4-7 | q4-go-review | 1 | 799 | 436 | 45.4% |
| claude-opus-4-7 | q4-go-review | 2 | 577 | 512 | 11.3% |
| claude-opus-4-7 | q4-go-review | 3 | 699 | 453 | 35.2% |
| claude-opus-4-7 | q5-log-analysis | 1 | 909 | 806 | 11.3% |
| claude-opus-4-7 | q5-log-analysis | 2 | 868 | 785 | 9.6% |
| claude-opus-4-7 | q5-log-analysis | 3 | 911 | 800 | 12.2% |
| claude-opus-4-7 | q6-longtext-summary | 1 | 286 | 266 | 7.0% |
| claude-opus-4-7 | q6-longtext-summary | 2 | 252 | 266 | -5.6% |
| claude-opus-4-7 | q6-longtext-summary | 3 | 282 | 264 | 6.4% |
| claude-opus-4-7 | q7-tcp-handshake | 1 | 1201 | 1070 | 10.9% |
| claude-opus-4-7 | q7-tcp-handshake | 2 | 1020 | 930 | 8.8% |
| claude-opus-4-7 | q7-tcp-handshake | 3 | 1177 | 975 | 17.2% |
| claude-opus-4-7 | q8-isolation-levels | 1 | 1016 | 1003 | 1.3% |
| claude-opus-4-7 | q8-isolation-levels | 2 | 1149 | 933 | 18.8% |
| claude-opus-4-7 | q8-isolation-levels | 3 | 1102 | 883 | 19.9% |
| claude-opus-4-7 | q9-translate | 1 | 156 | 157 | -0.6% |
| claude-opus-4-7 | q9-translate | 2 | 173 | 134 | 22.5% |
| claude-opus-4-7 | q9-translate | 3 | 132 | 149 | -12.9% |
| claude-opus-4-7 | q10-rewrite-email | 1 | 168 | 130 | 22.6% |
| claude-opus-4-7 | q10-rewrite-email | 2 | 176 | 138 | 21.6% |
| claude-opus-4-7 | q10-rewrite-email | 3 | 175 | 147 | 16.0% |
| deepseek-v4-flash | q1-codegen-wordcount | 1 | 1189 | 890 | 25.1% |
| deepseek-v4-flash | q1-codegen-wordcount | 2 | 2956 | 1866 | 36.9% |
| deepseek-v4-flash | q1-codegen-wordcount | 3 | 2440 | 1583 | 35.1% |
| deepseek-v4-flash | q2-codegen-walk | 1 | 1035 | 623 | 39.8% |
| deepseek-v4-flash | q2-codegen-walk | 2 | 1025 | 636 | 38.0% |
| deepseek-v4-flash | q2-codegen-walk | 3 | 1299 | 1890 | -45.5% |
| deepseek-v4-flash | q3-sql-explain | 1 | 3377 | 5698 | -68.7% |
| deepseek-v4-flash | q3-sql-explain | 2 | 2994 | 2727 | 8.9% |
| deepseek-v4-flash | q3-sql-explain | 3 | 5010 | 1056 | 78.9% |
| deepseek-v4-flash | q4-go-review | 1 | 1050 | 499 | 52.5% |
| deepseek-v4-flash | q4-go-review | 2 | 1605 | 239 | 85.1% |
| deepseek-v4-flash | q4-go-review | 3 | 1125 | 731 | 35.0% |
| deepseek-v4-flash | q5-log-analysis | 1 | 6303 | 663 | 89.5% |
| deepseek-v4-flash | q5-log-analysis | 2 | 5149 | 907 | 82.4% |
| deepseek-v4-flash | q5-log-analysis | 3 | 4557 | 3110 | 31.8% |
| deepseek-v4-flash | q6-longtext-summary | 1 | 278 | 409 | -47.1% |
| deepseek-v4-flash | q6-longtext-summary | 2 | 943 | 293 | 68.9% |
| deepseek-v4-flash | q6-longtext-summary | 3 | 605 | 352 | 41.8% |
| deepseek-v4-flash | q7-tcp-handshake | 1 | 5794 | 1785 | 69.2% |
| deepseek-v4-flash | q7-tcp-handshake | 2 | 2428 | 3724 | -53.4% |
| deepseek-v4-flash | q7-tcp-handshake | 3 | 1637 | 621 | 62.1% |
| deepseek-v4-flash | q8-isolation-levels | 1 | 1288 | 622 | 51.7% |
| deepseek-v4-flash | q8-isolation-levels | 2 | 6123 | 641 | 89.5% |
| deepseek-v4-flash | q8-isolation-levels | 3 | 2629 | 331 | 87.4% |
| deepseek-v4-flash | q9-translate | 1 | 1280 | 334 | 73.9% |
| deepseek-v4-flash | q9-translate | 2 | 249 | 354 | -42.2% |
| deepseek-v4-flash | q9-translate | 3 | 1235 | 367 | 70.3% |
| deepseek-v4-flash | q10-rewrite-email | 1 | 1439 | 240 | 83.3% |
| deepseek-v4-flash | q10-rewrite-email | 2 | 189 | 181 | 4.2% |
| deepseek-v4-flash | q10-rewrite-email | 3 | 890 | 156 | 82.5% |
| deepseek-v4-pro | q1-codegen-wordcount | 1 | 1421 | 1250 | 12.0% |
| deepseek-v4-pro | q1-codegen-wordcount | 2 | 898 | 1895 | -111.0% |
| deepseek-v4-pro | q1-codegen-wordcount | 3 | 1164 | 1008 | 13.4% |
| deepseek-v4-pro | q2-codegen-walk | 1 | 1317 | 1479 | -12.3% |
| deepseek-v4-pro | q2-codegen-walk | 2 | 2012 | 1033 | 48.7% |
| deepseek-v4-pro | q2-codegen-walk | 3 | 1477 | 2345 | -58.8% |
| deepseek-v4-pro | q3-sql-explain | 1 | 1795 | 1169 | 34.9% |
| deepseek-v4-pro | q3-sql-explain | 2 | 834 | 1918 | -130.0% |
| deepseek-v4-pro | q3-sql-explain | 3 | 2698 | 1492 | 44.7% |
| deepseek-v4-pro | q4-go-review | 1 | 742 | 521 | 29.8% |
| deepseek-v4-pro | q4-go-review | 2 | 356 | 874 | -145.5% |
| deepseek-v4-pro | q4-go-review | 3 | 597 | 256 | 57.1% |
| deepseek-v4-pro | q5-log-analysis | 1 | 557 | 925 | -66.1% |
| deepseek-v4-pro | q5-log-analysis | 2 | 1263 | 1479 | -17.1% |
| deepseek-v4-pro | q5-log-analysis | 3 | 1172 | 1137 | 3.0% |
| deepseek-v4-pro | q6-longtext-summary | 1 | 981 | 1514 | -54.3% |
| deepseek-v4-pro | q6-longtext-summary | 2 | 971 | 2221 | -128.7% |
| deepseek-v4-pro | q6-longtext-summary | 3 | 875 | 1811 | -107.0% |
| deepseek-v4-pro | q7-tcp-handshake | 1 | 1255 | 1413 | -12.6% |
| deepseek-v4-pro | q7-tcp-handshake | 2 | 1053 | 1290 | -22.5% |
| deepseek-v4-pro | q7-tcp-handshake | 3 | 1458 | 1310 | 10.2% |
| deepseek-v4-pro | q8-isolation-levels | 1 | 1602 | 1191 | 25.7% |
| deepseek-v4-pro | q8-isolation-levels | 2 | 1430 | 1564 | -9.4% |
| deepseek-v4-pro | q8-isolation-levels | 3 | 905 | 1011 | -11.7% |
| deepseek-v4-pro | q9-translate | 1 | 521 | 790 | -51.6% |
| deepseek-v4-pro | q9-translate | 2 | 523 | 655 | -25.2% |
| deepseek-v4-pro | q9-translate | 3 | 702 | 749 | -6.7% |
| deepseek-v4-pro | q10-rewrite-email | 1 | 439 | 1060 | -141.5% |
| deepseek-v4-pro | q10-rewrite-email | 2 | 780 | 450 | 42.3% |
| deepseek-v4-pro | q10-rewrite-email | 3 | 785 | 857 | -9.2% |
| glm-5.1 | q1-codegen-wordcount | 1 | 1633 | 2059 | -26.1% |
| glm-5.1 | q1-codegen-wordcount | 2 | 1326 | 1631 | -23.0% |
| glm-5.1 | q1-codegen-wordcount | 3 | 1376 | 1316 | 4.4% |
| glm-5.1 | q2-codegen-walk | 1 | 1477 | 1502 | -1.7% |
| glm-5.1 | q2-codegen-walk | 2 | 1775 | 1359 | 23.4% |
| glm-5.1 | q2-codegen-walk | 3 | 1695 | 1380 | 18.6% |
| glm-5.1 | q3-sql-explain | 1 | 2085 | 2456 | -17.8% |
| glm-5.1 | q3-sql-explain | 2 | 2043 | 2152 | -5.3% |
| glm-5.1 | q3-sql-explain | 3 | 2663 | 1677 | 37.0% |
| glm-5.1 | q4-go-review | 1 | 946 | 447 | 52.7% |
| glm-5.1 | q4-go-review | 2 | 893 | 534 | 40.2% |
| glm-5.1 | q4-go-review | 3 | 791 | 700 | 11.5% |
| glm-5.1 | q5-log-analysis | 1 | 2077 | 1924 | 7.4% |
| glm-5.1 | q5-log-analysis | 2 | 2065 | 2089 | -1.2% |
| glm-5.1 | q5-log-analysis | 3 | 2126 | 1984 | 6.7% |
| glm-5.1 | q6-longtext-summary | 1 | 1265 | 1382 | -9.2% |
| glm-5.1 | q6-longtext-summary | 2 | 1130 | 854 | 24.4% |
| glm-5.1 | q6-longtext-summary | 3 | 1112 | 992 | 10.8% |
| glm-5.1 | q7-tcp-handshake | 1 | 1852 | 2020 | -9.1% |
| glm-5.1 | q7-tcp-handshake | 2 | 1585 | 1613 | -1.8% |
| glm-5.1 | q7-tcp-handshake | 3 | 1590 | 1554 | 2.3% |
| glm-5.1 | q8-isolation-levels | 1 | 1309 | 1428 | -9.1% |
| glm-5.1 | q8-isolation-levels | 2 | 1453 | 1587 | -9.2% |
| glm-5.1 | q8-isolation-levels | 3 | 1582 | 1218 | 23.0% |
| glm-5.1 | q9-translate | 1 | 1296 | 1196 | 7.7% |
| glm-5.1 | q9-translate | 2 | 1339 | 1125 | 16.0% |
| glm-5.1 | q9-translate | 3 | 1327 | 1136 | 14.4% |
| glm-5.1 | q10-rewrite-email | 1 | 755 | 823 | -9.0% |
| glm-5.1 | q10-rewrite-email | 2 | 697 | 535 | 23.2% |
| glm-5.1 | q10-rewrite-email | 3 | 681 | 775 | -13.8% |
| qwen3.5-flash | q1-codegen-wordcount | 1 | 3181 | 4015 | -26.2% |
| qwen3.5-flash | q1-codegen-wordcount | 2 | 2082 | 3653 | -75.5% |
| qwen3.5-flash | q1-codegen-wordcount | 3 | 1506 | 2914 | -93.5% |
| qwen3.5-flash | q2-codegen-walk | 1 | 2757 | 2134 | 22.6% |
| qwen3.5-flash | q2-codegen-walk | 2 | 3837 | 2920 | 23.9% |
| qwen3.5-flash | q2-codegen-walk | 3 | 2563 | 2873 | -12.1% |
| qwen3.5-flash | q3-sql-explain | 1 | 2317 | 3835 | -65.5% |
| qwen3.5-flash | q3-sql-explain | 2 | 4705 | 4096 | 12.9% |
| qwen3.5-flash | q3-sql-explain | 3 | 3276 | 3641 | -11.1% |
| qwen3.5-flash | q4-go-review | 1 | 3238 | 3111 | 3.9% |
| qwen3.5-flash | q4-go-review | 2 | 3268 | 2623 | 19.7% |
| qwen3.5-flash | q4-go-review | 3 | 2806 | 2178 | 22.4% |
| qwen3.5-flash | q5-log-analysis | 1 | 3305 | 3456 | -4.6% |
| qwen3.5-flash | q5-log-analysis | 2 | 2861 | 2809 | 1.8% |
| qwen3.5-flash | q5-log-analysis | 3 | 2794 | 2581 | 7.6% |
| qwen3.5-flash | q6-longtext-summary | 1 | 2125 | 1478 | 30.4% |
| qwen3.5-flash | q6-longtext-summary | 2 | 1952 | 4318 | -121.2% |
| qwen3.5-flash | q6-longtext-summary | 3 | 2150 | 1969 | 8.4% |
| qwen3.5-flash | q7-tcp-handshake | 1 | 3807 | 3200 | 15.9% |
| qwen3.5-flash | q7-tcp-handshake | 2 | 4408 | 3435 | 22.1% |
| qwen3.5-flash | q7-tcp-handshake | 3 | 5942 | 4147 | 30.2% |
| qwen3.5-flash | q8-isolation-levels | 1 | 1707 | 1794 | -5.1% |
| qwen3.5-flash | q8-isolation-levels | 2 | 1443 | 2738 | -89.7% |
| qwen3.5-flash | q8-isolation-levels | 3 | 2050 | 2686 | -31.0% |
| qwen3.5-flash | q9-translate | 1 | 2737 | 1545 | 43.6% |
| qwen3.5-flash | q9-translate | 2 | 2932 | 2916 | 0.5% |
| qwen3.5-flash | q9-translate | 3 | 4002 | 3245 | 18.9% |
| qwen3.5-flash | q10-rewrite-email | 1 | 2248 | 2057 | 8.5% |
| qwen3.5-flash | q10-rewrite-email | 2 | 2566 | 1721 | 32.9% |
| qwen3.5-flash | q10-rewrite-email | 3 | 1349 | 1685 | -24.9% |

## Reproducing

Run any gateway instance with the models above, issue the frozen questions
with the switch off/on (the ON state must use the example prompt verbatim),
3 rounds each, and take the median of the pair ratios. Expect your exact
numbers to differ — models and providers drift — but the shape (wide
spread, reasoning models occasionally negative) should hold.
