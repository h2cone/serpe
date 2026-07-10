package agent

import "encoding/json"

// defaultTools returns the built-in local toolset.
func defaultTools() []Tool {
	return []Tool{readTool{}, writeTool{}, editTool{}, bashTool{}}
}

func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }
