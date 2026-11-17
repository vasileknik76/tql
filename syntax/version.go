package syntax

// VersionedFormat supports version selection and detection; empty and auto detect.
// WithVersion must return an independent backend retaining its custom type and methods.
type VersionedFormat interface {
	WithVersion(string) (Format, error)
	Version(*Node) string
}
