// Package providers binds transport and authentication configuration to one
// provider protocol, then derives immutable models by physical model ID.
//
// New and Provider.Model perform validation and allocation only; neither sends
// a network request. Providers and their bound models are safe for concurrent
// use and share the configured HTTP client and connection pool.
//
// The first release supports OpenAI Chat Completions, OpenAI Responses,
// Anthropic Messages, and Gemini GenerateContent. It performs no automatic
// retries or model fallback because generation POSTs may be billable and are
// not inherently idempotent. Callers can inspect models.Error.Retryable and
// RetryAfter and make that orchestration decision outside this package.
//
// API keys are added only at the HTTP boundary. Config never reads environment
// variables, and prompts, tool arguments, and outputs are not logged. Contexts
// define total call duration; the default shared HTTP client has no whole-call
// timeout that could truncate a long stream.
package providers
