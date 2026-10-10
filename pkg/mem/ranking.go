package mem

import (
	"sort"
	"strings"
	"time"
)

func SortSearchResults(results []Entry) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if results[i].LexicalScore != results[j].LexicalScore {
			return results[i].LexicalScore > results[j].LexicalScore
		}
		if results[i].VectorScore != results[j].VectorScore {
			return results[i].VectorScore > results[j].VectorScore
		}
		return results[i].ID < results[j].ID
	})
}

// reRankResults применяет повышающие коэффициенты:
// - Freshness: записи младше 7 дней → *1.05
// - TagMatch: теги пересекаются со словами запроса → *1.10
// - Important: важные записи → *1.15
// Возвращает количество записей, получивших буст
func ReRankResults(results []Entry, query string) int {
	if len(results) == 0 {
		return 0
	}

	now := time.Now()
	queryWords := strings.Fields(strings.ToLower(query))
	count := 0

	for i := range results {
		boost := 1.0
		hasBoost := false

		// Freshness boost: записи младше 7 дней
		if t, err := time.Parse(time.RFC3339, results[i].Created); err == nil {
			if now.Sub(t) < 7*24*time.Hour {
				boost *= 1.05
				hasBoost = true
			}
		}

		// Tag match boost: совпадение тегов со словами запроса
		for _, tag := range results[i].Tags {
			tagLower := strings.ToLower(tag)
			for _, word := range queryWords {
				if tagLower == word || strings.Contains(tagLower, word) || strings.Contains(word, tagLower) {
					boost *= 1.10
					hasBoost = true
					goto nextEntry
				}
			}
		}

	nextEntry:
		// Important boost: важные записи поднимаем на 15%
		if results[i].Important {
			boost *= 1.15
			hasBoost = true
		}

		if hasBoost {
			newScore := results[i].Score * boost
			if newScore > 1.0 {
				newScore = 1.0
			}
			results[i].Score = newScore
			count++
		}
	}

	return count
}
