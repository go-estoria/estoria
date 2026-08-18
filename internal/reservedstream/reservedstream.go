// Package reservedstream is the registry of stream types the library reserves
// for its own infrastructure. aggregatestore consults it when enforcing the
// reserved namespace, so the library's own aggregates pass validation that
// user aggregates do not.
//
// The registry guards against accidental collision with user aggregate
// types; it is not a security boundary. Callers own their event store and
// can write to any stream in it — consumers of infrastructure streams must
// treat undecodable data there as an error, not an impossibility.
package reservedstream

// ProjectionStreamType is the stream type under which projection lifecycle
// aggregates are stored. projection/lifecycle re-exports it as
// lifecycle.StreamType.
const ProjectionStreamType = "estoria.projection"

// Allowed reports whether a stream type within the reserved namespace is one
// of the library's own.
func Allowed(streamType string) bool {
	return streamType == ProjectionStreamType
}
