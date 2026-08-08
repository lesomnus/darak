// Package run is a thin, injectable wrapper around command execution, so the
// code that shells out can be tested without executing anything.
package run

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Runner executes a command with optional stdin and returns its stdout.
type Runner interface {
	Run(ctx context.Context, stdin, name string, args ...string) (string, error)
}

// Exec is the real Runner.
type Exec struct{}

// Run executes name with args, writing stdin to the child if non-empty.
//
// On failure the error names the command and includes its stderr, but never
// stdin: stdin is where credentials travel precisely so they stay out of places
// that get logged.
func (Exec) Run(ctx context.Context, stdin, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %s: %w: %s",
			name, strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}
