package tools

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

const panicDiagnosticMaxRunes = 200

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
		output = panicOutput(name, recovered)
	}()
	return run()
}

func panicOutput(name string, recovered any) Output {
	return Output{Text: fmt.Sprintf("工具 %s 执行时崩溃：%s", name, safePanicText(recovered)), ExitCode: 1, Panic: true}
}

func safePanicText(recovered any) string {
	text := fmt.Sprintf("%v", recovered)
	text = RedactText(text)
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > panicDiagnosticMaxRunes {
		text = string(runes[:panicDiagnosticMaxRunes]) + "…"
	}
	if text == "" || text == "<nil>" {
		return "未知崩溃"
	}
	return text
}
