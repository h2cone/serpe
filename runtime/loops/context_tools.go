package loops

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/imagecheck"
	"github.com/h2cone/serpe/internal/jsonvalue"
)

const defaultMaxToolExchanges = 16

const projectedResultReserve = int64(32)

const projectedFixedOmittedBody = "[serpe-tool-result-omitted:v1]"

// projectToolContext returns a request-only projection of messages. The
// canonical transcript is not modified.
func projectToolContext(messages []models.Message, maxExchanges int) []models.Message {
	limits := ContextLimits{
		MaxToolCallArgumentContextBytes: defaultMaxToolCallArgumentContextBytes,
		MaxToolExchanges:                maxExchanges,
		MaxToolTextContextBytes:         defaultMaxToolTextContextBytes,
		MaxToolImageContextBytes:        defaultMaxToolImageContextBytes,
	}
	if limits.MaxToolExchanges <= 0 {
		limits.MaxToolExchanges = defaultMaxToolExchanges
	}
	out, err := projectToolContextBounded(messages, limits, models.ToolResultPolicy{}, false)
	if err != nil {
		return nil
	}
	return out
}

type projectionPlan struct {
	allowGroupDeletion bool
	detailedSummary    bool
	dropOldestRetained int
	omitOlderResults   bool
	omitLatestResults  bool
}

type projectionInfo struct {
	retainedGroups int
	droppedGroups  int
}

// projectToolContextBounded creates a provider-request snapshot. It never
// mutates canonical messages and never separates a tool call from its result.
func projectToolContextBounded(messages []models.Message, limits ContextLimits, policy models.ToolResultPolicy, policyKnown bool) ([]models.Message, error) {
	out, _, err := projectToolContextPlanned(context.Background(), messages, limits, policy, policyKnown, projectionPlan{
		allowGroupDeletion: true,
		detailedSummary:    true,
	})
	return out, err
}

func projectToolContextPlanned(ctx context.Context, messages []models.Message, limits ContextLimits, policy models.ToolResultPolicy, policyKnown bool, plan projectionPlan) ([]models.Message, projectionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, projectionInfo{}, err
	}
	if err := validateToolHistory(messages); err != nil {
		return nil, projectionInfo{}, err
	}
	groups := toolExchangeSpans(messages)
	keepStart := len(groups)
	var argumentBytes, resultReserve int64
	keptGroups := 0
	for i := len(groups) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return nil, projectionInfo{}, err
		}
		groupArgs, ok := toolArgumentBytes(messages[groups[i].start])
		if !ok {
			return nil, projectionInfo{}, fmt.Errorf("%w: tool argument context size overflow", ErrRunLimit)
		}
		results := int64(len(toolResultsOf(messages[groups[i].start+1])))
		reserve, ok := safeProjectionAdd(resultReserve, results*projectedResultReserve)
		if !ok {
			return nil, projectionInfo{}, fmt.Errorf("%w: tool result context reserve overflow", ErrRunLimit)
		}
		args, ok := safeProjectionAdd(argumentBytes, groupArgs)
		if !ok {
			return nil, projectionInfo{}, fmt.Errorf("%w: tool argument context size overflow", ErrRunLimit)
		}
		fits := keptGroups < limits.MaxToolExchanges &&
			args <= limits.MaxToolCallArgumentContextBytes &&
			reserve <= limits.MaxToolTextContextBytes
		if !fits {
			if keptGroups == 0 {
				return nil, projectionInfo{}, fmt.Errorf("%w: latest tool exchange cannot fit request context", ErrRunLimit)
			}
			break
		}
		keepStart = i
		keptGroups++
		argumentBytes = args
		resultReserve = reserve
	}
	for dropped := 0; dropped < plan.dropOldestRetained && keepStart < len(groups)-1; dropped++ {
		removedResults := int64(len(toolResultsOf(messages[groups[keepStart].start+1])))
		resultReserve -= removedResults * projectedResultReserve
		keepStart++
		keptGroups--
	}
	if keepStart > 0 && !plan.allowGroupDeletion {
		return nil, projectionInfo{}, fmt.Errorf("%w: model requires continuous tool history", ErrRunLimit)
	}
	maxSummary := int64(16 << 10)
	if half := limits.MaxToolTextContextBytes / 2; half < maxSummary {
		maxSummary = half
	}
	var summary string
	for {
		if keepStart > 0 {
			var summaryErr error
			summary, summaryErr = toolHistorySummary(ctx, messages, groups[:keepStart], maxSummary, plan.detailedSummary)
			if summaryErr != nil {
				return nil, projectionInfo{}, summaryErr
			}
		}
		if resultReserve+int64(len(summary)) <= limits.MaxToolTextContextBytes {
			break
		}
		if keepStart >= len(groups)-1 || !plan.allowGroupDeletion {
			return nil, projectionInfo{}, fmt.Errorf("%w: latest tool results cannot fit request context", ErrRunLimit)
		}
		removedResults := int64(len(toolResultsOf(messages[groups[keepStart].start+1])))
		resultReserve -= removedResults * projectedResultReserve
		keepStart++
		keptGroups--
	}

	dropped := groups[:keepStart]
	dropMessage := make([]bool, len(messages))
	summaryAt := -1
	for _, group := range dropped {
		dropMessage[group.start] = true
		dropMessage[group.start+1] = true
		summaryAt = group.start // groups are chronological: retain the newest slot.
	}
	out := make([]models.Message, 0, len(messages)-2*len(dropped)+1)
	for i := range messages {
		if i == summaryAt {
			out = append(out, models.NewUserMessage(models.Text(summary)))
		}
		if dropMessage[i] {
			continue
		}
		out = append(out, messages[i].Clone())
	}

	textRemaining := limits.MaxToolTextContextBytes - int64(len(summary))
	imageRemaining := limits.MaxToolImageContextBytes
	imageSlots := math.MaxInt
	if policyKnown {
		imageSlots = 0
		if policy.InlineImages {
			ordinaryImages := countOrdinaryImages(out)
			if ordinaryImages > policy.MaxImages {
				return nil, projectionInfo{}, fmt.Errorf("%w: ordinary images exceed model request policy", ErrRunLimit)
			}
			imageSlots = policy.MaxImages - ordinaryImages
			if imageSlots < 0 {
				imageSlots = 0
			}
		}
	}
	refs := projectedToolResults(out)
	prepared := make([][]models.Content, len(refs))
	for index, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, projectionInfo{}, err
		}
		if forceProjectedOmission(plan, ref) {
			continue
		}
		content := &out[ref.message].Content[ref.content]
		children := make([]models.Content, 0, len(content.ToolResult.Content))
		for _, child := range content.ToolResult.Content {
			if child.Kind != models.ContentImage || child.Image == nil {
				children = append(children, child.Clone())
				continue
			}
			reason, info := projectedImageReason(child.Image, policy, policyKnown)
			if reason == "" && int64(len(child.Image.Data)) > imageRemaining {
				reason = "context_image_budget"
			}
			if reason == "" && imageSlots == 0 {
				reason = "context_image_slots"
			}
			if reason != "" {
				children = append(children, models.Text(projectedImageMetadata(child.Image, info, reason)))
				continue
			}
			children = append(children, child.Clone())
			imageRemaining -= int64(len(child.Image.Data))
			if imageSlots != math.MaxInt {
				imageSlots--
			}
		}
		prepared[index] = children
	}
	allowances := make([]int64, len(refs))
	for index := range allowances {
		allowances[index] = -1
	}
	var latest []int
	for index, ref := range refs {
		if ref.groupFromNewest == 0 && !forceProjectedOmission(plan, ref) {
			latest = append(latest, index)
		}
	}
	if len(latest) > 0 {
		olderReserve := int64(len(refs)-len(latest)) * projectedResultReserve
		latestBudget := textRemaining - olderReserve
		if latestBudget < int64(len(latest))*projectedResultReserve {
			return nil, projectionInfo{}, fmt.Errorf("%w: latest tool results cannot receive fair context", ErrRunLimit)
		}
		for index, allowance := range fairProjectedTextAllowances(refs, prepared, latest, latestBudget) {
			allowances[index] = allowance
		}
	}
	remainingResults := int64(len(refs))
	for index, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, projectionInfo{}, err
		}
		content := &out[ref.message].Content[ref.content]
		if forceProjectedOmission(plan, ref) {
			content.ToolResult.Content = []models.Content{models.Text(projectedFixedOmittedBody)}
			remainingResults--
			if int64(len(projectedFixedOmittedBody)) > textRemaining {
				return nil, projectionInfo{}, fmt.Errorf("%w: fixed tool result body exceeds context budget", ErrRunLimit)
			}
			textRemaining -= int64(len(projectedFixedOmittedBody))
			continue
		}
		remainingResults--
		allowance := allowances[index]
		if allowance < 0 {
			reserve := remainingResults * projectedResultReserve
			allowance = textRemaining - reserve
			if allowance < projectedResultReserve {
				allowance = projectedResultReserve
			}
		}
		compacted, used, err := compactProjectedText(prepared[index], allowance)
		if err != nil {
			return nil, projectionInfo{}, err
		}
		if used > textRemaining {
			return nil, projectionInfo{}, fmt.Errorf("%w: projected tool text exceeds context budget", ErrRunLimit)
		}
		textRemaining -= used
		content.ToolResult.Content = compacted
	}
	return out, projectionInfo{retainedGroups: len(groups) - keepStart, droppedGroups: keepStart}, nil
}

func forceProjectedOmission(plan projectionPlan, ref projectedResultRef) bool {
	return plan.omitLatestResults && ref.groupFromNewest == 0 ||
		plan.omitOlderResults && ref.groupFromNewest > 0
}

func fairProjectedTextAllowances(refs []projectedResultRef, children [][]models.Content, indexes []int, budget int64) map[int]int64 {
	allowances := make(map[int]int64, len(indexes))
	active := append([]int(nil), indexes...)
	sort.SliceStable(active, func(left, right int) bool {
		return refs[active[left]].callIndex < refs[active[right]].callIndex
	})
	sizes := make(map[int]int64, len(active))
	for _, index := range active {
		for _, child := range children[index] {
			if child.Kind == models.ContentText && child.Text != nil {
				sizes[index] += int64(len(child.Text.Text))
			}
		}
	}
	remaining := budget
	for len(active) > 0 {
		share := remaining / int64(len(active))
		if share < projectedResultReserve {
			share = projectedResultReserve
		}
		var next []int
		for _, index := range active {
			if sizes[index] <= share {
				allowances[index] = sizes[index]
				remaining -= sizes[index]
			} else {
				next = append(next, index)
			}
		}
		if len(next) == len(active) {
			for offset, index := range next {
				allowance := remaining / int64(len(next)-offset)
				if allowance < projectedResultReserve {
					allowance = projectedResultReserve
				}
				allowances[index] = allowance
				remaining -= allowance
			}
			break
		}
		active = next
	}
	return allowances
}

type projectedResultRef struct {
	message, content int
	groupFromNewest  int
	callIndex        int
	isError          bool
}

func projectedToolResults(messages []models.Message) []projectedResultRef {
	groups := toolExchangeSpans(messages)
	refs := make([]projectedResultRef, 0)
	for i := len(groups) - 1; i >= 0; i-- {
		messageIndex := groups[i].start + 1
		callIndexes := make(map[string]int, groups[i].calls)
		for callIndex, call := range toolCallsOf(messages[groups[i].start]) {
			callIndexes[call.ID] = callIndex
		}
		groupRefs := make([]projectedResultRef, 0, groups[i].calls)
		for contentIndex, content := range messages[messageIndex].Content {
			if content.Kind != models.ContentToolResult || content.ToolResult == nil {
				continue
			}
			groupRefs = append(groupRefs, projectedResultRef{
				message: messageIndex, content: contentIndex,
				groupFromNewest: len(groups) - 1 - i,
				callIndex:       callIndexes[content.ToolResult.CallID],
				isError:         content.ToolResult.IsError,
			})
		}
		sort.SliceStable(groupRefs, func(left, right int) bool {
			if groupRefs[left].isError != groupRefs[right].isError {
				return groupRefs[left].isError
			}
			return groupRefs[left].callIndex < groupRefs[right].callIndex
		})
		refs = append(refs, groupRefs...)
	}
	return refs
}

func validateToolHistory(messages []models.Message) error {
	for i := range messages {
		calls := toolCallsOf(messages[i])
		results := toolResultsOf(messages[i])
		if len(calls) > 0 {
			if i+1 >= len(messages) || !matchingExchange(calls, toolResultsOf(messages[i+1])) {
				return fmt.Errorf("%w: incomplete tool exchange at message %d", ErrInvalidModelResponse, i)
			}
		}
		if len(results) > 0 {
			if i == 0 || !matchingExchange(toolCallsOf(messages[i-1]), results) {
				return fmt.Errorf("%w: orphan tool results at message %d", ErrInvalidModelResponse, i)
			}
		}
	}
	return nil
}

func toolArgumentBytes(message models.Message) (int64, bool) {
	var total int64
	for _, call := range toolCallsOf(message) {
		next, ok := safeProjectionAdd(total, int64(len(call.Arguments)))
		if !ok {
			return 0, false
		}
		total = next
	}
	return total, true
}

func countOrdinaryImages(messages []models.Message) int {
	count := 0
	for _, message := range messages {
		for _, content := range message.Content {
			if content.Kind == models.ContentImage {
				count++
			}
		}
	}
	return count
}

func projectedImageReason(image *models.ImageContent, policy models.ToolResultPolicy, known bool) (string, imagecheck.Info) {
	if !known {
		return "", imagecheck.Info{}
	}
	if !policy.InlineImages {
		return "inline_images_disabled", imagecheck.Info{}
	}
	if image.URI != "" {
		return "inline_image_required", imagecheck.Info{}
	}
	if !containsString(policy.MIMETypes, image.MIMEType) {
		return "mime_not_supported", imagecheck.Info{}
	}
	if image.Detail != "" && !containsImageDetail(policy.ImageDetails, image.Detail) {
		return "detail_not_supported", imagecheck.Info{}
	}
	if int64(len(image.Data)) > policy.MaxRawImageBytes {
		return "image_bytes_exceeded", imagecheck.Info{}
	}
	info, err := imagecheck.Inspect(image.MIMEType, image.Data, imagecheck.Limits{
		MaxBytes: 7 << 20, MaxWidth: 8192, MaxHeight: 8192, MaxPixels: 40_000_000,
	})
	if err != nil {
		return "invalid_image", imagecheck.Info{}
	}
	if policy.MaxWidth > 0 && info.Width > policy.MaxWidth {
		return "image_width_exceeded", info
	}
	if policy.MaxHeight > 0 && info.Height > policy.MaxHeight {
		return "image_height_exceeded", info
	}
	if policy.MaxPixels > 0 && int64(info.Width)*int64(info.Height) > policy.MaxPixels {
		return "image_pixels_exceeded", info
	}
	return "", info
}

func projectedImageMetadata(image *models.ImageContent, info imagecheck.Info, reason string) string {
	digest := sha256.Sum256(image.Data)
	return fmt.Sprintf("[serpe-tool-image-omitted:v1 width=%d height=%d bytes=%d sha256=%s reason=%s]",
		info.Width, info.Height, len(image.Data), hex.EncodeToString(digest[:]), reason)
}

func compactProjectedText(children []models.Content, allowance int64) ([]models.Content, int64, error) {
	var total int64
	firstText := -1
	h := sha256.New()
	for i, child := range children {
		if child.Kind != models.ContentText || child.Text == nil {
			continue
		}
		if firstText < 0 {
			firstText = i
		}
		next, ok := safeProjectionAdd(total, int64(len(child.Text.Text)))
		if !ok {
			return nil, 0, fmt.Errorf("%w: projected tool text size overflow", ErrRunLimit)
		}
		total = next
		_, _ = h.Write([]byte(child.Text.Text))
	}
	if firstText < 0 {
		return children, 0, nil
	}
	if total <= allowance {
		return children, total, nil
	}
	short := projectedFixedOmittedBody
	if allowance < int64(len(short)) {
		return nil, 0, fmt.Errorf("%w: tool result omitted marker cannot fit context", ErrRunLimit)
	}
	marker := fmt.Sprintf("[serpe-tool-result-omitted:v1 bytes=%d sha256=%s]", total, hex.EncodeToString(h.Sum(nil)))
	preview := short
	if allowance >= int64(len(marker)) {
		payload := allowance - int64(len(marker))
		head := payload / 2
		tail := payload - head
		preview = projectedTextPrefix(children, head) + marker + projectedTextSuffix(children, tail)
	}
	out := make([]models.Content, len(children))
	for i := range children {
		out[i] = children[i].Clone()
		if out[i].Kind == models.ContentText && out[i].Text != nil {
			out[i].Text.Text = ""
		}
	}
	out[firstText].Text.Text = preview
	return out, int64(len(preview)), nil
}

func projectedTextPrefix(children []models.Content, limit int64) string {
	if limit <= 0 {
		return ""
	}
	buf := make([]byte, 0, limit)
	for _, child := range children {
		if child.Kind != models.ContentText || child.Text == nil || int64(len(buf)) >= limit {
			continue
		}
		remaining := int(limit - int64(len(buf)))
		text := []byte(child.Text.Text)
		if len(text) > remaining {
			text = text[:remaining]
			for len(text) > 0 && !utf8.Valid(text) {
				text = text[:len(text)-1]
			}
		}
		buf = append(buf, text...)
	}
	return string(buf)
}

func projectedTextSuffix(children []models.Content, limit int64) string {
	if limit <= 0 {
		return ""
	}
	buf := make([]byte, 0, limit)
	for i := len(children) - 1; i >= 0 && int64(len(buf)) < limit; i-- {
		child := children[i]
		if child.Kind != models.ContentText || child.Text == nil {
			continue
		}
		remaining := int(limit - int64(len(buf)))
		text := []byte(child.Text.Text)
		if len(text) > remaining {
			text = text[len(text)-remaining:]
			for len(text) > 0 && !utf8.Valid(text) {
				text = text[1:]
			}
		}
		buf = append(text, buf...)
	}
	return string(buf)
}

func safeProjectionAdd(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || right > math.MaxInt64-left {
		return 0, false
	}
	return left + right, true
}

type exchangeSpan struct {
	start, end int
	calls      int
	errors     int
}

func toolExchangeSpans(messages []models.Message) []exchangeSpan {
	var out []exchangeSpan
	for i := 0; i < len(messages); i++ {
		calls := toolCallsOf(messages[i])
		if len(calls) == 0 {
			continue
		}
		if i+1 >= len(messages) || messages[i+1].Role != models.RoleUser {
			continue
		}
		results := toolResultsOf(messages[i+1])
		if !matchingExchange(calls, results) {
			continue
		}
		errs := 0
		for _, r := range results {
			if r.IsError {
				errs++
			}
		}
		out = append(out, exchangeSpan{start: i, end: i + 2, calls: len(calls), errors: errs})
		i++
	}
	return out
}

func toolCallsOf(msg models.Message) []models.ToolCall {
	var out []models.ToolCall
	for _, c := range msg.Content {
		if c.Kind == models.ContentToolCall && c.ToolCall != nil {
			out = append(out, *c.ToolCall)
		}
	}
	return out
}

func toolResultsOf(msg models.Message) []models.ToolResult {
	var out []models.ToolResult
	for _, c := range msg.Content {
		if c.Kind == models.ContentToolResult && c.ToolResult != nil {
			out = append(out, *c.ToolResult)
		}
	}
	return out
}

func matchingExchange(calls []models.ToolCall, results []models.ToolResult) bool {
	if len(calls) == 0 || len(calls) != len(results) {
		return false
	}
	seen := make(map[string]string, len(calls))
	for _, c := range calls {
		seen[c.ID] = c.Name
	}
	for _, r := range results {
		name, ok := seen[r.CallID]
		if !ok || name != r.Name {
			return false
		}
		delete(seen, r.CallID)
	}
	return len(seen) == 0
}

func toolHistorySummary(ctx context.Context, messages []models.Message, dropped []exchangeSpan, maxBytes int64, detailed bool) (string, error) {
	groups, calls, errs := 0, 0, 0
	var rolling [sha256.Size]byte
	for _, group := range dropped {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		groups++
		calls += group.calls
		errs += group.errors
		exchange, err := canonicalExchangeDigest(messages[group.start], messages[group.start+1])
		if err != nil {
			return "", fmt.Errorf("%w: canonical tool history digest: %v", ErrInvalidModelResponse, err)
		}
		h := sha256.New()
		writeDigestString(h, "serpe-tool-history-roll:v1")
		writeDigestBytes(h, rolling[:])
		writeDigestBytes(h, exchange[:])
		copy(rolling[:], h.Sum(nil))
	}
	summary := fmt.Sprintf("[serpe-tool-history-summary:v1] omitted_groups=%d omitted_calls=%d omitted_errors=%d digest=%s",
		groups, calls, errs, hex.EncodeToString(rolling[:]))
	if int64(len(summary)) > maxBytes {
		return "", fmt.Errorf("%w: tool history summary cannot fit context", ErrRunLimit)
	}
	if !detailed {
		return summary, nil
	}
	// Optional detail is newest-first and never includes raw IDs, arguments,
	// result bodies, provider state, or image bytes.
	for groupIndex := len(dropped) - 1; groupIndex >= 0; groupIndex-- {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		group := dropped[groupIndex]
		results := make(map[string]models.ToolResult, group.calls)
		for _, result := range toolResultsOf(messages[group.start+1]) {
			results[result.CallID] = result
		}
		for _, call := range toolCallsOf(messages[group.start]) {
			result, ok := results[call.ID]
			if !ok {
				return "", fmt.Errorf("%w: summary exchange result is missing", ErrInvalidModelResponse)
			}
			callDigest := sha256.Sum256([]byte(call.ID))
			bodyDigest, err := canonicalResultBodyDigest(result)
			if err != nil {
				return "", fmt.Errorf("%w: summary result digest: %v", ErrInvalidModelResponse, err)
			}
			line := fmt.Sprintf("\n tool=%q call_id_sha256=%s body_sha256=%s",
				summaryToolName(call.Name), hex.EncodeToString(callDigest[:]), hex.EncodeToString(bodyDigest[:]))
			if int64(len(summary)+len(line)) > maxBytes {
				return summary, nil
			}
			summary += line
			for _, child := range result.Content {
				if child.Kind != models.ContentImage || child.Image == nil {
					continue
				}
				if err := ctx.Err(); err != nil {
					return "", err
				}
				info := imagecheck.Info{}
				if len(child.Image.Data) > 0 {
					if inspected, inspectErr := imagecheck.Inspect(child.Image.MIMEType, child.Image.Data, imagecheck.Limits{
						MaxBytes: 7 << 20, MaxWidth: 8192, MaxHeight: 8192, MaxPixels: 40_000_000,
					}); inspectErr == nil {
						info = inspected
					}
				}
				imageDigest := sha256.Sum256(child.Image.Data)
				imageLine := fmt.Sprintf("\n  image_mime=%q width=%d height=%d bytes=%d sha256=%s",
					summaryToolName(child.Image.MIMEType), info.Width, info.Height, len(child.Image.Data), hex.EncodeToString(imageDigest[:]))
				if int64(len(summary)+len(imageLine)) > maxBytes {
					return summary, nil
				}
				summary += imageLine
			}
		}
	}
	return summary, nil
}

func canonicalExchangeDigest(assistant, results models.Message) ([sha256.Size]byte, error) {
	h := sha256.New()
	writeDigestString(h, "serpe-tool-exchange:v1")
	if err := writeCanonicalMessageDigest(h, assistant); err != nil {
		return [sha256.Size]byte{}, err
	}
	if err := writeCanonicalMessageDigest(h, results); err != nil {
		return [sha256.Size]byte{}, err
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

func writeCanonicalMessageDigest(h hash.Hash, message models.Message) error {
	if err := message.Validate(); err != nil {
		return err
	}
	writeDigestString(h, string(message.Role))
	writeDigestUint64(h, uint64(len(message.Content)))
	for _, content := range message.Content {
		canonical, err := content.CanonicalBytes()
		if err != nil {
			return err
		}
		writeDigestBytes(h, canonical)
	}
	if message.ProviderState == nil {
		writeDigestUint64(h, 0)
		return nil
	}
	writeDigestUint64(h, 1)
	writeDigestString(h, message.ProviderState.Provider)
	canonical, err := jsonvalue.Canonical(message.ProviderState.Data)
	if err != nil {
		return err
	}
	writeDigestBytes(h, canonical)
	return nil
}

func canonicalResultBodyDigest(result models.ToolResult) ([sha256.Size]byte, error) {
	h := sha256.New()
	writeDigestString(h, "serpe-tool-result-body:v1")
	if result.IsError {
		writeDigestUint64(h, 1)
	} else {
		writeDigestUint64(h, 0)
	}
	writeDigestUint64(h, uint64(len(result.Content)))
	for _, content := range result.Content {
		canonical, err := content.CanonicalBytes()
		if err != nil {
			return [sha256.Size]byte{}, err
		}
		writeDigestBytes(h, canonical)
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

func writeDigestString(h hash.Hash, value string) { writeDigestBytes(h, []byte(value)) }

func writeDigestBytes(h hash.Hash, value []byte) {
	writeDigestUint64(h, uint64(len(value)))
	_, _ = h.Write(value)
}

func writeDigestUint64(h hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = h.Write(encoded[:])
}

func summaryToolName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			b.WriteByte('?')
		} else {
			b.WriteRune(r)
		}
		if b.Len() >= 128 {
			break
		}
	}
	value := b.String()
	for len(value) > 128 || !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
