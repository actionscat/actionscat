package matcher

import (
	"fmt"
	"regexp"
	"strings"
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

func NewRegexRule(spec RuleSpec) (*Rule, error) {
	compiled, err := regexp.Compile(spec.Pattern)
	if err != nil {
		return nil, fmt.Errorf("compile rule %s failed: %w", spec.Name, err)
	}

	return &Rule{
		Name:       spec.Name,
		Pattern:    compiled,
		ActionName: spec.ActionName,
		Priority:   spec.Priority,
	}, nil
}

func NewURLRule(name string, actionName string, domains ...string) RuleSpec {
	escapedDomains := make([]string, 0, len(domains))

	for _, domain := range domains {
		escapedDomains = append(escapedDomains, regexp.QuoteMeta(domain))
	}

	domainPattern := strings.Join(escapedDomains, "|")

	return RuleSpec{
		Name:       name,
		ActionName: actionName,
		Pattern:    fmt.Sprintf(`(?i)\b(?:https?://)?(?:www\.)?(?:%s)(?:/[^\s]*)?`, domainPattern),
		Priority:   100,
	}
}

func (r *Rule) Match(text string) bool {
	return r.Pattern.MatchString(text)
}
