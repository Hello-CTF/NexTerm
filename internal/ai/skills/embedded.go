package skills

// defaultIndex is empty: NexTerm ships no built-in skills, so every skill is
// one the user writes in Settings.
func defaultIndex() (*Index, error) {
	return &Index{skills: map[string]Skill{}}, nil
}
