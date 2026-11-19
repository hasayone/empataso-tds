package admin

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// aiMaxNudges is how many replies in a row the turn answers with a nudge. A nudge is one more
	// model call and counts as a step, so the turn still ends at the step ceiling.
	aiMaxNudges = 3

	// aiPromiseTail is how many characters at the end of a reply are read for a promise.
	aiPromiseTail = 240

	// aiSameFailMax is how many times one tool call, with the same arguments, may fail before the
	// same call is refused without running. aiMaxRefusals is how many such refusals end the turn.
	aiSameFailMax = 2
	aiMaxRefusals = 3
)

// aiNudgeText is the user message that follows a reply which promised more work and made no tool
// call, or was cut off by the output limit. It is a fixed text and holds no data.
const aiNudgeText = "Continue the task now. If you still need data, call the tool in this reply. " +
	"If the task is complete, give the final answer and promise nothing further."

// aiTruncatedStops are the finish reasons of a reply the output limit cut: Anthropic max_tokens
// and the chat-completion length.
var aiTruncatedStops = map[string]bool{"max_tokens": true, "length": true}

// aiPromiseRe finds, in Russian, Ukrainian and English, a sentence in which the assistant says it
// will fetch, check or continue.
var aiPromiseRe = regexp.MustCompile(`(?i)(загруж|продолж|проверю|проверим|запрош|получу|соберу|посмотрю|выполню|сейчас |завантаж|продовж|перевір|зберу|отримаю|зараз |let me|i'll|i will|i am going to|i'm going to|next page|more pages|continuing|proceeding)`)

// aiPromisesMore reports whether text ends in a promise of more work: its last aiPromiseTail
// characters match aiPromiseRe and the text is not a question to the user.
func aiPromisesMore(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasSuffix(text, "?") {
		return false
	}
	if n := utf8.RuneCountInString(text); n > aiPromiseTail {
		text = string([]rune(text)[n-aiPromiseTail:])
	}
	return aiPromiseRe.MatchString(text)
}

// shouldNudge reports whether a reply without tool calls must be answered with a nudge instead of
// ending the turn: it was cut by the output limit, or the turn has run a tool and the reply ends
// in a promise of more work. Never more than aiMaxNudges in a row.
func (t *aiTurn) shouldNudge(rep aiReply, text string) bool {
	if t.nudges >= aiMaxNudges {
		return false
	}
	return aiTruncatedStops[rep.StopReason] || t.toolRuns > 0 && aiPromisesMore(text)
}

// aiCallKey returns the key of a tool call for the failure count: the tool and its arguments in
// compact form.
func aiCallKey(call aiToolCall) string {
	var b bytes.Buffer
	if json.Compact(&b, call.Args) != nil {
		b.Reset()
		b.Write(call.Args)
	}
	return call.Name + " " + b.String()
}

// repeatedFailure reports whether the call already failed aiSameFailMax times in this turn. Such a
// call is refused without running; the aiMaxRefusals-th refusal sets stalled.
func (t *aiTurn) repeatedFailure(call aiToolCall) bool {
	if t.failed[aiCallKey(call)] < aiSameFailMax {
		return false
	}
	t.refused++
	return true
}

// noteFailure counts one failure of the call.
func (t *aiTurn) noteFailure(call aiToolCall) {
	if t.failed == nil {
		t.failed = map[string]int{}
	}
	t.failed[aiCallKey(call)]++
}

// stalled reports whether the turn refused aiMaxRefusals calls for repeating a failure and must
// stop instead of looping on the same page.
func (t *aiTurn) stalled() bool { return t.refused >= aiMaxRefusals }

// aiStalledTexts is the closing line of a turn that stopped because a tool kept failing on the
// same call, by language.
var aiStalledTexts = map[string]string{
	"en": "I stopped: the same tool call keeps failing, so repeating it will not help. Tell me how to change it, or narrow the request.",
	"ru": "Останавливаюсь: один и тот же вызов инструмента снова и снова заканчивается ошибкой, повтор не поможет. Подскажите, как его изменить, или сузьте запрос.",
	"uk": "Зупиняюся: той самий виклик інструмента знову й знову завершується помилкою, повтор не допоможе. Підкажіть, як його змінити, або звузьте запит.",
}

// aiStalledText returns the closing line of a stalled turn in lang.
func aiStalledText(lang string) string {
	if text, ok := aiStalledTexts[lang]; ok {
		return text
	}
	return aiStalledTexts[aiLangDefault]
}
