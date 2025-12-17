# plarix-action

Minimal GitHub Action that estimates LLM cost impact on Pull Requests. Scans diffs for model changes, `max_tokens`, and retry counts, then posts a short Markdown report.

**No AI calls, no backend, no telemetry** — just a tiny Go binary reading patches.

## Quick Start

### 1. Add the workflow

Create `.github/workflows/plarix.yml` in your repository:

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

### 2. (Optional) Add configuration

Create `.plarix.yml` in your repository root to customize assumptions:

```yaml
assumptions:
  requests_per_day: 10000
  avg_input_tokens: 800
  avg_output_tokens: 400
  provider: "openai"      # or "anthropic"
  model: "gpt-4o-mini"    # your default model
```

If missing, defaults above are used and noted in the report.

### 3. Open a PR

The action runs automatically and:
- Posts a comment on the PR (updates the same comment on new commits)
- Writes to the GitHub Actions job summary

## Example Output

```
<!-- plarix-action -->

### LLM cost check
Pricing: 2025-12-17 (sources: https://openai.com/pricing, https://www.anthropic.com/pricing)

Assumptions: 10000 req/day | 800 in tokens | 400 out tokens | openai/gpt-4o-mini

| | Model | Est. per request | Est. monthly |
|---|---|---|---|
| Before | gpt-4o | $0.0028 | $840.00 |
| After  | gpt-4o-mini | $0.0004 | $108.00 |

Monthly spend trend:
Before |====================== $840.00
After  |==...                  $108.00

Observed changes (diff-based heuristics):
- Models: gpt-4o -> gpt-4o-mini
- max_tokens: 4096 -> 2048
```

## What It Does

- **Reads diffs** via GitHub API to find LLM-related changes
- **Detects** model name changes (`gpt-4o`, `claude-3-5-sonnet`, etc.)
- **Detects** `max_tokens` and retry count changes
- **Computes** cost estimates using bundled pricing data
- **Reports** BEFORE → AFTER comparison with ASCII trend bars

## What It Does NOT Do

- No AI/LLM calls
- No network calls for pricing (bundled in binary)
- No code execution from PR contents
- No telemetry or tracking
- No external services or databases

## Pricing Data

Pricing is embedded at compile time from `cmd/plarix/pricing.json` using Go's `//go:embed`. Supported models:

**OpenAI:** gpt-4o, gpt-4o-mini, gpt-4-turbo, gpt-3.5-turbo, o1, o1-mini, o3, o3-mini, o4-mini

**Anthropic:** claude-sonnet-4, claude-3-5-sonnet, claude-haiku-4, claude-3-5-haiku, claude-opus-4, claude-3-opus

To update pricing after verifying official pages:
```bash
# Edit prices in cmd/update-pricing/main.go, then:
make update-pricing
```

Sources: [OpenAI Pricing](https://openai.com/pricing) | [Anthropic Pricing](https://www.anthropic.com/pricing)

## Security

- **Read-only diff analysis** — no code execution
- **GitHub token** used only for REST API calls (PR files, comments)
- **No outbound calls** except GitHub API
- **No secrets required** beyond the default `GITHUB_TOKEN`

## Development

```bash
# Build
go build -o plarix ./cmd/plarix

# Update pricing (after editing cmd/update-pricing/main.go)
make update-pricing

# Test locally (requires GitHub env vars)
GITHUB_TOKEN=xxx GITHUB_EVENT_PATH=event.json GITHUB_REPOSITORY=owner/repo ./plarix
```

## Release

Tag with `vX.Y.Z` to trigger the release workflow, which builds and uploads `plarix_Linux_x86_64.tar.gz`.

## License

MIT — see [LICENSE](LICENSE)
