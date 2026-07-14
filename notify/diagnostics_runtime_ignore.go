package main

import (
	"strings"

	"github.com/gobwas/glob"
)

// diagnosticsNamespaceIgnoreVerdictFromExpanded consumes the exact expanded
// pattern list returned by Syncthing. Syncthing has already resolved includes
// and expanded unrooted patterns; this evaluator pins the same gobwas/glob
// grammar and first-match semantics for the fixed ASCII namespace paths.
func diagnosticsNamespaceIgnoreVerdictFromExpanded(patterns []string) diagnosticsNamespaceIgnoreVerdict {
	verdict := diagnosticsNamespaceIgnoreVerdict{
		Evaluated: true, IncludesSupported: true, Fingerprint: diagnosticsNamespaceIgnoreFingerprint(),
	}
	compiled := make([]diagnosticsExpandedIgnorePattern, 0, len(patterns))
	for _, raw := range patterns {
		pattern, ok := compileDiagnosticsExpandedIgnorePattern(raw)
		if !ok {
			verdict.IncludesSupported = false
			return verdict
		}
		compiled = append(compiled, pattern)
	}
	for _, path := range diagnosticsNamespaceRequiredIgnorePatterns {
		if diagnosticsExpandedPathIgnored(path, compiled) {
			verdict.AnyMatched = true
			return verdict
		}
	}
	return verdict
}

type diagnosticsExpandedIgnorePattern struct {
	matcher  glob.Glob
	ignored  bool
	foldCase bool
}

func compileDiagnosticsExpandedIgnorePattern(raw string) (diagnosticsExpandedIgnorePattern, bool) {
	value := raw
	ignored := true
	foldCase := false
	seen := [3]bool{}
	for {
		switch {
		case strings.HasPrefix(value, "!") && !seen[0]:
			seen[0] = true
			ignored = false
			value = value[1:]
		case strings.HasPrefix(value, "(?i)") && !seen[1]:
			seen[1] = true
			foldCase = true
			value = value[4:]
		case strings.HasPrefix(value, "(?d)") && !seen[2]:
			seen[2] = true
			value = value[4:]
		default:
			goto parsed
		}
	}

parsed:
	if value == "" {
		return diagnosticsExpandedIgnorePattern{}, false
	}
	if strings.HasPrefix(value, "/") {
		value = strings.TrimPrefix(value, "/")
	}
	if foldCase {
		value = strings.ToLower(value)
	}
	matcher, err := glob.Compile(value, '/')
	if err != nil {
		return diagnosticsExpandedIgnorePattern{}, false
	}
	return diagnosticsExpandedIgnorePattern{matcher: matcher, ignored: ignored, foldCase: foldCase}, true
}

func diagnosticsExpandedPathIgnored(path string, patterns []diagnosticsExpandedIgnorePattern) bool {
	for _, pattern := range patterns {
		candidate := path
		if pattern.foldCase {
			candidate = strings.ToLower(candidate)
		}
		if pattern.matcher.Match(candidate) {
			return pattern.ignored
		}
	}
	return false
}
