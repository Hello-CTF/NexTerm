package agent

import aicontext "github.com/ProbiusOfficial/NexTerm/internal/ai/context"

func systemPrompt() string {
	return aicontext.SystemPrompt()
}

func planSystemPrompt() string {
	return aicontext.PlanPrompt()
}
