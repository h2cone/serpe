package loops

import (
	"context"
	"fmt"
	"reflect"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/runtime/sessions"
)

// Config constructs an immutable Runner.
type Config struct {
	Model  models.Model
	Tools  *tools.Executor
	Limits Limits
}

// Limits bounds a single run. Zero values for MaxModelTurns, MaxToolCalls, and
// MaxIdenticalSteps use safe defaults and cannot disable those bounds.
// MaxObservedTokens zero disables the observed-token limit. Stream carries
// one-turn event envelope limits; MaxToolCalls is the run-lifetime executed
// call counter.
type Limits struct {
	MaxModelTurns     int
	MaxToolCalls      int
	MaxObservedTokens int64
	MaxIdenticalSteps int
	Stream            models.StreamLimits
	sessions.Limits
	MaxRetainedToolBytes  int64
	MaxCanonicalToolBytes int64
	Context               ContextLimits
}

// ContextLimits bound only the request projection sent to a model. They do
// not alter the canonical transcript retained by a run or session.
type ContextLimits struct {
	MaxToolCallArgumentContextBytes int64
	MaxToolExchanges                int
	MaxToolTextContextBytes         int64
	MaxToolImageContextBytes        int64
}

const (
	defaultMaxModelTurns                   = 32
	defaultMaxToolCalls                    = 128
	defaultMaxIdenticalSteps               = 3
	defaultMaxRetainedToolBytes            = int64(64 << 20)
	defaultMaxCanonicalToolBytes           = int64(256 << 20)
	defaultMaxToolCallArgumentContextBytes = int64(16 << 20)
	defaultMaxToolTextContextBytes         = int64(256 << 10)
	defaultMaxToolImageContextBytes        = int64(7 << 20)
	minToolContextTextBytes                = int64(16 << 10)
	minToolBudgetBytes                     = int64(4 << 10)
)

// Runner executes agent runs against a fixed model and tool set.
// After construction it is immutable and safe for concurrent use.
type Runner struct {
	model                  models.Model
	tools                  *tools.Executor
	limits                 Limits
	capabilities           models.CapabilitySet
	rejectParallel         bool
	requestBudget          models.RequestBudgetReporter
	maxEncodedRequestBytes int64
	toolResultPolicy       models.ToolResultPolicy
	adaptImages            bool
	allowToolGroupDeletion bool
}

// New validates config and builds a concurrent-safe Runner.
func New(config Config) (*Runner, error) {
	if isNilDynamic(config.Model) {
		return nil, fmt.Errorf("%w: model is required", ErrInvalidConfig)
	}
	limits, err := normalizeLimits(config.Limits, config.Tools)
	if err != nil {
		return nil, err
	}
	contract, err := inspectModelContract(config.Model, config.Tools)
	if err != nil {
		return nil, err
	}
	return &Runner{
		model:                  config.Model,
		tools:                  config.Tools,
		limits:                 limits,
		capabilities:           contract.capabilities,
		rejectParallel:         contract.rejectParallel,
		requestBudget:          contract.requestBudget,
		maxEncodedRequestBytes: contract.maxEncodedRequestBytes,
		toolResultPolicy:       contract.toolResultPolicy,
		adaptImages:            contract.adaptImages,
		allowToolGroupDeletion: contract.allowToolGroupDeletion,
	}, nil
}

// Limits returns the normalized immutable limits used by the Runner.
func (r *Runner) Limits() Limits {
	if r == nil {
		return Limits{}
	}
	return r.limits
}

// ToolDefinitions returns a defensive snapshot of the tools exposed to the
// model. It is primarily used by composition roots enforcing authorization
// policy before accepting requests.
func (r *Runner) ToolDefinitions() []models.Tool {
	if r == nil || r.tools == nil {
		return nil
	}
	return r.tools.Definitions()
}

type modelContract struct {
	capabilities           models.CapabilitySet
	rejectParallel         bool
	requestBudget          models.RequestBudgetReporter
	maxEncodedRequestBytes int64
	toolResultPolicy       models.ToolResultPolicy
	adaptImages            bool
	allowToolGroupDeletion bool
}

func inspectModelContract(model models.Model, exec *tools.Executor) (modelContract, error) {
	contract := modelContract{allowToolGroupDeletion: true}
	capsKnown := false

	if reporter, ok := model.(models.CapabilityReporter); ok {
		if isNilDynamic(reporter) {
			return modelContract{}, fmt.Errorf("%w: nil CapabilityReporter", ErrInvalidConfig)
		}
		capabilities, err := callCapabilities(reporter)
		if err != nil {
			return modelContract{}, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		contract.capabilities = capabilities
		contract.rejectParallel = !capabilities.Has(models.CapabilityParallelTools)
		capsKnown = true
		if _, ok := model.(models.StreamLimitAcceptor); !ok {
			return modelContract{}, fmt.Errorf("%w: model is missing StreamLimitAcceptor", ErrInvalidConfig)
		}
	}

	if reporter, ok := model.(models.RequestBudgetReporter); ok {
		if isNilDynamic(reporter) {
			return modelContract{}, fmt.Errorf("%w: nil RequestBudgetReporter", ErrInvalidConfig)
		}
		maximum, err := callMaxEncodedRequestBytes(reporter)
		if err != nil {
			return modelContract{}, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		if maximum <= 0 {
			return modelContract{}, fmt.Errorf("%w: MaxEncodedRequestBytes must be positive", ErrInvalidConfig)
		}
		contract.requestBudget = reporter
		contract.maxEncodedRequestBytes = maximum
	} else if capsKnown {
		return modelContract{}, fmt.Errorf("%w: model is missing RequestBudgetReporter", ErrInvalidConfig)
	}

	if reporter, ok := model.(models.ToolHistoryPolicyReporter); ok {
		if isNilDynamic(reporter) {
			return modelContract{}, fmt.Errorf("%w: nil ToolHistoryPolicyReporter", ErrInvalidConfig)
		}
		allowed, err := callAllowsToolHistoryGroupDeletion(reporter)
		if err != nil {
			return modelContract{}, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		contract.allowToolGroupDeletion = allowed
	}

	policyReporter, hasPolicyReporter := model.(models.ToolResultPolicyReporter)
	if hasPolicyReporter {
		if isNilDynamic(policyReporter) {
			return modelContract{}, fmt.Errorf("%w: nil ToolResultPolicyReporter", ErrInvalidConfig)
		}
		policy, reported, err := callToolResultPolicy(policyReporter)
		if err != nil {
			return modelContract{}, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		if !reported {
			if !zeroToolResultPolicy(policy) {
				return modelContract{}, fmt.Errorf("%w: unreported tool-result policy must be empty", ErrInvalidConfig)
			}
		} else {
			policy, err = normalizeToolResultPolicy(policy)
			if err != nil {
				return modelContract{}, err
			}
			if policy.InlineImages && capsKnown && !contract.capabilities.Has(models.CapabilityToolResultImage) {
				return modelContract{}, fmt.Errorf("%w: tool-result image policy exceeds model capabilities", ErrInvalidConfig)
			}
			contract.toolResultPolicy = policy
		}
		contract.adaptImages = true
	} else if capsKnown {
		if contract.capabilities.Has(models.CapabilityToolResultImage) {
			return modelContract{}, fmt.Errorf("%w: model is missing ToolResultPolicyReporter", ErrInvalidConfig)
		}
		contract.adaptImages = true
	}

	var defs []models.Tool
	if exec != nil {
		defs = exec.Definitions()
	}
	if len(defs) == 0 {
		return contract, nil
	}
	if capsKnown && !contract.capabilities.Has(models.CapabilityTools) {
		return modelContract{}, fmt.Errorf("%w: model does not support tools", ErrInvalidConfig)
	}
	if validator, ok := model.(models.ToolDefinitionValidator); ok {
		if isNilDynamic(validator) {
			return modelContract{}, fmt.Errorf("%w: nil ToolDefinitionValidator", ErrInvalidConfig)
		}
		if err := callDefinitionValidator(validator, defs); err != nil {
			return modelContract{}, fmt.Errorf("%w: tool definitions: %v", ErrInvalidConfig, err)
		}
	} else if capsKnown {
		return modelContract{}, fmt.Errorf("%w: model is missing ToolDefinitionValidator", ErrInvalidConfig)
	}
	return contract, nil
}

func callAllowsToolHistoryGroupDeletion(reporter models.ToolHistoryPolicyReporter) (allowed bool, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("AllowsToolHistoryGroupDeletion panic: %v", rec)
		}
	}()
	return reporter.AllowsToolHistoryGroupDeletion(), nil
}

func callCapabilities(reporter models.CapabilityReporter) (capabilities models.CapabilitySet, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("Capabilities panic: %v", rec)
		}
	}()
	return reporter.Capabilities(), nil
}

func callMaxEncodedRequestBytes(reporter models.RequestBudgetReporter) (maximum int64, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("MaxEncodedRequestBytes panic: %v", rec)
		}
	}()
	return reporter.MaxEncodedRequestBytes(), nil
}

func callToolResultPolicy(reporter models.ToolResultPolicyReporter) (policy models.ToolResultPolicy, reported bool, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("ToolResultPolicy panic: %v", rec)
		}
	}()
	policy, reported = reporter.ToolResultPolicy()
	return policy, reported, nil
}

// prepareModelRequest projects canonical history and, when the model exposes
// an encoded-size reporter, performs the bounded deterministic contraction
// sequence before any network call or new tool side effect.
func (r *Runner) prepareModelRequest(ctx context.Context, conversation *conversation, choice models.ToolChoice, additional ...models.Message) (*models.Request, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	plan := projectionPlan{
		allowGroupDeletion: r.allowToolGroupDeletion,
		detailedSummary:    true,
	}
	req, initial, err := conversation.requestPlanned(ctx, choice, plan, additional...)
	if err != nil {
		return nil, 0, err
	}
	if r.requestBudget == nil {
		return req, 0, nil
	}
	measure := func(candidate *models.Request) (int64, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return r.encodedRequestSizeUpperBound(ctx, candidate)
	}
	size, err := measure(req)
	if err != nil {
		return nil, 0, err
	}
	if size <= r.maxEncodedRequestBytes {
		return req, size, nil
	}
	lastSize := size
	plans := contractionPlans(plan, initial, r.allowToolGroupDeletion)
	for i, candidate := range plans {
		req, _, err = conversation.requestPlanned(ctx, choice, candidate, additional...)
		if err != nil {
			return nil, 0, err
		}
		lastSize, err = measure(req)
		if err != nil {
			return nil, 0, err
		}
		if lastSize <= r.maxEncodedRequestBytes {
			return req, lastSize, nil
		}
		if i+1 >= 19 {
			break
		}
	}
	return nil, 0, fmt.Errorf("%w: encoded model request upper bound %d exceeds %d bytes after %d deterministic projections",
		ErrRunLimit, lastSize, r.maxEncodedRequestBytes, 1+len(plans))
}

func contractionPlans(base projectionPlan, initial projectionInfo, allowDelete bool) []projectionPlan {
	var plans []projectionPlan
	if initial.retainedGroups > 1 || initial.droppedGroups > 0 {
		next := base
		next.detailedSummary = false
		next.omitOlderResults = true
		plans = append(plans, next)
		base = next
	}
	maxDeletes := initial.retainedGroups - 1
	if maxDeletes > 15 {
		maxDeletes = 15
	}
	if !allowDelete {
		maxDeletes = 0
	}
	for deleted := 1; deleted <= maxDeletes; deleted++ {
		next := base
		next.dropOldestRetained = deleted
		plans = append(plans, next)
		base = next
	}
	if initial.retainedGroups > 0 {
		next := base
		next.omitLatestResults = true
		plans = append(plans, next)
	}
	return plans
}

func (r *Runner) encodedRequestSizeUpperBound(ctx context.Context, req *models.Request) (int64, error) {
	if r == nil || r.requestBudget == nil {
		return 0, nil
	}
	size, err := callEncodedRequestSizeUpperBound(r.requestBudget, ctx, req, true)
	if err != nil {
		return 0, fmt.Errorf("%w: model request sizing failed: %w", ErrRunLimit, err)
	}
	if size < 0 {
		return 0, fmt.Errorf("%w: request sizer returned a negative size", ErrInvalidModelResponse)
	}
	return size, nil
}

func callEncodedRequestSizeUpperBound(reporter models.RequestBudgetReporter, ctx context.Context, req *models.Request, stream bool) (size int64, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("EncodedRequestSizeUpperBound panic: %v", rec)
		}
	}()
	return reporter.EncodedRequestSizeUpperBound(ctx, req.Clone(), stream)
}

func normalizeToolResultPolicy(policy models.ToolResultPolicy) (models.ToolResultPolicy, error) {
	if !policy.InlineImages {
		if !zeroToolResultPolicy(policy) {
			return models.ToolResultPolicy{}, fmt.Errorf("%w: disabled tool-result image policy must have no limits", ErrInvalidConfig)
		}
		return models.ToolResultPolicy{}, nil
	}
	if len(policy.MIMETypes) < 1 || len(policy.MIMETypes) > 16 {
		return models.ToolResultPolicy{}, fmt.Errorf("%w: tool-result MIME policy must contain 1 to 16 entries", ErrInvalidConfig)
	}
	allowedMIME := map[string]struct{}{
		"image/gif": {}, "image/jpeg": {}, "image/png": {}, "image/webp": {},
	}
	mimes := make([]string, len(policy.MIMETypes))
	seenMIME := make(map[string]struct{}, len(policy.MIMETypes))
	for i, mime := range policy.MIMETypes {
		if len(mime) == 0 || len(mime) > 127 {
			return models.ToolResultPolicy{}, fmt.Errorf("%w: invalid tool-result MIME length", ErrInvalidConfig)
		}
		if _, ok := allowedMIME[mime]; !ok {
			return models.ToolResultPolicy{}, fmt.Errorf("%w: unsupported tool-result MIME %q", ErrInvalidConfig, mime)
		}
		if _, duplicate := seenMIME[mime]; duplicate {
			return models.ToolResultPolicy{}, fmt.Errorf("%w: duplicate tool-result MIME %q", ErrInvalidConfig, mime)
		}
		seenMIME[mime] = struct{}{}
		mimes[i] = mime
	}
	if len(policy.ImageDetails) > 3 {
		return models.ToolResultPolicy{}, fmt.Errorf("%w: too many tool-result image details", ErrInvalidConfig)
	}
	details := make([]models.ImageDetail, len(policy.ImageDetails))
	seenDetail := make(map[models.ImageDetail]struct{}, len(policy.ImageDetails))
	for i, detail := range policy.ImageDetails {
		if detail != models.ImageDetailAuto && detail != models.ImageDetailLow && detail != models.ImageDetailHigh {
			return models.ToolResultPolicy{}, fmt.Errorf("%w: invalid tool-result image detail %q", ErrInvalidConfig, detail)
		}
		if _, duplicate := seenDetail[detail]; duplicate {
			return models.ToolResultPolicy{}, fmt.Errorf("%w: duplicate tool-result image detail %q", ErrInvalidConfig, detail)
		}
		seenDetail[detail] = struct{}{}
		details[i] = detail
	}
	if policy.MaxRawImageBytes < 1 || policy.MaxRawImageBytes > 7<<20 {
		return models.ToolResultPolicy{}, fmt.Errorf("%w: MaxRawImageBytes must be between 1 and %d", ErrInvalidConfig, 7<<20)
	}
	if policy.MaxImages < 1 || policy.MaxImages > 64 {
		return models.ToolResultPolicy{}, fmt.Errorf("%w: MaxImages must be between 1 and 64", ErrInvalidConfig)
	}
	if policy.MaxWidth < 0 || policy.MaxWidth > 8192 || policy.MaxHeight < 0 || policy.MaxHeight > 8192 || policy.MaxPixels < 0 || policy.MaxPixels > 40_000_000 {
		return models.ToolResultPolicy{}, fmt.Errorf("%w: invalid tool-result image dimensions", ErrInvalidConfig)
	}
	policy.MIMETypes = mimes
	policy.ImageDetails = details
	return policy, nil
}

func zeroToolResultPolicy(policy models.ToolResultPolicy) bool {
	return !policy.InlineImages && len(policy.MIMETypes) == 0 && len(policy.ImageDetails) == 0 &&
		policy.MaxRawImageBytes == 0 && policy.MaxImages == 0 && policy.MaxWidth == 0 &&
		policy.MaxHeight == 0 && policy.MaxPixels == 0
}

func isNilDynamic(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

func callDefinitionValidator(v models.ToolDefinitionValidator, defs []models.Tool) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("ValidateToolDefinitions panic: %v", rec)
		}
	}()
	copied := make([]models.Tool, len(defs))
	for i := range defs {
		copied[i] = defs[i].Clone()
	}
	return v.ValidateToolDefinitions(copied)
}

func normalizeLimits(limits Limits, executor *tools.Executor) (Limits, error) {
	toolCallsConfigured := limits.MaxToolCalls != 0
	if limits.MaxModelTurns < 0 || limits.MaxToolCalls < 0 || limits.MaxObservedTokens < 0 || limits.MaxIdenticalSteps < 0 ||
		limits.MaxRetainedToolBytes < 0 || limits.MaxCanonicalToolBytes < 0 {
		return Limits{}, fmt.Errorf("%w: limits must not be negative", ErrInvalidConfig)
	}
	if limits.MaxModelTurns == 0 {
		limits.MaxModelTurns = defaultMaxModelTurns
	}
	if limits.MaxToolCalls == 0 {
		limits.MaxToolCalls = defaultMaxToolCalls
	}
	if limits.MaxIdenticalSteps == 0 {
		limits.MaxIdenticalSteps = defaultMaxIdenticalSteps
	}
	stream, err := models.NormalizeStreamLimits(limits.Stream)
	if err != nil {
		return Limits{}, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	limits.Stream = stream
	sessionLimits, err := normalizeSessionLimits(limits.Limits)
	if err != nil {
		return Limits{}, err
	}
	limits.Limits = sessionLimits
	if limits.MaxRetainedToolBytes, err = normalizeInt64Limit(limits.MaxRetainedToolBytes, defaultMaxRetainedToolBytes, 1, "MaxRetainedToolBytes"); err != nil {
		return Limits{}, err
	}
	if limits.MaxCanonicalToolBytes, err = normalizeInt64Limit(limits.MaxCanonicalToolBytes, defaultMaxCanonicalToolBytes, 1, "MaxCanonicalToolBytes"); err != nil {
		return Limits{}, err
	}
	if limits.MaxCanonicalToolBytes < limits.MaxRetainedToolBytes {
		return Limits{}, fmt.Errorf("%w: MaxCanonicalToolBytes must be at least MaxRetainedToolBytes", ErrInvalidConfig)
	}
	if executor != nil {
		input, _ := executor.Limits()
		if !toolCallsConfigured {
			limits.MaxToolCalls = input.MaxCalls
		}
		if limits.MaxToolCalls > input.MaxCalls {
			return Limits{}, fmt.Errorf("%w: MaxToolCalls exceeds Executor MaxCalls %d", ErrInvalidConfig, input.MaxCalls)
		}
		if limits.MaxRetainedToolBytes < minToolBudgetBytes || limits.MaxCanonicalToolBytes < minToolBudgetBytes {
			return Limits{}, fmt.Errorf("%w: tool byte limits must be at least %d when tools are configured", ErrInvalidConfig, minToolBudgetBytes)
		}
	}
	if limits.Context, err = normalizeContextLimits(limits.Context); err != nil {
		return Limits{}, err
	}
	return limits, nil
}

func normalizeSessionLimits(limits sessions.Limits) (sessions.Limits, error) {
	normalized, err := sessions.NormalizeLimits(limits)
	if err != nil {
		return sessions.Limits{}, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return normalized, nil
}

func (r *Runner) modelStreamLimits(remainingToolCalls int) models.StreamLimits {
	limits := r.limits.Stream
	turnCalls := remainingToolCalls
	if turnCalls < 1 {
		// Zero would normalize to the package ceiling and reopen a 128-call turn.
		turnCalls = 1
	}
	if r.tools != nil {
		input, _ := r.tools.Limits()
		if input.MaxCalls < turnCalls {
			turnCalls = input.MaxCalls
		}
	}
	limits.MaxToolCalls = turnCalls
	return limits
}

func normalizeInt64Limit(value, ceiling, minimum int64, name string) (int64, error) {
	if value == 0 {
		return ceiling, nil
	}
	if value < minimum || value > ceiling {
		return 0, fmt.Errorf("%w: %s must be between %d and %d", ErrInvalidConfig, name, minimum, ceiling)
	}
	return value, nil
}

func normalizeIntLimit(value, ceiling, minimum int, name string) (int, error) {
	if value == 0 {
		return ceiling, nil
	}
	if value < minimum || value > ceiling {
		return 0, fmt.Errorf("%w: %s must be between %d and %d", ErrInvalidConfig, name, minimum, ceiling)
	}
	return value, nil
}

func normalizeContextLimits(limits ContextLimits) (ContextLimits, error) {
	if limits.MaxToolCallArgumentContextBytes < 0 || limits.MaxToolExchanges < 0 || limits.MaxToolTextContextBytes < 0 || limits.MaxToolImageContextBytes < 0 {
		return ContextLimits{}, fmt.Errorf("%w: context limits must not be negative", ErrInvalidConfig)
	}
	var err error
	if limits.MaxToolCallArgumentContextBytes, err = normalizeInt64Limit(limits.MaxToolCallArgumentContextBytes, defaultMaxToolCallArgumentContextBytes, 2, "MaxToolCallArgumentContextBytes"); err != nil {
		return ContextLimits{}, err
	}
	if limits.MaxToolExchanges, err = normalizeIntLimit(limits.MaxToolExchanges, defaultMaxToolExchanges, 1, "MaxToolExchanges"); err != nil {
		return ContextLimits{}, err
	}
	if limits.MaxToolTextContextBytes, err = normalizeInt64Limit(limits.MaxToolTextContextBytes, defaultMaxToolTextContextBytes, minToolContextTextBytes, "MaxToolTextContextBytes"); err != nil {
		return ContextLimits{}, err
	}
	if limits.MaxToolImageContextBytes, err = normalizeInt64Limit(limits.MaxToolImageContextBytes, defaultMaxToolImageContextBytes, 1, "MaxToolImageContextBytes"); err != nil {
		return ContextLimits{}, err
	}
	return limits, nil
}
