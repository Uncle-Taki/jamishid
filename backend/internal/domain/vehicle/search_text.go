package vehicle

import (
	"embed"
	"encoding/json"
	"strings"
	"unicode"
)

// NormalizeSearchText preserves original display text elsewhere; only the search
// representation folds Persian/Arabic variants, digits and punctuation.
func NormalizeSearchText(value string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(value) {
		switch {
		case r == 'ي' || r == 'ى':
			r = 'ی'
		case r == 'ك':
			r = 'ک'
		case r >= '۰' && r <= '۹':
			r = '0' + r - '۰'
		case r >= '٠' && r <= '٩':
			r = '0' + r - '٠'
		case unicode.Is(unicode.Mn, r):
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(r)
		} else {
			out.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

//go:embed normalization_contract.json
var normalizationFiles embed.FS
var normalizationContract = func() struct {
	Version       string            `json:"version"`
	TokenAliases  map[string]string `json:"tokenAliases"`
	PhraseAliases map[string]string `json:"phraseAliases"`
} {
	var result struct {
		Version       string            `json:"version"`
		TokenAliases  map[string]string `json:"tokenAliases"`
		PhraseAliases map[string]string `json:"phraseAliases"`
	}
	raw, err := normalizationFiles.ReadFile("normalization_contract.json")
	if err != nil {
		panic(err)
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		panic(err)
	}
	return result
}()

func NormalizationVersion() string { return normalizationContract.Version }

// NormalizeModelText keeps digit groups intact and applies only explicit aliases.
// Type/trim digits are never dropped. Display text and structured IDs are untouched.
func NormalizeModelText(value string) string {
	text := NormalizeSearchText(value)
	var out strings.Builder
	var previous rune
	for _, r := range text {
		if (unicode.IsLetter(previous) && unicode.IsDigit(r)) || (unicode.IsDigit(previous) && unicode.IsLetter(r)) {
			out.WriteByte(' ')
		}
		out.WriteRune(r)
		previous = r
	}
	text = out.String()
	// Alias phrases are non-overlapping in the versioned contract.
	for alias, canonical := range normalizationContract.PhraseAliases {
		text = strings.TrimSpace(strings.ReplaceAll(" "+text+" ", " "+alias+" ", " "+canonical+" "))
	}
	tokens := strings.Fields(text)
	for i, token := range tokens {
		if canonical, ok := normalizationContract.TokenAliases[token]; ok {
			tokens[i] = canonical
		}
	}
	return strings.Join(tokens, " ")
}
