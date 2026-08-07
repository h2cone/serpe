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
// sessions proceed in parallel. AppendAt provides optimistic CAS-append when
// the caller observed a transcript length before a turn. Cross-process
// coordination is out of scope; a Store implementation is responsible for its
// own atomicity (FileStore.Create is exclusive within a process and uses
// non-clobbering publish).
//
// Store implementations: MemoryStore (in-process) and FileStore (one JSON
// snapshot file per session under a root directory, atomic temp+rename
// writes). Session IDs use one portable alphabet for every Store (see Session).
// Application-level Get→Run→commit-on-Completed wiring lives in package
// compose, not here.
package sessions
