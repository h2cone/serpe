package shared

import "github.com/h2cone/serpe/core/models"

// openAIToolImageModels is an intentionally closed catalog. Model aliases are
// added only with a protocol conformance fixture; unknown and dated IDs remain
// default-deny instead of inheriting support from a name prefix.
var openAIToolImageModels = map[string]struct{}{
	"gpt-4.1": {}, "gpt-4.1-mini": {}, "gpt-4.1-nano": {},
	"gpt-4o": {}, "gpt-4o-mini": {},
	"gpt-5": {}, "gpt-5-mini": {}, "gpt-5-nano": {},
}

// googleToolImageModels is a closed GenerateContent catalog verified against
// the Gemini GenerateContent multimodal function-response and model lifecycle
// pages visible on 2026-08-12 (rechecked 2026-08-13). Dated, image-generation,
// live, and unknown aliases are deliberately not inferred from a gemini-3
// prefix.
var googleToolImageModels = map[string]struct{}{
	"gemini-3-flash-preview":             {},
	"gemini-3.1-pro-preview":             {},
	"gemini-3.1-pro-preview-customtools": {},
	"gemini-3.1-flash-lite":              {},
	"gemini-3.5-flash":                   {},
	"gemini-3.5-flash-lite":              {},
	"gemini-3.6-flash":                   {},
}

// ToolResultPolicy returns the conservative per-model image policy shared by
// default and official drivers. Protocol support is necessary but model-ID
// policy is still checked here.
func ToolResultPolicy(provider, modelID string, capabilities models.CapabilitySet) (models.ToolResultPolicy, bool) {
	if !capabilities.Has(models.CapabilityToolResultImage) {
		return models.ToolResultPolicy{}, false
	}
	switch provider {
	case "anthropic":
		return models.ToolResultPolicy{
			InlineImages:     true,
			MIMETypes:        []string{"image/gif", "image/jpeg", "image/png", "image/webp"},
			MaxRawImageBytes: 7 << 20,
			MaxImages:        20,
			MaxWidth:         8000,
			MaxHeight:        8000,
			MaxPixels:        40_000_000,
		}, true
	case "openai":
		if !capabilities.Has(models.CapabilityProviderState) {
			return models.ToolResultPolicy{}, false
		}
		if _, ok := openAIToolImageModels[modelID]; !ok {
			return models.ToolResultPolicy{}, false
		}
		return models.ToolResultPolicy{
			InlineImages:     true,
			MIMETypes:        []string{"image/gif", "image/jpeg", "image/png", "image/webp"},
			ImageDetails:     []models.ImageDetail{models.ImageDetailAuto, models.ImageDetailLow, models.ImageDetailHigh},
			MaxRawImageBytes: 7 << 20,
			MaxImages:        64,
			MaxWidth:         8192,
			MaxHeight:        8192,
			MaxPixels:        40_000_000,
		}, true
	case "google":
		if _, ok := googleToolImageModels[modelID]; !ok {
			return models.ToolResultPolicy{}, false
		}
		return models.ToolResultPolicy{
			InlineImages:     true,
			MIMETypes:        []string{"image/jpeg", "image/png", "image/webp"},
			MaxRawImageBytes: 7 << 20,
			MaxImages:        64,
			MaxWidth:         8192,
			MaxHeight:        8192,
			MaxPixels:        40_000_000,
		}, true
	default:
		return models.ToolResultPolicy{}, false
	}
}
