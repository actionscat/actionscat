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
	// 相对于项目根目录的 actions 路径
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

		// 构造子目录路径
		luaPath := filepath.Join(actionsPath, entry.Name(), "rule.lua")
		log.Printf("[scanActionsLua] checking %s", luaPath)

		// 查找 rule.lua 文件
		if _, err := os.Stat(luaPath); err != nil {
			log.Printf("[scanActionsLua] rule.lua not found in %s", entry.Name())
			continue
		}

		log.Printf("[scanActionsLua] executing %s", luaPath)
		// 执行 lua 规则
		if MatchLua(luaPath, text) {
			// 规则匹配，关联 ActionName
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
