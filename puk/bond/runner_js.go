//go:build js

package bond

import (
	"context"
	"errors"
)

type Runner struct{}

func NewRunner(string) (*Runner, error) {
	return nil, errors.New("offline editor cannot start local processes")
}
func (*Runner) Start(context.Context, []byte) error {
	return errors.New("offline editor cannot start local processes")
}
func (*Runner) Stop() error   { return nil }
func (*Runner) Running() bool { return false }

func (*Runner) Close() error { return nil }
