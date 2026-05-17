package matcher

import (
	"regexp"
)

type Rule struct {
	Name       string
	Pattern    *regexp.Regexp
	ActionName string
	Priority   int
}

type RuleSpec struct {
	Name       string
	ActionName string
	Pattern    string
	Priority   int
}

func (r *Rule) Match(text string) bool {
	return r.Pattern.MatchString(text)
}
