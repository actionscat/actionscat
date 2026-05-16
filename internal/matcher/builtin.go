package matcher

var BuiltinRuleSpecs = []RuleSpec{
	NewURLRule("bilibili-url", "bilibili", "bilibili.com", "b23.tv"),
	NewURLRule("ithome-url", "ithome", "ithome.com"),
	NewURLRule("yt-url", "youtube", "youtube.com", "youtu.be"),
	NewURLRule("ncm-url", "netease_music", "music.163.com"),
}

func RegisterBuiltinRules(engine *SimpleEngine) error {
	for _, spec := range BuiltinRuleSpecs {
		rule, err := NewRegexRule(spec)
		if err != nil {
			return err
		}

		if err := engine.RegisterRule(rule); err != nil {
			return err
		}
	}

	return nil
}
