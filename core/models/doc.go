// Package models defines provider-independent requests, responses, streaming
// events, and model invocation interfaces.
//
// Content is a closed tagged union. EncodeContent / DecodeContent (and
// MarshalContent / UnmarshalContent) are the single authority for content-kind
// serialization; persistence and interop layers should call them instead of
// re-enumerating kinds.
//
// A Request and all memory reachable from it belong to the caller until an
// invocation starts. Once Complete or Stream is called, the caller must not
// modify the request or its backing slices until the invocation has returned
// or the stream has been closed. Model implementations are safe for concurrent
// use; an individual Stream has exactly one reader, although Close may run
// concurrently with Next.
//
// Stream is a pull API and therefore provides natural backpressure. It emits a
// response start, ordered part lifecycle events, and one response end. A clean
// transport EOF before the protocol terminal is reported as ErrorProtocol,
// never as a successful empty response. Response remains nil while a terminal
// response_end event is observable and becomes available after Next reports
// exhaustion. Complete and a fully consumed Stream normalize equivalent
// provider results into the same Response shape.
package models
