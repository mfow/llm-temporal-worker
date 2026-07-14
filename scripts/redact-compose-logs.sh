#!/bin/sh
set -eu

: "${LLMTW_LOG_REDACT_REDIS_PASSWORD:?LLMTW_LOG_REDACT_REDIS_PASSWORD is required}"
: "${LLMTW_LOG_REDACT_POSTGRES_PASSWORD:?LLMTW_LOG_REDACT_POSTGRES_PASSWORD is required}"
: "${LLMTW_LOG_REDACT_MOCK_API_KEY:?LLMTW_LOG_REDACT_MOCK_API_KEY is required}"
: "${LLMTW_LOG_REDACT_CONTINUATION_HMAC:?LLMTW_LOG_REDACT_CONTINUATION_HMAC is required}"

# Read the secrets from the environment instead of awk -v arguments so custom
# passwords are matched literally and never exposed in the redactor command line.
exec awk '
BEGIN {
	secrets[1] = ENVIRON["LLMTW_LOG_REDACT_REDIS_PASSWORD"]
	secrets[2] = ENVIRON["LLMTW_LOG_REDACT_POSTGRES_PASSWORD"]
	secrets[3] = ENVIRON["LLMTW_LOG_REDACT_MOCK_API_KEY"]
	secrets[4] = ENVIRON["LLMTW_LOG_REDACT_CONTINUATION_HMAC"]
}
{
	for (i = 1; i <= 4; i++) {
		secret = secrets[i]
		while (length(secret) && index($0, secret)) {
			position = index($0, secret)
			$0 = substr($0, 1, position - 1) "[REDACTED]" substr($0, position + length(secret))
		}
	}
	print
}
'
