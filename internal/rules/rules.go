// Package rules assigns an attention tier to each cached item. Rules are
// evaluated at render time, first match wins, so editing the config never
// requires a refetch.
package rules

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ultus-net/attention-tui/internal/config"
)

// Item is one thing that may need attention: a chat thread or a mail message.
type Item struct {
	Source     string
	ID         string
	ChatID     string
	ChatName   string
	From       string
	Subject    string
	Preview    string
	Importance string
	URL        string
	Received   time.Time
	Focused    bool
	Flagged    bool
	Mention    bool
	Seen       bool
}

// Verdict is the tier an item landed in. Name is the rule that assigned it,
// or "default" when no rule matched.
type Verdict struct {
	Name   string
	Show   bool
	Notify bool
}

type compiledRule struct {
	name    string
	tier    string
	match   config.Match
	from    []*regexp.Regexp
	chat    []*regexp.Regexp
	subject *regexp.Regexp
	hasFrom bool
	hasChat bool
	hasSubj bool
}

// Engine holds compiled rules and the tier table.
type Engine struct {
	rules    []compiledRule
	tiers    map[string]config.Tier
	defaults map[string]string
}

// New compiles every rule and rejects anything the rest of the program cannot
// honour. Errors name the offending rule.
func New(cfg *config.Config) (*Engine, error) {
	e := &Engine{
		tiers:    make(map[string]config.Tier, len(cfg.Tiers)),
		defaults: make(map[string]string, len(cfg.Defaults)),
	}
	for name, tier := range cfg.Tiers {
		e.tiers[name] = tier
	}
	for source, tier := range cfg.Defaults {
		if _, ok := e.tiers[tier]; !ok {
			return nil, fmt.Errorf("rules: default for %q names unknown tier %q", source, tier)
		}
		e.defaults[source] = tier
	}
	e.rules = make([]compiledRule, 0, len(cfg.Rules))
	for i, r := range cfg.Rules {
		cr := compiledRule{name: r.Name, tier: r.Tier, match: r.Match}
		if r.Tier != "" {
			if _, ok := e.tiers[r.Tier]; !ok {
				return nil, fmt.Errorf("rules[%d] %q: unknown tier %q", i, r.Name, r.Tier)
			}
		}
		for _, pattern := range r.Match.From {
			re, err := compileGlob(pattern, i, r.Name)
			if err != nil {
				return nil, err
			}
			cr.from = append(cr.from, re)
		}
		for _, pattern := range r.Match.Chat {
			re, err := compileGlob(pattern, i, r.Name)
			if err != nil {
				return nil, err
			}
			cr.chat = append(cr.chat, re)
		}
		if r.Match.Subject != "" {
			re, err := regexp.Compile("(?i)" + r.Match.Subject)
			if err != nil {
				return nil, fmt.Errorf("rules[%d] %q: bad subject regex: %w", i, r.Name, err)
			}
			cr.subject = re
		}
		cr.hasFrom = len(cr.from) > 0
		cr.hasChat = len(cr.chat) > 0
		cr.hasSubj = cr.subject != nil
		e.rules = append(e.rules, cr)
	}
	return e, nil
}

func compileGlob(pattern string, index int, name string) (*regexp.Regexp, error) {
	re, err := regexp.Compile("(?i)^" + globToRegexp(pattern) + "$")
	if err != nil {
		return nil, fmt.Errorf("rules[%d] %q: bad glob %q: %w", index, name, pattern, err)
	}
	return re, nil
}

func globToRegexp(pattern string) string {
	parts := strings.Split(pattern, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return strings.Join(parts, ".*")
}

// Eval walks the rules in file order and returns the first verdict whose
// predicate matches. Otherwise it returns the default tier for the source.
func (e *Engine) Eval(it Item) Verdict {
	for i := range e.rules {
		r := &e.rules[i]
		if !r.matches(it) {
			continue
		}
		tier := r.tier
		if tier == "" {
			tier = e.defaultTier(it.Source)
		}
		return e.verdict(r.name, tier)
	}
	return e.verdict("default", e.defaultTier(it.Source))
}

func (e *Engine) defaultTier(source string) string {
	if tier, ok := e.defaults[source]; ok {
		return tier
	}
	if _, ok := e.tiers["quiet"]; ok {
		return "quiet"
	}
	return ""
}

func (e *Engine) verdict(name, tier string) Verdict {
	t, ok := e.tiers[tier]
	if !ok {
		return Verdict{Name: name}
	}
	return Verdict{Name: name, Show: t.Show, Notify: t.Notify}
}

func (r *compiledRule) matches(it Item) bool {
	m := r.match
	if m.Source != "" && !strings.EqualFold(m.Source, it.Source) {
		return false
	}
	if r.hasFrom && !matchFrom(r.from, it.From) {
		return false
	}
	if r.hasSubj && !r.subject.MatchString(it.Subject) {
		return false
	}
	if r.hasChat && !matchAny(r.chat, it.ChatName) {
		return false
	}
	if m.Mention != nil && it.Mention != *m.Mention {
		return false
	}
	if m.Focused != nil && it.Focused != *m.Focused {
		return false
	}
	if m.Flagged != nil && it.Flagged != *m.Flagged {
		return false
	}
	if m.Importance != "" && !strings.EqualFold(m.Importance, it.Importance) {
		return false
	}
	return true
}

func matchAny(patterns []*regexp.Regexp, value string) bool {
	for _, re := range patterns {
		if re.MatchString(value) {
			return true
		}
	}
	return false
}

// matchFrom accepts either the raw From header or the bare address inside
// angle brackets, so "cfo@example.com" matches "Ana <cfo@example.com>".
func matchFrom(patterns []*regexp.Regexp, value string) bool {
	if matchAny(patterns, value) {
		return true
	}
	open := strings.LastIndex(value, "<")
	end := strings.LastIndex(value, ">")
	if open < 0 || end <= open {
		return false
	}
	return matchAny(patterns, strings.TrimSpace(value[open+1:end]))
}
