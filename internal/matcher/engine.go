package matcher

func (e *SimpleEngine) Match(rawMsg string) (*MatchResult, bool) {
	for _, rule := range e.rules {
		if rule.Match(rawMsg) {
			return &MatchResult{ActionName: rule.ActionName, Data: nil}, true
		}
	}
	return nil, false
}
