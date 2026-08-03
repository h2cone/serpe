// Package providers binds transport and authentication configuration to one
// provider protocol, then derives immutable models by upstream model ID.
//
// New and Provider.Model perform validation and allocation only; neither sends
// a network request. Providers and their bound models are safe for concurrent
// use and share the configured HTTP client and connection pool.
//
// Supported protocols are OpenAI Chat Completions, OpenAI Responses, Anthropic
// Messages, and Gemini GenerateContent. Each protocol can run on either the
// default built-in HTTP/JSON/SSE Driver or the corresponding vendor official
// Go SDK Driver, selected by Config.Driver. The zero Driver value is
// DriverDefault and preserves existing behavior. There is no automatic
// fallback between Drivers: official SDK construction or call failures are
// returned as normalized models.Error values.
//
// The package performs no automatic retries or model fallback because
// generation POSTs may be billable and are not inherently idempotent. Callers
// can inspect models.Error.Retryable and RetryAfter and make that
// orchestration decision outside this package.
//
// API keys are added only at the HTTP boundary. Config never reads environment
// variables (including when DriverOfficialSDK is selected), and prompts, tool
// arguments, and outputs are not logged. Contexts define total call duration;
// the default shared HTTP client has no whole-call timeout that could truncate
// a long stream. Official SDK default retries and ambient credentials are
// disabled so both Drivers share the same safety contract.
package providers

import "github.com/h2cone/ouro/core/models"

// Provider is an immutable protocol and transport binding. Model validates and
// binds an upstream model ID without network access.
type Provider interface {
	Model(upstreamModelID string) (models.Model, error)
}
