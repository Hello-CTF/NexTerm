package memory

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

const (
	bm25K1 = 1.2
	bm25B  = 0.75
	// recencyWeight keeps the recency boost below one matched rare term, so
	// it orders otherwise equal entries without outranking a real match.
	recencyWeight = 0.1
)

// rankEntries orders entries by relevance to query. Without searchable query
// terms the incoming order is kept. Ties fall back to topic then id.
func rankEntries(entries []Entry, query string) []Entry {
	queryTerms := uniqueTerms(tokenize(query))
	if len(entries) < 2 || len(queryTerms) == 0 {
		return entries
	}
	docs := make([]map[string]int, len(entries))
	lengths := make([]int, len(entries))
	docFreq := make(map[string]int)
	total := 0
	for i, entry := range entries {
		terms := tokenize(entry.Topic + " " + entry.Content)
		counts := make(map[string]int, len(terms))
		for _, term := range terms {
			counts[term]++
		}
		docs[i], lengths[i] = counts, len(terms)
		total += len(terms)
		for term := range counts {
			docFreq[term]++
		}
	}
	avg := float64(total) / float64(len(entries))
	if avg == 0 {
		avg = 1
	}
	minUpdated, maxUpdated := entries[0].UpdatedAt, entries[0].UpdatedAt
	for _, entry := range entries {
		minUpdated = min(minUpdated, entry.UpdatedAt)
		maxUpdated = max(maxUpdated, entry.UpdatedAt)
	}
	n := float64(len(entries))
	scores := make([]float64, len(entries))
	for i, entry := range entries {
		for _, term := range queryTerms {
			tf := float64(docs[i][term])
			if tf == 0 {
				continue
			}
			df := float64(docFreq[term])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			norm := tf + bm25K1*(1-bm25B+bm25B*float64(lengths[i])/avg)
			scores[i] += idf * tf * (bm25K1 + 1) / norm
		}
		if maxUpdated > minUpdated {
			scores[i] += recencyWeight * float64(entry.UpdatedAt-minUpdated) / float64(maxUpdated-minUpdated)
		}
	}
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := order[a], order[b]
		if scores[x] != scores[y] {
			return scores[x] > scores[y]
		}
		if entries[x].Topic != entries[y].Topic {
			return entries[x].Topic < entries[y].Topic
		}
		return entries[x].ID < entries[y].ID
	})
	ranked := make([]Entry, len(entries))
	for i, index := range order {
		ranked[i] = entries[index]
	}
	return ranked
}

func uniqueTerms(terms []string) []string {
	seen := make(map[string]struct{}, len(terms))
	result := make([]string, 0, len(terms))
	for _, term := range terms {
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		result = append(result, term)
	}
	return result
}

// tokenize yields lowercase Latin/digit words (length >= 2) and, for CJK
// runs, character bigrams (a lone character stays a unigram).
func tokenize(text string) []string {
	var terms []string
	var word, cjk []rune
	flushWord := func() {
		if len(word) >= 2 {
			terms = append(terms, string(word))
		}
		word = word[:0]
	}
	flushCJK := func() {
		if len(cjk) == 1 {
			terms = append(terms, string(cjk))
		}
		cjk = cjk[:0]
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case isCJK(r):
			flushWord()
			cjk = append(cjk, r)
			if len(cjk) >= 2 {
				terms = append(terms, string(cjk[len(cjk)-2:]))
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			flushCJK()
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return terms
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}
