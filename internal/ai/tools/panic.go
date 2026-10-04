package tools

import (
	"context"
	"fmt"
)

func Guarded(ctx context.Context, name string, run func() (Output, error)) (output Output, err error) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			output = Output{}
			err = ctxErr
			return
		}
		output = Fail(fmt.Errorf("工具 %s 执行崩溃: %v", name, recovered))
	}()
	return run()
}
