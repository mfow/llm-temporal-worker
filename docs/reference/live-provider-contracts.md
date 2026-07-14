# Guarded Live Provider Contracts

`integration/live` is a small, credentialed contract suite for facts that
offline fixtures cannot establish: authentication, provider wire acceptance,
the reported actual service class, provider request and response IDs, usage,
reported cost, and continuation behavior. It is excluded from ordinary tests
by the `live` build tag.

The suite is intentionally fail-closed. A real invocation is possible only
when all three exact environment values equal `"1"`:

1. `LLMTW_LIVE_TESTS=1`
2. `LLMTW_LIVE_AUTHORIZED=1`
3. the selected profile's enable flag from the table below

All other values, including common truthy spellings, leave the profile
disabled. Gate evaluation happens before the harness constructs an adapter or
looks up credentials. The protected manual release workflow supplied by Task
24 must set the first two values only after environment approval, set only the
selected profile flag, and inject only that profile's scoped credentials.
Fork pull requests, ordinary pull requests, and scheduled workflows must not
set any of these flags or receive these credentials.

## Compile-only safety check

This command compiles the complete live harness without making a provider
request:

```sh
go test -tags=live ./integration/live -run '^$'
```

The workflow may use that command in uncredentialed jobs. A protected manual
run selects one profile by adding its flag and the two suite gates; it runs
`TestLiveProviderContracts` with scoped credentials. The harness does not
read a provider's environment variables until those gates pass.

## Pinned profiles

Every profile uses tenant `llmtw-live-contract`, the exact prompt `Reply with
exactly: live-contract-ok`, a maximum output of 8 tokens, and a maximum actual
reported spend of 25,000 microUSD (USD 0.025). Models, protocols, and base
endpoints are checked in rather than supplied by an environment variable.

| Profile | Protocol and pinned model | Profile gate | Credential source and additional configuration | Continuation |
| --- | --- | --- | --- | --- |
| `openai-responses` | OpenAI Responses; `gpt-4.1-mini` | `LLMTW_LIVE_OPENAI_RESPONSES` | `OPENAI_API_KEY`; OpenAI v1 endpoint | Pinned |
| `azure-responses` | Azure OpenAI Responses; `gpt-4.1-mini` | `LLMTW_LIVE_AZURE_RESPONSES` | Azure Default Credential; `LLMTW_LIVE_AZURE_OPENAI_ENDPOINT` | Pinned |
| `openai-chat` | OpenAI Chat Completions; `gpt-4.1-mini` | `LLMTW_LIVE_OPENAI_CHAT` | `OPENAI_API_KEY`; OpenAI v1 endpoint | Rejected before invocation |
| `openrouter-chat` | OpenRouter Chat Completions; `openai/gpt-4.1-mini` | `LLMTW_LIVE_OPENROUTER_CHAT` | `OPENROUTER_API_KEY`; OpenRouter API endpoint | Rejected before invocation |
| `exa-chat` | Exa Chat; `exa` | `LLMTW_LIVE_EXA_CHAT` | `EXA_API_KEY`; Exa API endpoint | Rejected before invocation |
| `anthropic-direct` | Anthropic Messages; `claude-3-5-haiku-latest` | `LLMTW_LIVE_ANTHROPIC_DIRECT` | `ANTHROPIC_API_KEY`; Anthropic API endpoint | Pinned |
| `anthropic-aws` | Anthropic Messages through AWS; `claude-3-5-haiku-latest` | `LLMTW_LIVE_ANTHROPIC_AWS` | AWS default credential chain; `LLMTW_LIVE_ANTHROPIC_AWS_WORKSPACE_ID` | Pinned |
| `bedrock-anthropic` | Amazon Bedrock Anthropic; `anthropic.claude-3-5-haiku-20241022-v1:0` | `LLMTW_LIVE_BEDROCK_ANTHROPIC` | AWS default credential chain; default AWS SDK endpoint resolution | Pinned |

The request deliberately omits the public service class. The worker must
normalize that omission to `standard`. Before invocation, the harness checks
that the compiled call still carries the pinned endpoint, protocol family,
model, operation key, normalized `standard` class, and a non-empty provider
tier. A provider-internal value such as `default` is an explicit mapping for
the public `standard` class; it is never permission to use an unspecified
provider default.

For profiles that do not support continuations, the harness first compiles a
pinned-continuation request and requires the adapter to reject it before any
provider invocation. For the remaining profiles, a completed response must
carry a non-empty continuation handle pinned to the selected endpoint and
model.

## Evidence and cost handling

A successful live test emits only the following release-evidence fields:

- profile and fixed tenant;
- provider request and response IDs;
- actual normalized service class;
- whether actual spend was reported, its microUSD amount when known, and the
  provider cost method;
- whether a pinned continuation was verified.

It never logs the prompt, model output, raw endpoint URL, credential, raw
provider payload, or raw SDK error. The test uses `provider.NopObserver{}` so
it does not add request tracing that could capture these values.

Responses must report positive input and output token usage, valid actual
`standard` service class, request and response IDs, and a completed status.
When a provider reports cost, it must be USD, name its method, and be at or
below the fixed ceiling. When cost is not reported, the evidence records
`cost_method=not_reported`; the suite does not infer a number from a pricing
catalog. OpenRouter and Exa are allowed to omit a cost status only when the
reported actual cost fields themselves are valid. A live failure never updates
capabilities, price catalogs, limits, or fixtures.

## Release workflow handoff

Task 24 owns workflow implementation. Its protected `workflow_dispatch` path
should select exactly one profile, expose that profile's 25,000-microUSD
ceiling in the workflow summary, run the scoped test, and retain its redacted
test log as release evidence. It must not add this suite to PR or scheduled
workflows, and it must not turn a test failure into an automatic retry,
catalog update, or price change.
