package models

// Capability is one protocol-adapter feature bit.
type Capability uint64

const (
	CapabilityText Capability = 1 << iota
	CapabilityImageInput
	CapabilityTools
	CapabilityParallelTools
	CapabilityJSONOutput
	CapabilityJSONSchema
	CapabilityReasoningSummary
	CapabilityProviderState
	CapabilityMultipleCandidates
	CapabilityToolResultImage
)

// CapabilitySet is an allocation-free set of adapter capabilities.
type CapabilitySet uint64

// Capabilities creates a set from individual capabilities.
func Capabilities(capabilities ...Capability) CapabilitySet {
	var set CapabilitySet
	for _, capability := range capabilities {
		set |= CapabilitySet(capability)
	}
	return set
}

// Has reports whether all requested capabilities are present.
func (s CapabilitySet) Has(capabilities ...Capability) bool {
	for _, capability := range capabilities {
		if s&CapabilitySet(capability) == 0 {
			return false
		}
	}
	return true
}
