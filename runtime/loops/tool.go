package loops

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/internal/imagecheck"
)

func normalizeToolOutput(call models.ToolCall, out tools.Output) (models.Content, error) {
	content := models.ToolResultContent(call.ID, call.Name, out.IsError, out.Content...)
	if err := content.Validate(); err != nil {
		return models.Content{}, err
	}
	return content, nil
}

func cloneToolOutput(in *tools.Output) *tools.Output {
	if in == nil {
		return nil
	}
	out := *in
	if in.Content != nil {
		out.Content = make([]models.Content, len(in.Content))
		for i := range in.Content {
			out.Content[i] = in.Content[i].Clone()
		}
	}
	return &out
}

type toolImageDecision struct {
	downgrade bool
	reason    string
	info      imagecheck.Info
}

func (r *Runner) adaptToolOutput(previous tools.Output) (tools.Output, error) {
	if r == nil || r.tools == nil || !r.toolResultPolicyKnown {
		return previous, nil
	}
	decisions := make([]toolImageDecision, len(previous.Content))
	imageCount := 0
	downgraded := 0
	for i := range previous.Content {
		content := previous.Content[i]
		if content.Kind != models.ContentImage || content.Image == nil {
			continue
		}
		imageCount++
		info, err := imagecheck.Inspect(content.Image.MIMEType, content.Image.Data, imagecheck.Limits{
			MaxBytes: 7 << 20, MaxWidth: 8192, MaxHeight: 8192, MaxPixels: 40_000_000,
		})
		if err != nil {
			return tools.Output{}, fmt.Errorf("sealed tool image failed validation: %w", err)
		}
		reason := r.toolImageDowngradeReason(content.Image, info, imageCount)
		if reason != "" {
			decisions[i] = toolImageDecision{downgrade: true, reason: reason, info: info}
			downgraded++
		}
	}
	if downgraded == 0 {
		return previous, nil
	}

	replacement := tools.Output{Content: make([]models.Content, len(previous.Content)), IsError: true}
	for i := range previous.Content {
		if !decisions[i].downgrade {
			replacement.Content[i] = previous.Content[i].Clone()
			continue
		}
		image := previous.Content[i].Image
		digest := sha256.Sum256(image.Data)
		replacement.Content[i] = models.Text(fmt.Sprintf(
			"[serpe-tool-image-downgraded:v1 mime=%s detail=%s width=%d height=%d bytes=%d sha256=%s reason=%s]",
			image.MIMEType, image.Detail, decisions[i].info.Width, decisions[i].info.Height,
			len(image.Data), hex.EncodeToString(digest[:]), decisions[i].reason,
		))
	}
	adapted, err := r.tools.ReFinalize(previous, replacement)
	if err == nil {
		return adapted, nil
	}
	if !errors.Is(err, tools.ErrOutputLimit) {
		return tools.Output{}, err
	}

	fallback := tools.Output{Content: make([]models.Content, len(previous.Content)), IsError: true}
	manifest := newToolImageManifest()
	for i := range previous.Content {
		if !decisions[i].downgrade {
			fallback.Content[i] = previous.Content[i].Clone()
			continue
		}
		image := previous.Content[i].Image
		manifest.add(image.MIMEType, image.Detail, image.Data)
		fallback.Content[i] = models.Text("")
	}
	for i := range fallback.Content {
		if decisions[i].downgrade {
			fallback.Content[i] = models.Text(fmt.Sprintf(
				"[serpe-tool-images-downgraded:v1 count=%d sha256=%s reason=provider_policy]",
				downgraded, manifest.sum(),
			))
			break
		}
	}
	adapted, err = r.tools.ReFinalize(previous, fallback)
	if err != nil {
		return tools.Output{}, fmt.Errorf("compact image downgrade failed: %w", err)
	}
	return adapted, nil
}

func (r *Runner) toolImageDowngradeReason(image *models.ImageContent, info imagecheck.Info, ordinal int) string {
	policy := r.toolResultPolicy
	if !policy.InlineImages {
		return "inline_images_disabled"
	}
	if ordinal > policy.MaxImages {
		return "image_count_exceeded"
	}
	if !containsString(policy.MIMETypes, image.MIMEType) {
		return "mime_not_supported"
	}
	if image.Detail != "" && !containsImageDetail(policy.ImageDetails, image.Detail) {
		return "detail_not_supported"
	}
	if int64(len(image.Data)) > policy.MaxRawImageBytes {
		return "image_bytes_exceeded"
	}
	if policy.MaxWidth > 0 && info.Width > policy.MaxWidth {
		return "image_width_exceeded"
	}
	if policy.MaxHeight > 0 && info.Height > policy.MaxHeight {
		return "image_height_exceeded"
	}
	pixels := int64(info.Width) * int64(info.Height)
	if policy.MaxPixels > 0 && pixels > policy.MaxPixels {
		return "image_pixels_exceeded"
	}
	return ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsImageDetail(values []models.ImageDetail, want models.ImageDetail) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type toolImageManifest struct{ hash hash.Hash }

func newToolImageManifest() *toolImageManifest {
	h := sha256.New()
	_, _ = h.Write([]byte("serpe.runtime.tool-image-downgrade.v1"))
	return &toolImageManifest{hash: h}
}

func (m *toolImageManifest) add(mime string, detail models.ImageDetail, data []byte) {
	m.write([]byte(mime))
	m.write([]byte(detail))
	m.write(data)
}

func (m *toolImageManifest) write(value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = m.hash.Write(size[:])
	_, _ = m.hash.Write(value)
}

func (m *toolImageManifest) sum() string { return hex.EncodeToString(m.hash.Sum(nil)) }
