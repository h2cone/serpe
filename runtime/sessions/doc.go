// Package sessions provides provider-neutral, persistable conversation
// snapshots for the agent runtime. It is independent of providers, model
// invocation, and shell concerns.
//
// Ownership: Manager encodes sessions into owned records and decodes returned
// snapshots, so callers never share slices, maps, JSON, or media bytes with
// stored state. Returned snapshots are independent and safe to modify.
//
// Concurrency: a Manager serializes composite writes per session ID, so
// concurrent updates to the same session never lose messages, while different
// sessions proceed in parallel. AppendAt provides optimistic CAS-append when
// the caller observed a transcript length before a turn. Store implementations
// are responsible for opaque-record atomicity, byte ownership, and lifecycle;
// a successful NewManager call takes exclusive ownership and Manager.Close
// releases the Store.
//
// Store implementations: MemoryStore is in-process. FileStore holds a
// cross-process exclusive maintenance lock, pins a pre-existing private root,
// and atomically publishes one versioned opaque record per session using its
// v2 lowercase filename codec. Legacy layouts require explicit offline
// MaintainFileStore migration. Manager is the single owner of Session
// validation and the record codec. Session IDs use one portable alphabet for
// every Store (see Session). Application-level Get→Run→commit-on-Completed
// wiring lives in package compose, not here.
package sessions
