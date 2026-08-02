package providers

import "fmt"

// Driver selects the concrete transport implementation used by a Provider.
// Protocol and Driver are orthogonal: Protocol chooses endpoint semantics,
// Driver chooses whether the default HTTP/SSE adapters or the official vendor
// SDK perform the request.
type Driver string

const (
	// DriverDefault uses the library's built-in HTTP/JSON/SSE adapters.
	// The empty Driver value is normalized to DriverDefault.
	DriverDefault Driver = "default"
	// DriverOfficialSDK uses the corresponding vendor official Go SDK for the
	// selected Protocol. Construction or call failures are returned as
	// normalized errors; there is no automatic fallback to DriverDefault.
	DriverOfficialSDK Driver = "official_sdk"
)

// normalizeDriver returns the canonical Driver for construction. An empty value
// is treated as DriverDefault for source and runtime compatibility.
func normalizeDriver(driver Driver) (Driver, error) {
	switch driver {
	case "", DriverDefault:
		return DriverDefault, nil
	case DriverOfficialSDK:
		return DriverOfficialSDK, nil
	default:
		return "", fmt.Errorf("providers: unknown driver %q", driver)
	}
}
