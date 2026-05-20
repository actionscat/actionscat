package matcher

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
)

// Rule 定义一个匹配规则
type Rule struct {
	Name       string
	Pattern    *regexp.Regexp
	ActionName string
	Priority   int
	rules      []string
	LuaPath    string
}

func (r *Rule) Match(text string) bool {
	//try to scan lua rules
	matched, actionName := scanActionsLua(text)
	if matched {
		r.ActionName = actionName
		return true
	}
	return false
}

func scanActionsLua(text string) (bool, string) {
	// relative path
	actionsPath := filepath.Join("..", "..", "actions")
	entries, err := os.ReadDir(actionsPath)
	if err != nil {
		log.Printf("[scanActionsLua] failed to read actions: %v", err)
		return false, ""
	}

	log.Printf("[scanActionsLua] found %d entries", len(entries))

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// construct directory path
		luaPath := filepath.Join(actionsPath, entry.Name(), "rule.lua")
		log.Printf("[scanActionsLua] checking %s", luaPath)

		// find rule.lua
		if _, err := os.Stat(luaPath); err != nil {
			log.Printf("[scanActionsLua] rule.lua not found in %s", entry.Name())
			continue
		}

		log.Printf("[scanActionsLua] executing %s", luaPath)
		if MatchLua(luaPath, text) {
			log.Printf("[matcher] lua rule matched in %s", entry.Name())
			return true, entry.Name()
		}
		log.Printf("[scanActionsLua] %s returned false", entry.Name())
	}

	log.Printf("[scanActionsLua] no rules matched")
	return false, ""
}

type RuleMatched struct {
	IsMatched bool
}

func (r *RuleMatched) Match(_ string) bool {
	return r.IsMatched
}
