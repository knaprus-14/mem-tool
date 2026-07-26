package main

import (
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Chunk — один фрагмент документа
type Chunk struct {
	Text  string
	Label string // описание: "стр 5, раздел 2.1" или первые символы
	Index int    // номер чанка в документе (с 0)
}

// ChunkDocument разбивает текст документа на чанки согласно конфигу
func ChunkDocument(text string, maxSize, overlap int, strategy string) []Chunk {
	if maxSize <= 0 {
		maxSize = 1000
	}
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= maxSize {
		overlap = maxSize / 5
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	switch strategy {
	case "sentence":
		return chunkBySentence(text, maxSize, overlap)
	case "fixed":
		return chunkByFixed(text, maxSize, overlap)
	default: // "paragraph"
		return chunkByParagraph(text, maxSize, overlap)
	}
}

// === Чанкинг по абзацам (по умолчанию) ===

func chunkByParagraph(text string, maxSize, overlap int) []Chunk {
	// Разбиваем на абзацы по двойному переносу строки
	paragraphs := splitParagraphs(text)
	if len(paragraphs) == 0 {
		return nil
	}

	var chunks []Chunk
	var current strings.Builder
	var currentSize int
	var chunkLabel string
	chunkIdx := 0

	flush := func() {
		if current.Len() == 0 {
			return
		}
		label := chunkLabel
		if label == "" {
			label = firstLine(current.String())
		}
		chunks = append(chunks, Chunk{
			Text:  strings.TrimSpace(current.String()),
			Label: label,
			Index: chunkIdx,
		})
		chunkIdx++
	}

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		paraSize := utf8.RuneCountInString(para)

		// Если абзац сам по себе больше maxSize — режем его по предложениям
		if paraSize > maxSize {
			// Сначала сбрасываем текущее
			flush()

			// Дробим абзац на предложения и группируем
			sentences := splitSentences(para)
			var subBuilder strings.Builder
			subSize := 0
			for _, sent := range sentences {
				sent = strings.TrimSpace(sent)
				sentSize := utf8.RuneCountInString(sent)
				if subSize+sentSize > maxSize && subBuilder.Len() > 0 {
					chunks = append(chunks, Chunk{
						Text:  strings.TrimSpace(subBuilder.String()),
						Label: firstLine(subBuilder.String()),
						Index: chunkIdx,
					})
					chunkIdx++
					subBuilder.Reset()
					subSize = 0
				}
				if subBuilder.Len() > 0 {
					subBuilder.WriteString(" ")
				}
				subBuilder.WriteString(sent)
				subSize += sentSize
			}
			if subBuilder.Len() > 0 {
				chunks = append(chunks, Chunk{
					Text:  strings.TrimSpace(subBuilder.String()),
					Label: firstLine(subBuilder.String()),
					Index: chunkIdx,
				})
				chunkIdx++
			}
			current.Reset()
			currentSize = 0
			chunkLabel = ""
			continue
		}

		// Если не влезает — сбрасываем
		if currentSize+paraSize+1 > maxSize && current.Len() > 0 {
			flush()
			current.Reset()
			currentSize = 0
			chunkLabel = ""
		}

		// Запоминаем метку первого непустого абзаца в чанке
		if chunkLabel == "" && para != "" {
			chunkLabel = truncate(para, 80)
		}

		if current.Len() > 0 {
			current.WriteString("\n\n")
			currentSize += 2
		}
		current.WriteString(para)
		currentSize += paraSize
	}

	flush()

	// Добавляем перекрытие между чанками
	if overlap > 0 && len(chunks) > 1 {
		chunks = addOverlap(chunks, overlap)
	}

	return chunks
}

// === Чанкинг по предложениям ===

func chunkBySentence(text string, maxSize, overlap int) []Chunk {
	sentences := splitSentences(text)
	if len(sentences) == 0 {
		return nil
	}

	var chunks []Chunk
	var current strings.Builder
	currentSize := 0
	chunkIdx := 0

	for _, sent := range sentences {
		sent = strings.TrimSpace(sent)
		if sent == "" {
			continue
		}
		sentSize := utf8.RuneCountInString(sent)

		if currentSize+sentSize+1 > maxSize && current.Len() > 0 {
			chunks = append(chunks, Chunk{
				Text:  strings.TrimSpace(current.String()),
				Label: firstLine(current.String()),
				Index: chunkIdx,
			})
			chunkIdx++
			current.Reset()
			currentSize = 0
		}

		if current.Len() > 0 {
			current.WriteString(" ")
			currentSize++
		}
		current.WriteString(sent)
		currentSize += sentSize
	}

	if current.Len() > 0 {
		chunks = append(chunks, Chunk{
			Text:  strings.TrimSpace(current.String()),
			Label: firstLine(current.String()),
			Index: chunkIdx,
		})
	}

	if overlap > 0 && len(chunks) > 1 {
		chunks = addOverlap(chunks, overlap)
	}

	return chunks
}

// === Чанкинг фиксированного размера ===

func chunkByFixed(text string, maxSize, overlap int) []Chunk {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}

	var chunks []Chunk
	chunkIdx := 0
	pos := 0

	for pos < len(runes) {
		end := pos + maxSize
		if end > len(runes) {
			end = len(runes)
		}

		// Пытаемся разорвать на границе слова (пробела)
		if end < len(runes) {
			// Ищем пробел перед end
			cut := end
			for j := end; j > pos+maxSize/2; j-- {
				if runes[j-1] == ' ' || runes[j-1] == '\n' {
					cut = j
					break
				}
			}
			end = cut
		}

		chunkText := string(runes[pos:end])
		chunks = append(chunks, Chunk{
			Text:  strings.TrimSpace(chunkText),
			Label: firstLine(chunkText),
			Index: chunkIdx,
		})
		chunkIdx++

		// Сдвиг с перекрытием
		nextPos := end - overlap
		if nextPos <= pos {
			nextPos = end
		}
		pos = nextPos
	}

	return chunks
}

// === Вспомогательные функции ===

// splitParagraphs разбивает текст на абзацы по двойному переносу строки
func splitParagraphs(text string) []string {
	// Нормализуем концы строк
	text = strings.ReplaceAll(text, "\r\n", "\n")

	// Разделяем по двум и более переводам строк
	re := regexp.MustCompile(`\n{2,}`)
	parts := re.Split(text, -1)

	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// splitSentences разбивает текст на предложения
func splitSentences(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	// Заменяем переносы строк внутри абзаца на пробелы
	text = strings.ReplaceAll(text, "\n", " ")

	// Регулярка для границ предложений
	// Ищем . ! ? за которыми идёт пробел и заглавная буква
	re := regexp.MustCompile(`([.!?])\s+(\p{Lu})`)
	result := re.Split(text, -1)

	// Если не удалось разбить — возвращаем как одно предложение
	if len(result) <= 1 {
		return []string{strings.TrimSpace(text)}
	}

	// Собираем результат
	var sentences []string
	for _, s := range result {
		s = strings.TrimSpace(s)
		if s != "" {
			sentences = append(sentences, s)
		}
	}

	return sentences
}

// addOverlap добавляет перекрытие: последние overlap символов из чанка N
// добавляются в начало чанка N+1 как контекст
func addOverlap(chunks []Chunk, overlap int) []Chunk {
	result := make([]Chunk, len(chunks))
	copy(result, chunks)

	for i := 1; i < len(chunks); i++ {
		prevRunes := []rune(result[i-1].Text)
		if len(prevRunes) > overlap {
			overlapText := string(prevRunes[len(prevRunes)-overlap:])
			// Проверяем, не начинается ли уже следующий чанк с этого текста
			currText := result[i].Text
			if !strings.HasPrefix(currText, overlapText) {
				result[i].Text = overlapText + "\n" + currText
			}
		}
	}

	return result
}

// firstLine возвращает первую строку текста (для метки)
func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if idx := strings.Index(text, "\n"); idx > 0 {
		return truncate(text[:idx], 80)
	}
	return truncate(text, 80)
}

// truncate обрезает строку до maxLen символов
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// isSentenceBoundary проверяет, является ли символ границей предложения
func isSentenceBoundary(r rune) bool {
	return r == '.' || r == '!' || r == '?' || r == '\n'
}

// CountTokens приблизительный подсчёт токенов (символы / 2)
func CountTokens(text string) int {
	return int(math.Ceil(float64(utf8.RuneCountInString(text)) / 2.0))
}
