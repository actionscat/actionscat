package matcher

import "log"

func (e *SimpleEngine) Match(rawMsg string) (*MatchResult, bool) {
	log.Printf("[engine] rules count: %d", len(e.rules))
	for i, rule := range e.rules {
		log.Printf("[engine] checking rule %d: %s", i, rule.Name)
		if rule.Match(rawMsg) {
			log.Printf("[engine] matched! action: %s", rule.ActionName)
			return &MatchResult{ActionName: rule.ActionName, Data: rawMsg}, true
		}
	}
	log.Printf("[engine] no match")
	return nil, false
}
