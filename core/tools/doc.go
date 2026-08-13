// Package tools is Serpe's generic tool execution subsystem.
//
// It owns registration, argument validation, batch planning, resource
// serialization, and single-call output bounding. It does not own model
// turns, sessions, providers, or the semantics of any particular tool.
//
// An Executor is immutable after construction and safe for concurrent use
// by many goroutines and loops.Runner runs. Definition() is called once
// at registration; later mutations of the returned value are ignored.
//
// Tool.Execute returning Output{IsError:true} is a model-recoverable
// business failure. A non-nil Go error is a fatal infrastructure or
// contract failure and stops the batch. Planner/Activator may use
// Reject/CallError for recoverable pre-execution refusals; the same error
// from Execute/Run is fatal.
//
// Callers that receive a Batch from Start must Close it.
package tools
