package providers

// MappingPolicy controls the two documented deterministic compatibility
// conversions. The zero value is strict.
type MappingPolicy string

const (
	MappingStrict  MappingPolicy = "strict"
	MappingLenient MappingPolicy = "lenient"
)

// UnknownEventPolicy controls forward-compatible unknown SSE event handling.
// The zero value rejects unknown events.
type UnknownEventPolicy string

const (
	UnknownEventError  UnknownEventPolicy = "error"
	UnknownEventIgnore UnknownEventPolicy = "ignore"
)

// ContentTypePolicy controls validation of successful HTTP response media
// types. The zero value requires the protocol media type.
type ContentTypePolicy string

const (
	ContentTypeRequire ContentTypePolicy = "require"
	ContentTypeIgnore  ContentTypePolicy = "ignore"
)

// Policy controls protocol mapping and forward-compatibility behavior.
type Policy struct {
	Mapping      MappingPolicy
	UnknownEvent UnknownEventPolicy
	ContentType  ContentTypePolicy
}

func (p Policy) normalized() (Policy, bool) {
	if p.Mapping == "" {
		p.Mapping = MappingStrict
	}
	if p.UnknownEvent == "" {
		p.UnknownEvent = UnknownEventError
	}
	if p.ContentType == "" {
		p.ContentType = ContentTypeRequire
	}
	valid := (p.Mapping == MappingStrict || p.Mapping == MappingLenient) &&
		(p.UnknownEvent == UnknownEventError || p.UnknownEvent == UnknownEventIgnore) &&
		(p.ContentType == ContentTypeRequire || p.ContentType == ContentTypeIgnore)
	return p, valid
}
