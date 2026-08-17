package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/h2cone/serpe/core/tools"
)

func lexicalClaim(wd, user string, access tools.Access) tools.Claim {
	cleanWD := filepath.Clean(wd)
	clean := filepath.Clean(user)
	if runtime.GOOS == "windows" {
		cleanWD = strings.ToLower(cleanWD)
		clean = strings.ToLower(clean)
	}
	sum := sha256.Sum256([]byte("v1\x00" + filepath.VolumeName(cleanWD) + "\x00" + cleanWD + "\x00" + clean))
	return tools.Claim{Resource: "file:lex:v1:" + hex.EncodeToString(sum[:]), Access: access}
}

func planPath(in tools.Invocation, path string, maxPath int64, access tools.Access) (tools.Plan, error) {
	if err := checkPathString(in.Scope.WorkingDir, maxPath); err != nil || !filepath.IsAbs(in.Scope.WorkingDir) {
		return tools.Plan{}, tools.Reject("working directory must be a valid absolute path")
	}
	if _, err := resolveUserPath(filepath.Clean(in.Scope.WorkingDir), path, maxPath); err != nil {
		return tools.Plan{}, tools.Reject(err.Error())
	}
	return tools.Plan{Claims: []tools.Claim{lexicalClaim(in.Scope.WorkingDir, path, access)}}, nil
}

func (t readTool) Plan(_ context.Context, in tools.Invocation) (tools.Plan, error) {
	args, err := parseObject(in)
	if err != nil {
		return tools.Plan{}, err
	}
	path, ok := objectString(args, "path")
	if !ok {
		return tools.Plan{}, tools.Reject("path is required")
	}
	return planPath(in, path, t.set.lim.MaxPathBytes, tools.AccessRead)
}

func (t writeTool) Plan(_ context.Context, in tools.Invocation) (tools.Plan, error) {
	args, err := parseObject(in)
	if err != nil {
		return tools.Plan{}, err
	}
	path, ok := objectString(args, "path")
	if !ok {
		return tools.Plan{}, tools.Reject("path is required")
	}
	return planPath(in, path, t.set.lim.MaxPathBytes, tools.AccessWrite)
}

func (t editTool) Plan(_ context.Context, in tools.Invocation) (tools.Plan, error) {
	args, err := parseObject(in)
	if err != nil {
		return tools.Plan{}, err
	}
	path, ok := objectString(args, "path")
	if !ok {
		return tools.Plan{}, tools.Reject("path is required")
	}
	return planPath(in, path, t.set.lim.MaxPathBytes, tools.AccessWrite)
}

func executeActivated(ctx context.Context, act tools.Activation) (tools.Output, error) {
	if act.Close != nil {
		defer func() { _ = act.Close() }()
	}
	if act.Run == nil {
		return tools.Output{}, errors.New("activation is missing Run")
	}
	return act.Run(ctx)
}

func (t readTool) Activate(ctx context.Context, in tools.Invocation) (tools.Activation, error) {
	return activateFile(ctx, in, t.set, targetRead, func(runCtx context.Context, target *resolvedTarget) (tools.Output, error) {
		return t.executeResolved(runCtx, in, target)
	})
}

func (t writeTool) Activate(ctx context.Context, in tools.Invocation) (tools.Activation, error) {
	return activateFile(ctx, in, t.set, targetWrite, func(runCtx context.Context, target *resolvedTarget) (tools.Output, error) {
		return t.executeResolved(runCtx, in, target)
	})
}

func (t editTool) Activate(ctx context.Context, in tools.Invocation) (tools.Activation, error) {
	return activateFile(ctx, in, t.set, targetEdit, func(runCtx context.Context, target *resolvedTarget) (tools.Output, error) {
		return t.executeResolved(runCtx, in, target)
	})
}

func activateFile(
	ctx context.Context,
	in tools.Invocation,
	set *Set,
	mode targetMode,
	run func(context.Context, *resolvedTarget) (tools.Output, error),
) (tools.Activation, error) {
	args, err := parseObject(in)
	if err != nil {
		return tools.Activation{}, err
	}
	path, ok := objectString(args, "path")
	if !ok {
		return tools.Activation{}, tools.Reject("path is required")
	}
	target, err := set.resolveTarget(ctx, in, path, mode)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return tools.Activation{}, err
		}
		return tools.Activation{}, tools.Reject(err.Error())
	}
	return tools.Activation{
		Claims: append([]tools.Claim(nil), target.claims...),
		Run: func(runCtx context.Context) (tools.Output, error) {
			return run(runCtx, target)
		},
		Close: target.close,
	}, nil
}
