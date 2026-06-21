package execx

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

type CmdResult struct {
	Output string
}

func Run(ctx context.Context, name string, args ...string) (CmdResult, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return CmdResult{Output: buf.String()}, err
}

