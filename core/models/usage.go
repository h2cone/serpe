package models

import "encoding/json"

// Usage is normalized token accounting. Unset values mean the provider did
// not report the metric; they are distinct from a reported zero.
type Usage struct {
	InputTokens       Optional[int64]
	OutputTokens      Optional[int64]
	CachedInputTokens Optional[int64]
	ReasoningTokens   Optional[int64]
	ToolUseTokens     Optional[int64]
	TotalTokens       Optional[int64]
	Raw               json.RawMessage
}

func (u Usage) clone() Usage {
	u.Raw = append(json.RawMessage(nil), u.Raw...)
	return u
}
