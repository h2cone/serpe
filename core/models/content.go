package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/h2cone/ouro/internal/jsonvalue"
)

// ContentKind identifies a member of the closed Content union.
type ContentKind string

const (
	// ContentText is ordinary visible text.
	ContentText ContentKind = "text"
	// ContentImage is an image supplied by URI or inline bytes.
	ContentImage ContentKind = "image"
	// ContentToolCall is a client function call produced by the model.
	ContentToolCall ContentKind = "tool_call"
	// ContentToolResult is the result of a prior client function call.
	ContentToolResult ContentKind = "tool_result"
	// ContentReasoningSummary is provider-approved displayable reasoning.
	ContentReasoningSummary ContentKind = "reasoning_summary"
	// ContentRefusal is a model refusal.
	ContentRefusal ContentKind = "refusal"
)

// TextContent contains text.
type TextContent struct {
	Text string
}

// ImageDetail is a provider-independent image detail hint.
type ImageDetail string

const (
	// ImageDetailAuto lets the provider choose image detail.
	ImageDetailAuto ImageDetail = "auto"
	// ImageDetailLow requests lower image detail.
	ImageDetailLow ImageDetail = "low"
	// ImageDetailHigh requests higher image detail.
	ImageDetailHigh ImageDetail = "high"
)

// ImageContent contains either URI or MIMEType plus Data, never both.
type ImageContent struct {
	URI      string
	MIMEType string
	Data     []byte
	Detail   ImageDetail
}

// ToolCall is a finalized client function call.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ToolResult is a client function result.
type ToolResult struct {
	CallID  string
	Name    string
	Content []Content
	IsError bool
}

// ReasoningSummaryContent is a displayable reasoning summary, not private
// chain-of-thought.
type ReasoningSummaryContent struct {
	Text string
}

// RefusalContent contains refusal text.
type RefusalContent struct {
	Text string
}

// Content is a closed tagged union. Exactly one variant matching Kind must be
// non-nil.
type Content struct {
	Kind             ContentKind
	Text             *TextContent
	Image            *ImageContent
	ToolCall         *ToolCall
	ToolResult       *ToolResult
	ReasoningSummary *ReasoningSummaryContent
	Refusal          *RefusalContent
}

// Text creates a text content block.
func Text(text string) Content {
	return Content{Kind: ContentText, Text: &TextContent{Text: text}}
}

// ImageURI creates an image content block referring to a URI.
func ImageURI(uri string) Content {
	return Content{Kind: ContentImage, Image: &ImageContent{URI: uri}}
}

// ImageBytes creates an inline image content block and copies data.
func ImageBytes(mimeType string, data []byte) Content {
	return Content{Kind: ContentImage, Image: &ImageContent{MIMEType: mimeType, Data: append([]byte(nil), data...)}}
}

// ToolCallContent creates a finalized tool-call content block and copies the
// JSON arguments.
func ToolCallContent(id, name string, arguments json.RawMessage) Content {
	return Content{Kind: ContentToolCall, ToolCall: &ToolCall{ID: id, Name: name, Arguments: append(json.RawMessage(nil), arguments...)}}
}

// ToolResultContent creates a tool-result content block. name must match the
// function name from the corresponding tool call.
func ToolResultContent(callID, name string, isError bool, content ...Content) Content {
	return Content{Kind: ContentToolResult, ToolResult: &ToolResult{CallID: callID, Name: name, IsError: isError, Content: cloneContents(content)}}
}

// ReasoningSummary creates a displayable reasoning summary block.
func ReasoningSummary(text string) Content {
	return Content{Kind: ContentReasoningSummary, ReasoningSummary: &ReasoningSummaryContent{Text: text}}
}

// Refusal creates a refusal block.
func Refusal(text string) Content {
	return Content{Kind: ContentRefusal, Refusal: &RefusalContent{Text: text}}
}

// Validate checks that exactly one union member is active and validates it.
func (c Content) Validate() error {
	if unionCount(c) != 1 {
		return fmt.Errorf("content: exactly one variant must be set")
	}
	switch c.Kind {
	case ContentText:
		if c.Text == nil {
			return fmt.Errorf("content: text kind does not match variant")
		}
	case ContentImage:
		if c.Image == nil {
			return fmt.Errorf("content: image kind does not match variant")
		}
		if err := validateImage(*c.Image); err != nil {
			return err
		}
	case ContentToolCall:
		if c.ToolCall == nil {
			return fmt.Errorf("content: tool_call kind does not match variant")
		}
		if c.ToolCall.Name == "" {
			return fmt.Errorf("content: tool call name is required")
		}
		if !jsonvalue.IsObject(c.ToolCall.Arguments) {
			return fmt.Errorf("content: tool call arguments must be a JSON object")
		}
	case ContentToolResult:
		if c.ToolResult == nil {
			return fmt.Errorf("content: tool_result kind does not match variant")
		}
		if c.ToolResult.CallID == "" {
			return fmt.Errorf("content: tool result call ID is required")
		}
		if c.ToolResult.Name == "" {
			return fmt.Errorf("content: tool result name is required")
		}
		if len(c.ToolResult.Content) == 0 {
			return fmt.Errorf("content: tool result content is required")
		}
		for i := range c.ToolResult.Content {
			if c.ToolResult.Content[i].Kind != ContentText && c.ToolResult.Content[i].Kind != ContentImage {
				return fmt.Errorf("content: tool result child %d must be text or image", i)
			}
			if err := c.ToolResult.Content[i].Validate(); err != nil {
				return fmt.Errorf("content: tool result child %d: %w", i, err)
			}
		}
	case ContentReasoningSummary:
		if c.ReasoningSummary == nil {
			return fmt.Errorf("content: reasoning_summary kind does not match variant")
		}
	case ContentRefusal:
		if c.Refusal == nil {
			return fmt.Errorf("content: refusal kind does not match variant")
		}
	default:
		return fmt.Errorf("content: unknown kind %q", c.Kind)
	}
	return nil
}

func validateImage(image ImageContent) error {
	inline := image.MIMEType != "" || image.Data != nil
	if (image.URI == "") == !inline {
		return fmt.Errorf("content: image must contain exactly one of URI or inline data")
	}
	if image.URI != "" {
		u, err := url.Parse(image.URI)
		if err != nil || u.Scheme == "" {
			return fmt.Errorf("content: image URI must be absolute")
		}
	}
	if inline && (image.MIMEType == "" || len(image.Data) == 0) {
		return fmt.Errorf("content: inline image requires MIME type and data")
	}
	if image.Detail != "" && image.Detail != ImageDetailAuto && image.Detail != ImageDetailLow && image.Detail != ImageDetailHigh {
		return fmt.Errorf("content: invalid image detail %q", image.Detail)
	}
	return nil
}

func cloneContents(in []Content) []Content {
	if in == nil {
		return nil
	}
	out := make([]Content, len(in))
	for i := range in {
		out[i] = in[i].Clone()
	}
	return out
}

// Clone returns a deep copy safe for the caller to retain and modify.
func (c Content) Clone() Content {
	out := Content{Kind: c.Kind}
	if c.Text != nil {
		v := *c.Text
		out.Text = &v
	}
	if c.Image != nil {
		v := *c.Image
		v.Data = append([]byte(nil), c.Image.Data...)
		out.Image = &v
	}
	if c.ToolCall != nil {
		v := *c.ToolCall
		v.Arguments = append(json.RawMessage(nil), c.ToolCall.Arguments...)
		out.ToolCall = &v
	}
	if c.ToolResult != nil {
		v := *c.ToolResult
		v.Content = cloneContents(c.ToolResult.Content)
		out.ToolResult = &v
	}
	if c.ReasoningSummary != nil {
		v := *c.ReasoningSummary
		out.ReasoningSummary = &v
	}
	if c.Refusal != nil {
		v := *c.Refusal
		out.Refusal = &v
	}
	return out
}

// Equal reports whether two content blocks carry the same normalized meaning.
// Tool arguments are compared as JSON values, ignoring insignificant
// whitespace and object-key order while preserving number lexemes.
func (c Content) Equal(other Content) bool {
	if c.Kind != other.Kind {
		return false
	}
	switch c.Kind {
	case ContentText:
		return c.Text != nil && other.Text != nil && c.Text.Text == other.Text.Text
	case ContentImage:
		return c.Image != nil && other.Image != nil &&
			c.Image.URI == other.Image.URI &&
			c.Image.MIMEType == other.Image.MIMEType &&
			c.Image.Detail == other.Image.Detail &&
			bytes.Equal(c.Image.Data, other.Image.Data)
	case ContentToolCall:
		return c.ToolCall != nil && other.ToolCall != nil &&
			c.ToolCall.ID == other.ToolCall.ID &&
			c.ToolCall.Name == other.ToolCall.Name &&
			jsonvalue.Equal(c.ToolCall.Arguments, other.ToolCall.Arguments)
	case ContentToolResult:
		return c.ToolResult != nil && other.ToolResult != nil &&
			c.ToolResult.CallID == other.ToolResult.CallID &&
			c.ToolResult.Name == other.ToolResult.Name &&
			c.ToolResult.IsError == other.ToolResult.IsError &&
			equalContents(c.ToolResult.Content, other.ToolResult.Content)
	case ContentReasoningSummary:
		return c.ReasoningSummary != nil && other.ReasoningSummary != nil &&
			c.ReasoningSummary.Text == other.ReasoningSummary.Text
	case ContentRefusal:
		return c.Refusal != nil && other.Refusal != nil && c.Refusal.Text == other.Refusal.Text
	default:
		return false
	}
}

func equalContents(left, right []Content) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !left[i].Equal(right[i]) {
			return false
		}
	}
	return true
}
