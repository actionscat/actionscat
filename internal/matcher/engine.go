package matcher

import _ "fmt"

type MatchResult struct {
	ActionName string
	Data       any
}

type SimpleEngine struct {
	rules []*Rule
}

func NewSimpleEngine() *SimpleEngine {
	return &SimpleEngine{
		rules: make([]*Rule, 0),
	}
}

func (e *SimpleEngine) RegisterRule(rule *Rule) error {
	if rule == nil {
		return nil
	}
	e.rules = append(e.rules, rule)
	return nil
}

func (e *SimpleEngine) Match(rawMsg string) (*MatchResult, bool) {
	for _, rule := range e.rules {
		if rule.Match(rawMsg) {
			return &MatchResult{ActionName: rule.ActionName, Data: nil}, true
		}
	}
	return nil, false
}
