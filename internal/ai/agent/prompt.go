package agent

import aicontext "github.com/Hello-CTF/NexTerm/internal/ai/context"

func systemPrompt(locale string) string {
	return aicontext.SystemPrompt(locale)
}

func planSystemPrompt(locale string) string {
	return aicontext.PlanPrompt(locale)
}
