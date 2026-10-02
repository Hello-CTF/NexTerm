package agent

import "context"

type runnerComponent struct {
	runner *Runner
}

func (c runnerComponent) Start(context.Context) error {
	return nil
}

func (c runnerComponent) Shutdown(context.Context) error {
	return c.runner.Close()
}
