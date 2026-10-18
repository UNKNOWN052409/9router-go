// Package systemprompt implements the fork's request pipeline: sanitize the
// client's user/tool messages, drop every system message the client sent, and
// install exactly one system message at index 0.
//
// The reason this is a package rather than a few lines in the chat handler is
// the invariant. A client can send zero system messages, or six, or put one in
// the middle of the conversation; the upstream we forward to is not required
// to agree with the client about any of that. Collapsing them to a single
// leading system message is the only arrangement we can reason about, so it is
// asserted rather than assumed — see Verify.
package systemprompt

import (
	// v1 for RawMessage, which keeps a message's content bytes intact while
	// everything else here follows the repo's v2 convention.
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// DefaultPrompt is the proxy's own instruction. It is deliberately generic:
// the identity of the assistant comes from the provider, not from us.
const DefaultPrompt = "You are a helpful assistant. Answer the user's request directly and accurately."

// Fragments are stripped from user and tool messages before forwarding. They
// are the strings a coding CLI injects into its own turns — instructions
// aimed at whichever agent happens to be reading the transcript, not at the
// user's actual question. Forwarding them lets a client's harness leak into
// the conversation the provider sees.
var Fragments = []string{
	"you will usually use this for PRs",
	"AGENTS.md",
	"CLAUDE.md",
	".cursorrules",
}

// blockRes match a wrapped directive block such as
// <system-reminder>…</system-reminder>, case- and whitespace-insensitively on
// both the open and close tag so <SYSTEM_REMINDER> and </system-reminder>
// pair up correctly.
//
// The spec writes this as one alternation with a backreference
// (<\s*(system[-_]?reminder|guardrails?|safety|policy)[^>]*>…<\/\s*\1\s*>).
// Go's regexp is RE2 and has no backreferences, so the alternation is expanded
// into one pattern per tag name. That is exactly equivalent for a language
// where \1 can only ever match the same alternative that opened the tag —
// which is the only thing this pattern ever needed it for — and it removes a
// class of bug where the closing tag could match a different alternative.
var blockRes = buildBlockRes()

func buildBlockRes() []*regexp.Regexp {
	// Each element mirrors one alternative of the spec's group, including its
	// own optional suffixes (guardrails? matches guardrail and guardrails).
	alternatives := []string{
		`system[-_]?reminder`,
		`guardrails?`,
		`safety`,
		`policy`,
	}
	out := make([]*regexp.Regexp, 0, len(alternatives))
	for _, alt := range alternatives {
		re := regexp.MustCompile(
			`(?is)<\s*` + alt + `\b[^>]*>.*?<\s*/\s*` + alt + `\s*>`)
		out = append(out, re)
	}
	return out
}

// Sanitize strips Fragments and directive blocks from one message's text.
//
// It is length-preserving in structure rather than content: a match is
// removed and surrounding whitespace collapsed, so a message that consisted
// only of stripped material comes back empty instead of as a run of blanks.
func Sanitize(s string) string {
	if s == "" {
		return s
	}
	out := s
	for _, re := range blockRes {
		out = re.ReplaceAllString(out, "")
	}

	// Case-insensitive: the literals are instructions, and a casing
	// variation is the same instruction.
	lower := strings.ToLower(out)
	for _, frag := range Fragments {
		f := strings.ToLower(frag)
		for {
			idx := strings.Index(lower, f)
			if idx < 0 {
				break
			}
			out = out[:idx] + out[idx+len(f):]
			lower = lower[:idx] + lower[idx+len(f):]
		}
	}
	return collapseWhitespace(out)
}

// spaceAfterRemoval matches the blank run a removal leaves behind, but only
// on a single line. Trimming whole lines and collapsing runs across newlines
// would reflow the line structure that tool output and log excerpts carry, and
// RTK compression downstream reads exactly that structure.
var spaceAfterRemoval = regexp.MustCompile(`[ \t]{2,}`)

func collapseWhitespace(s string) string {
	return strings.TrimSpace(spaceAfterRemoval.ReplaceAllString(s, " "))
}

// ErrInvariant marks a body that reached the pipeline's end without exactly
// one leading system message. The caller must refuse to forward rather than
// guess: forwarding a body we cannot account for is how a second system
// message reaches the provider and silently changes the model's behaviour.
var ErrInvariant = errors.New("systemprompt: body must carry exactly one system message at index 0")

// Message is one chat message.
//
// It is a field map rather than a struct because a chat message is not just a
// role and a string: assistant turns carry tool_calls, tool results carry
// tool_call_id, participants carry name, and every provider adds its own. A
// struct with only Role and Content would re-marshal without those fields,
// silently dropping the tool definitions the caller sent — which is exactly
// the class of loss this package exists to prevent.
type Message map[string]jsonv1.RawMessage

// Body is the subset of a chat request this package reads. The full request is
// decoded as a map of raw fields so everything outside messages — tools,
// stream, sampling parameters, provider extensions — passes through byte for
// byte instead of being re-marshalled through a struct that does not know
// those keys exist.
type Body map[string]jsonv1.RawMessage

// role reads a message's role, defaulting to "" when absent or not a string.
func (m Message) role() string {
	raw, ok := m["role"]
	if !ok {
		return ""
	}
	var r string
	if err := jsonv1.Unmarshal(raw, &r); err != nil {
		return ""
	}
	return r
}

// SanitizeText returns a message's content when it is a plain string, and
// reports false for any other content shape. Only string content is rewritten;
// an array-of-parts body is left alone rather than flattened, because
// flattening drops the part types a multimodal model needs.
func SanitizeText(m Message) (string, bool) {
	if m.role() == "system" {
		return "", false
	}
	raw, ok := m["content"]
	if !ok {
		return "", false
	}
	var s string
	if err := jsonv1.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// Rewrite sanitizes every non-system message in body and returns a new body
// with exactly one system message at index 0 carrying prompt.
//
// Every client-sent system message is dropped: the client cannot inject
// instructions ahead of the proxy's own prompt, and it cannot leave a second
// one behind for a later message to be mistaken for. System content is not
// merged in — a client that wants the proxy prompt to say more should say so
// out of band, not by smuggling a second system turn through this path.
//
// A body with no "messages" key is returned untouched. The Responses API
// carries its conversation in "input", and synthesizing a messages array for
// one would turn a native Responses request into a hybrid the upstream cannot
// serve.
//
// Every field other than messages is copied through untouched, and every
// field of every message other than the rewritten content is preserved.
func Rewrite(body []byte, prompt string) ([]byte, error) {
	if strings.TrimSpace(prompt) == "" {
		prompt = DefaultPrompt
	}
	var raw Body
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("systemprompt: decode request: %w", err)
	}

	rawMsgs, ok := raw["messages"]
	if !ok {
		// Not a Chat Completions body (or a malformed one). Neither the
		// reorder nor the sanitize applies, and inventing a messages array
		// here would corrupt the request rather than normalize it.
		return body, nil
	}

	var msgs []Message
	if err := json.Unmarshal(rawMsgs, &msgs); err != nil {
		return nil, fmt.Errorf("systemprompt: decode messages: %w", err)
	}

	out := make([]Message, 0, len(msgs)+1)
	out = append(out, Message{
		"role":    mustJSON("system"),
		"content": mustJSON(prompt),
	})

	for _, m := range msgs {
		if m.role() == "system" {
			continue
		}
		text, ok := SanitizeText(m)
		if !ok {
			out = append(out, m)
			continue
		}
		clean := Sanitize(text)
		if clean == text {
			out = append(out, m)
			continue
		}
		// Copy rather than mutate: the same map may be shared with the
		// caller's decoded body.
		c := make(Message, len(m))
		for k, v := range m {
			c[k] = v
		}
		c["content"] = mustJSON(clean)
		out = append(out, c)
	}

	encodedMsgs, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("systemprompt: encode messages: %w", err)
	}
	raw["messages"] = encodedMsgs

	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("systemprompt: encode request: %w", err)
	}
	return encoded, nil
}

// Verify checks the invariant the pipeline promises: exactly one system
// message, at index 0. It returns ErrInvariant with a description of what it
// actually found, because the caller's log line is the only record of a
// violation and "invariant failed" alone is not actionable.
//
// A body with no messages key is not a violation: that is a Responses-native
// request, which carries its conversation in "input" and is never rewritten.
// Rewrite returns such a body untouched, and Verify agrees by returning nil —
// asserting otherwise would fail every Responses request on the gateway.
func Verify(body []byte) error {
	var raw Body
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("systemprompt: decode for verify: %w", err)
	}
	rawMsgs, ok := raw["messages"]
	if !ok {
		return nil // not a Chat Completions body; nothing was rewritten
	}
	var msgs []Message
	if err := json.Unmarshal(rawMsgs, &msgs); err != nil {
		return fmt.Errorf("systemprompt: decode messages for verify: %w", err)
	}
	if len(msgs) == 0 {
		return fmt.Errorf("%w: body has an empty messages array", ErrInvariant)
	}
	if msgs[0].role() != "system" {
		return fmt.Errorf("%w: index 0 is role %q, want \"system\"", ErrInvariant, msgs[0].role())
	}
	for i, m := range msgs {
		if i > 0 && m.role() == "system" {
			return fmt.Errorf("%w: system message also at index %d", ErrInvariant, i)
		}
	}
	return nil
}

// mustJSON encodes a string that is known to be marshalable.
func mustJSON(s string) jsonv1.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		// A Go string always marshals; this is unreachable by construction
		// and panicking here is better than emitting invalid JSON upstream.
		panic("systemprompt: string is not marshalable: " + err.Error())
	}
	return b
}
