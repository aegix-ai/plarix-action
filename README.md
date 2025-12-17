# plarix-action

**Honest** LLM cost analysis for Pull Requests. No fake numbers, no misleading defaults.

Plarix analyzes your PR's actual impact on LLM costs using **real data** — either measured token usage from your CI tests or configured estimates from your `.plarix.yml`.

**No AI calls, no backend, no telemetry** — just a tiny Go binary that tells the truth.

## Why Plarix?

Most cost estimation tools show impressive-looking numbers based on arbitrary defaults. Plarix refuses to do that.

- **No data → No fake numbers.** Without configuration or measured usage, Plarix only reports diff heuristics.
- **Measured mode → Real costs.** Connect your CI tests to show actual token usage changes.
- **Clear data sources.** Every number is labeled with where it came from.

## Quick Start

### Basic Usage (Heuristics Only)

Without configuration, Plarix scans your PR diff for LLM-related changes:

```yaml
name: plarix
on:
  pull_request:
    types: [opened, synchronize, reopened]
permissions:
  contents: read
  pull-requests: write
jobs:
  llm-cost:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: aegix-ai/plarix-action@v0
```

Output (heuristic mode):
```
### LLM cost check
ℹ️ Data source: HEURISTIC_ONLY — no .plarix.yml or measured usage found

Observed changes (diff-based heuristics):
- Model change: gpt-4o → gpt-4o-mini
- max_tokens change: 4096 → 2048

💡 To see cost estimates, add a .plarix.yml config file.
💡 For real measured costs, see: examples/plarix-measured.yml
```

### Configured Estimates

Add `.plarix.yml` to your repository for estimated costs:

```yaml
assumptions:
  requests_per_day: 10000
  avg_input_tokens: 800
  avg_output_tokens: 400
  provider: "openai"
  model: "gpt-4o-mini"
```

Output (configured estimate mode):
```
### LLM cost check
ℹ️ Data source: CONFIGURED_ESTIMATE — from .plarix.yml assumptions

**Before** = model detected in removed lines (or .plarix.yml default)
**After** = model detected in added lines (or .plarix.yml default)

| | Model | Est. per request | Est. monthly |
|---|---|---|---|
| Before | gpt-4o | $0.0028 | $840.00 |
| After | gpt-4o-mini | $0.0004 | $108.00 |

📐 Formula: (requests_per_day × 30) × (input_tokens × input_price + output_tokens × output_price)
```

### 🌟 Measured Mode (Recommended)

The most accurate way: measure actual token usage from your CI tests.

Set these environment variables:
- `PLARIX_MEASURE_BASE` — Path to JSONL file with BASE commit token usage
- `PLARIX_MEASURE_HEAD` — Path to JSONL file with HEAD commit token usage

See [`examples/plarix-measured.yml`](examples/plarix-measured.yml) for a complete workflow.

Output (measured mode):
```
### LLM cost check
ℹ️ Data source: MEASURED — from CI test token usage logs

**Before** = measured from BASE commit test run
**After** = measured from HEAD commit test run

| Provider/Model | Before Tokens | After Tokens | Before Cost | After Cost | Δ Cost |
|---|---|---|---|---|---|
| openai/gpt-4o | 1,500 in / 500 out | 1,800 in / 600 out | $0.0113 | $0.0135 | +$0.0023 |
| anthropic/claude-sonnet-4 | 2,000 in / 800 out | 1,800 in / 700 out | $0.0180 | $0.0159 | -$0.0021 |

**Total:** Before $0.0293 → After $0.0294 (Δ +$0.0001)
```

## JSONL Format

For measured mode, your test suite must output JSONL with one API call per line:

```json
{"provider":"openai","model":"gpt-4o","input_tokens":1500,"output_tokens":500}
{"provider":"anthropic","model":"claude-sonnet-4","input_tokens":2000,"output_tokens":800,"cached_input_tokens":500}
```

Required fields:
- `provider` — `"openai"` or `"anthropic"`
- `model` — Model identifier (e.g., `"gpt-4o"`, `"claude-sonnet-4"`)
- `input_tokens` — Number of input/prompt tokens
- `output_tokens` — Number of output/completion tokens

Optional fields:
- `cached_input_tokens` — Tokens served from cache (Anthropic prompt caching)
- `timestamp` — ISO 8601 timestamp

## Data Source Labels

Plarix always tells you where numbers come from:

| Label | Meaning |
|-------|---------|
| `MEASURED` | Real token counts from your CI test runs |
| `CONFIGURED_ESTIMATE` | Estimates based on `.plarix.yml` assumptions |
| `HEURISTIC_ONLY` | Only diff analysis, no cost numbers |

## What It Detects

From PR diffs (heuristic analysis):
- Model name changes (`gpt-4o` → `gpt-4o-mini`)
- `max_tokens` parameter changes
- Retry count changes

## Supported Models

**OpenAI:** gpt-4o, gpt-4o-mini, gpt-4-turbo, gpt-3.5-turbo, o1, o1-mini, o3, o3-mini, o4-mini

**Anthropic:** claude-sonnet-4, claude-3-5-sonnet, claude-haiku-4, claude-3-5-haiku, claude-opus-4, claude-3-opus

Pricing sources: [OpenAI](https://platform.openai.com/docs/pricing) | [Anthropic](https://www.anthropic.com/pricing)

## Security

- **Read-only** — No code execution from PR contents
- **No external calls** — Pricing embedded at compile time
- **Minimal permissions** — Only needs `pull-requests: write` for comments
- **No telemetry** — Nothing leaves your Actions runner

## Development

```bash
# Build
go build -o plarix ./cmd/plarix

# Run tests
go test ./...

# Update pricing (after editing cmd/update-pricing/main.go)
make update-pricing
```

## License

MIT — see [LICENSE](LICENSE)
