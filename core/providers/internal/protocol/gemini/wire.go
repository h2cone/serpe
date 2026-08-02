package gemini

import "encoding/json"

type requestWire struct {
	SystemInstruction *contentWire      `json:"systemInstruction,omitempty"`
	Contents          []contentWire     `json:"contents"`
	Tools             []toolContainer   `json:"tools,omitempty"`
	ToolConfig        *toolConfigWire   `json:"toolConfig,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

type contentWire struct {
	Role  string     `json:"role,omitempty"`
	Parts []partWire `json:"parts"`
}

type partWire struct {
	Text             *string               `json:"text,omitempty"`
	InlineData       *inlineDataWire       `json:"inlineData,omitempty"`
	FileData         *fileDataWire         `json:"fileData,omitempty"`
	FunctionCall     *functionCallWire     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponseWire `json:"functionResponse,omitempty"`
	Thought          bool                  `json:"thought,omitempty"`
	ThoughtSignature string                `json:"thoughtSignature,omitempty"`
}

type inlineDataWire struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type fileDataWire struct {
	MIMEType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}

type functionCallWire struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type functionResponseWire struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type toolContainer struct {
	FunctionDeclarations []functionDeclaration `json:"functionDeclarations"`
}

type functionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type toolConfigWire struct {
	FunctionCallingConfig struct {
		Mode                 string   `json:"mode"`
		AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
	} `json:"functionCallingConfig"`
}

type generationConfig struct {
	MaxOutputTokens  *int            `json:"maxOutputTokens,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"topP,omitempty"`
	StopSequences    []string        `json:"stopSequences,omitempty"`
	Seed             *int64          `json:"seed,omitempty"`
	CandidateCount   *int            `json:"candidateCount,omitempty"`
	ResponseMIMEType string          `json:"responseMimeType,omitempty"`
	ResponseSchema   json.RawMessage `json:"responseSchema,omitempty"`
}

type responseWire struct {
	Candidates     []candidateWire    `json:"candidates"`
	PromptFeedback *promptFeedback    `json:"promptFeedback"`
	UsageMetadata  *usageMetadataWire `json:"usageMetadata"`
	ModelVersion   string             `json:"modelVersion"`
	ResponseID     string             `json:"responseId"`
	Error          *errorWire         `json:"error,omitempty"`
}

type candidateWire struct {
	Index        *int        `json:"index"`
	Content      contentWire `json:"content"`
	FinishReason string      `json:"finishReason"`
}

type promptFeedback struct {
	BlockReason        string `json:"blockReason"`
	BlockReasonMessage string `json:"blockReasonMessage"`
}

type usageMetadataWire struct {
	PromptTokenCount        *int64 `json:"promptTokenCount"`
	CandidatesTokenCount    *int64 `json:"candidatesTokenCount"`
	TotalTokenCount         *int64 `json:"totalTokenCount"`
	CachedContentTokenCount *int64 `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      *int64 `json:"thoughtsTokenCount"`
	ToolUsePromptTokenCount *int64 `json:"toolUsePromptTokenCount"`
}

type errorWire struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}
