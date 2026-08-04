package providers

import (
	"fmt"
)

// New validates and freezes configuration, selects one concrete Driver and
// protocol adapter, and returns a Provider. It performs no network operation.
// Driver selection is fixed for the lifetime of the returned Provider; bound
// models use that Driver for both Complete and Stream.
func New(config Config) (Provider, error) {
	spec, ok := lookupProtocol(config.Protocol)
	if !ok {
		return nil, fmt.Errorf("providers: missing or unknown protocol %q", config.Protocol)
	}
	internal, err := normalizeConfig(config, spec)
	if err != nil {
		return nil, err
	}
	driver, err := normalizeDriver(config.Driver)
	if err != nil {
		return nil, err
	}
	switch driver {
	case DriverDefault:
		return spec.builtin(internal)
	case DriverOfficialSDK:
		return spec.official(internal)
	default:
		return nil, fmt.Errorf("providers: unknown driver %q", driver)
	}
}
