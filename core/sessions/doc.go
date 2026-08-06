// Package sessions provides provider-neutral, persistable conversation
// snapshots for the agent runtime. It is independent of providers, model
// invocation, and shell concerns.
//
// Ownership: every public boundary deep-copies mutable data. Callers never
// share slices, maps, JSON, or media bytes with stored state; returned
// snapshots are independent and safe to modify.
//
// Concurrency: a Manager serializes composite writes per session ID, so
// concurrent updates to the same session never lose messages, while different
// sessions proceed in parallel. Cross-process coordination is out of scope;
// a Store implementation is responsible for its own atomicity.
package sessions
