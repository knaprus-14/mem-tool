package mem

import (
	"errors"
	"strings"
)

var ErrAnswerContextLimit = errors.New("model input exceeds the available context")

func isAnswerContextLimit(body []byte) bool {
	s := strings.ToLower(string(body))
	for _, phrase := range []string{"maximum context length", "context length exceeded", "exceeds the context window", "input length exceeds", "prompt is too long", "input is too long"} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}
