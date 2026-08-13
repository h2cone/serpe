package tools

import (
	"fmt"
	"sort"
	"unicode"
	"unicode/utf8"
)

const rootResource = "serpe.tools.root.v1"

func rootRead() Claim  { return Claim{Resource: rootResource, Access: AccessRead} }
func rootWrite() Claim { return Claim{Resource: rootResource, Access: AccessWrite} }

func normalizeClaims(in []Claim) ([]Claim, error) {
	if len(in) > maxClaimsPerCall {
		return nil, wrapExecution("claim budget exceeded")
	}
	merged := make(map[string]Access, len(in))
	for i, c := range in {
		if err := validateClaim(c); err != nil {
			return nil, fmt.Errorf("claim %d: %w", i, err)
		}
		prev, ok := merged[c.Resource]
		if !ok || c.Access == AccessWrite || prev < c.Access {
			merged[c.Resource] = c.Access
		}
	}
	out := make([]Claim, 0, len(merged))
	for res, acc := range merged {
		out = append(out, Claim{Resource: res, Access: acc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out, nil
}

func validateClaim(c Claim) error {
	if c.Access != AccessRead && c.Access != AccessWrite {
		return wrapExecution("invalid claim access")
	}
	if c.Resource == "" {
		return wrapExecution("resource is empty")
	}
	if !utf8.ValidString(c.Resource) {
		return wrapExecution("resource is not valid UTF-8")
	}
	if len(c.Resource) > maxResourceBytes {
		return wrapExecution("resource exceeds %d bytes", maxResourceBytes)
	}
	for _, r := range c.Resource {
		if unicode.IsControl(r) {
			return wrapExecution("resource contains a control character")
		}
	}
	return nil
}

func claimsConflict(a, b []Claim) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	ib := make(map[string]Access, len(b))
	for _, c := range b {
		ib[c.Resource] = c.Access
	}
	for _, c := range a {
		other, ok := ib[c.Resource]
		if !ok {
			continue
		}
		if c.Access == AccessWrite || other == AccessWrite {
			return true
		}
	}
	return false
}

func extendClaims(base, extra []Claim) ([]Claim, error) {
	// Activation claims may only tighten (add or upgrade), never remove.
	if len(extra) > maxClaimsPerCall || len(base) > maxClaimsPerCall-len(extra) {
		return nil, wrapExecution("claim budget exceeded")
	}
	have := make(map[string]Access, len(base))
	for _, c := range base {
		have[c.Resource] = c.Access
	}
	for _, c := range extra {
		if prev, ok := have[c.Resource]; ok && prev == AccessWrite && c.Access == AccessRead {
			return nil, wrapExecution("activation cannot weaken a static write claim")
		}
	}
	return normalizeClaims(append(append([]Claim(nil), base...), extra...))
}
