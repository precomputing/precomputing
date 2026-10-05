package traces

import (
	"regexp"
	"strings"
)

// Pattern is one kind of secret. What matches Re is replaced by the label "[redacted NAME]"; with
// Keep, the first group of the match (such as "AWS_SECRET_ACCESS_KEY=") stays before the label.
type Pattern struct {
	Name string
	Re   string
	Keep bool
	// may is a quick test that every match passes, such as a word it must contain; text that
	// fails it needs no regular expression.
	may func(string) bool
}

// Label is what a secret of this kind becomes.
func (p Pattern) Label() string { return "[redacted " + p.Name + "]" }

// Patterns are the secrets that agents meet in the wild, by the shapes providers give them: cloud
// keys in the environment, tokens in a git remote, API keys in settings files, private keys and
// bearer tokens. Each is masked before anything is stored, so the file never holds one.
//
// The demo page runs the same patterns in JavaScript to check the file (demo/traces/app/run.js
// gets them from the WebAssembly build), so they keep to what both engines read alike: ASCII
// classes, no \s, no (?i). A word that may come in either case is spelled out with ci.
var Patterns = []Pattern{
	{"private-key", `-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`, false, has("PRIVATE KEY-----")},
	{"aws-key-id", `\bAKIA[0-9A-Z]{16}\b`, false, has("AKIA")},
	{"aws-secret", `(` + ci("aws_secret_access_key") + ` *[=:] *(?:\\?["'])?)[A-Za-z0-9/+=]{40}`, true, hasFold("aws_secret_access_key")},
	{"github-token", `\bgh[pousr]_[A-Za-z0-9]{36}\b`, false, hasGitHub},
	{"api-key", `\bsk-(?:proj-)?[A-Za-z0-9_-]{32,}`, false, has("sk-")},
	{"slack-token", `\bxox[abprs]-[A-Za-z0-9-]{10,}`, false, has("xox")},
	{"bearer-token", `(` + ci("authorization") + `: *` + ci("bearer") + ` +)[A-Za-z0-9._~+/=-]{16,}`, true, hasFold("authorization")},
}

// ci spells a lowercase word so that it matches in either case, letter by letter, as [Aa][Ww][Ss].
func ci(word string) string {
	var b strings.Builder
	for _, c := range word {
		if c >= 'a' && c <= 'z' {
			b.WriteString("[" + string(c-32) + string(c) + "]")
		} else {
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

func has(word string) func(string) bool {
	return func(s string) bool { return strings.Contains(s, word) }
}

// hasFold tests for a lowercase ASCII word in any mix of case, as ci matches it.
func hasFold(word string) func(string) bool {
	return func(s string) bool {
		n := len(word)
	next:
		for i := 0; i+n <= len(s); i++ {
			for j := 0; j < n; j++ {
				c, w := s[i+j], word[j]
				if w >= 'a' && w <= 'z' {
					c |= 0x20 // the two cases of an ASCII letter differ in this bit alone
				}
				if c != w {
					continue next
				}
			}
			return true
		}
		return false
	}
}

// hasGitHub tests for ghp_, gho_, ghu_, ghs_ or ghr_.
func hasGitHub(s string) bool {
	for i := 0; ; {
		k := strings.Index(s[i:], "gh")
		if k < 0 {
			return false
		}
		i += k + 2
		if i+1 < len(s) && strings.IndexByte("pousr", s[i]) >= 0 && s[i+1] == '_' {
			return true
		}
	}
}

type secret struct {
	re   *regexp.Regexp
	with string
	may  func(string) bool
}

var secrets = func() []secret {
	out := make([]secret, len(Patterns))
	for i, p := range Patterns {
		out[i] = secret{regexp.MustCompile(p.Re), p.Label(), p.may}
		if p.Keep {
			out[i].with = "${1}" + p.Label()
		}
	}
	return out
}()

// Redact masks the secrets in s and says how many it masked.
func Redact(s string) (string, int) {
	n := 0
	for _, p := range secrets {
		if !p.may(s) {
			continue
		}
		m := p.re.FindAllStringIndex(s, -1)
		if len(m) == 0 {
			continue
		}
		n += len(m)
		s = p.re.ReplaceAllString(s, p.with)
	}
	return s, n
}
