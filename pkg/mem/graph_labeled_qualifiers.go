package mem

import (
	"reflect"
	"regexp"
	"strings"
	"unicode"
)

var knowledgeLabeledNumbers = regexp.MustCompile(`[-+−]?\d+(?:[.,]\d+)*(?:\s*[–—-]\s*\d+(?:[.,]\d+)*)?`)

// Materialized requirements repeat quoted qualifiers for readability. Only
// discount these repetitions when EVERY appended part already occurs verbatim
// in the statement (allowing whitespace). Unknown or additional prose retains
// the original score. Qualifier safeguards still inspect the complete body.
func knowledgeLabeledRequirementScoringText(body string) string {
	parts := strings.Split(strings.TrimSpace(body), "\n\n")
	statement, ok := strings.CutPrefix(parts[0], "Требование: ")
	if !ok || strings.TrimSpace(statement) == "" {
		return body
	}
	normalized := " " + strings.Join(strings.Fields(statement), " ") + " "
	for _, part := range parts[1:] {
		quote := ""
		for _, prefix := range []string{"Условие: ", "Исключение: ", "Ограничение: "} {
			if value, found := strings.CutPrefix(part, prefix); found {
				quote = strings.Join(strings.Fields(value), " ")
				break
			}
		}
		if quote == "" || !strings.Contains(normalized, " "+quote+" ") {
			return body
		}
	}
	return statement
}

// Conservative lexical safeguards, not an entailment model. Paraphrases that
// change these markers need explicit aliases in the annotated benchmark.
func knowledgeLabeledQualifiersCompatible(left, right string, required []string) bool {
	normalized := " " + normalizeKnowledgeIdentityText(right) + " "
	for _, fragment := range required {
		if !strings.Contains(normalized, normalizeKnowledgeIdentityText(fragment)) {
			return false
		}
	}
	return reflect.DeepEqual(knowledgeLabeledCriticalSignature(left), knowledgeLabeledCriticalSignature(right))
}

func knowledgeLabeledCriticalSignature(text string) map[string]bool {
	result := make(map[string]bool)
	// Splitting numbers from letters also recognizes units in "6МПа".
	for _, token := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
		switch token {
		case "не", "нет", "без", "запрещается", "запрещено", "только", "кроме", "исключением", "если", "при", "более", "менее", "до", "после",
			"должен", "должна", "должно", "должны", "следует", "необходимо", "требуется", "может", "могут", "можно", "допускается", "допускаются",
			"not", "no", "unless", "except", "only", "must", "may",
			"мм", "см", "м", "км", "мпа", "кпа", "па", "бар", "с", "мин", "ч", "кг", "г", "л", "mm", "cm", "m", "mpa", "kpa", "bar":
			result["word:"+token] = true
		}
	}
	for _, number := range knowledgeLabeledNumbers.FindAllString(text, -1) {
		number = strings.Join(strings.Fields(number), "")
		number = strings.NewReplacer(",", ".", "−", "-", "–", "-", "—", "-").Replace(number)
		result["number:"+number] = true
	}
	for _, symbol := range []string{"<", ">", "≤", "≥", "%", "²", "³"} {
		if strings.Contains(text, symbol) {
			result["symbol:"+symbol] = true
		}
	}
	return result
}
