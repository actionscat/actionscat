package matcher

import (
	"actionscat/internal/domain"
	"actionscat/internal/runner"
	"actionscat/internal/store"
	"context"
	"fmt"
	"log"
	"maps"
	"regexp"
	"strings"
	"sync"
)

type Engine struct {
	store      *store.SQLiteStore
	runner     *runner.Runner
	regexCache sync.Map // map[string]*regexp.Regexp
}

func NewEngine(store *store.SQLiteStore, runner *runner.Runner) *Engine {
	return &Engine{
		store:  store,
		runner: runner,
	}
}

func (e *Engine) getOrCompileRegex(pattern string) (*regexp.Regexp, error) {
	if val, ok := e.regexCache.Load(pattern); ok {
		return val.(*regexp.Regexp), nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}

	e.regexCache.Store(pattern, re)
	return re, nil
}

// MatchAndDispatch deterministically evaluates incoming MessageEvents against enabled Matchers
// ordered by priority (descending). Upon matching, it produces Runs in the database and notifies
// the Runner queue. It strictly does NOT execute action code in the dispatch path.
func (e *Engine) MatchAndDispatch(ctx context.Context, event domain.MessageEvent) ([]*domain.Run, error) {
	matchers, err := e.store.ListEnabledMatchers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled matchers: %w", err)
	}

	var dispatchedRuns []*domain.Run

	for _, m := range matchers {
		if ctx.Err() != nil {
			return dispatchedRuns, ctx.Err()
		}

		matched, captures := e.evaluateMatcher(m, event)
		if !matched {
			continue
		}

		// Prepare extra environment variables from event and captures
		extraEnv := make(map[string]string, len(captures)+5)
		maps.Copy(extraEnv, captures)
		extraEnv["ACTIONSCAT_EVENT_TEXT"] = event.Text
		extraEnv["ACTIONSCAT_EVENT_PLATFORM"] = event.Platform
		extraEnv["ACTIONSCAT_EVENT_SESSION_ID"] = event.SessionID
		extraEnv["ACTIONSCAT_EVENT_USER_ID"] = event.UserID
		extraEnv["ACTIONSCAT_EVENT_GROUP_ID"] = event.GroupID

		triggerMetadata := map[string]string{
			"matcher_id":   m.ID,
			"matcher_name": m.Name,
			"platform":     event.Platform,
			"session_id":   event.SessionID,
			"user_id":      event.UserID,
			"group_id":     event.GroupID,
		}

		run, err := e.runner.CreateRun(ctx, runner.CreateRunRequest{
			ActionID:        m.ActionID,
			TriggerType:     domain.TriggerTypeMatcher,
			TriggerMetadata: triggerMetadata,
			ExtraEnv:        extraEnv,
		})
		if err != nil {
			log.Printf("[matcher] could not create run for matched action %s (matcher %s): %v", m.ActionID, m.ID, err)
			continue
		}

		dispatchedRuns = append(dispatchedRuns, run)

		// Short-circuit: stop on first match unless explicitly configured to continue
		if !m.ContinueMatching {
			break
		}
	}

	return dispatchedRuns, nil
}

func (e *Engine) evaluateMatcher(m *domain.Matcher, event domain.MessageEvent) (bool, map[string]string) {
	targetVal := resolveTargetField(m.TargetField, event)
	captures := make(map[string]string)

	switch m.MatchType {
	case domain.MatchTypeExact:
		return targetVal == m.Pattern, captures

	case domain.MatchTypeContains:
		return strings.Contains(targetVal, m.Pattern), captures

	case domain.MatchTypeRegex:
		re, err := e.getOrCompileRegex(m.Pattern)
		if err != nil {
			log.Printf("[matcher] invalid regex pattern %q in matcher %s: %v", m.Pattern, m.ID, err)
			return false, captures
		}

		matchIndexes := re.FindStringSubmatchIndex(targetVal)
		if matchIndexes == nil {
			return false, captures
		}

		// Extract named capture groups mapped to environment variables
		subexpNames := re.SubexpNames()
		matches := re.FindStringSubmatch(targetVal)
		for i, name := range subexpNames {
			if name == "" || i >= len(matches) {
				continue
			}
			if envVar, exists := m.CaptureEnvMap[name]; exists && envVar != "" {
				captures[envVar] = matches[i]
			}
		}
		return true, captures

	default:
		log.Printf("[matcher] unknown match type %q in matcher %s", m.MatchType, m.ID)
		return false, captures
	}
}

func resolveTargetField(targetField string, event domain.MessageEvent) string {
	switch strings.ToLower(targetField) {
	case "", "text":
		return event.Text
	case "platform":
		return event.Platform
	case "session_id":
		return event.SessionID
	case "user_id":
		return event.UserID
	case "group_id":
		return event.GroupID
	default:
		if event.Metadata != nil {
			if val, ok := event.Metadata[targetField]; ok {
				return val
			}
		}
		return ""
	}
}
