package memory_chunk

import "unicode"

// Tokenizer counts the tokens of a text. The chunker only needs a count, not the tokens themselves.
//
// Counts must be (roughly) additive over text separated by whitespace: Count("a b") should be about Count("a") +
// Count("b"). The chunker relies on it to size chunks without recounting every candidate.
type Tokenizer interface {
	Count(text string) int
}

// Estimator is the default Tokenizer: a pure-Go estimate of the token count of a BPE tokenizer such as the
// cl100k_base one used by OpenAI's text-embedding-3 models. It needs no vocabulary file and no network.
//
// It is deliberately a little conservative (it tends to over-count by 10-20% on English prose), so that a chunk sized
// by it stays under the real limit of the embedding model. Whitespace is free; every other character belongs to a run:
//
//   - a run of letters costs 1 token per 4 ASCII letters (rounded up), or per 2 non-ASCII letters;
//   - a run of digits costs 1 token per 3 digits (rounded up);
//   - a Han, Hiragana, Katakana or Hangul character costs 1 token;
//   - any other character (punctuation, symbols, emoji) costs 1 token.
//
// To size chunks by the exact tokens of a model, implement Tokenizer on top of that model's tokenizer and set it
// in Config.Tokenizer.
type Estimator struct{}

// Count implements Tokenizer.
func (Estimator) Count(text string) int { return EstimateTokens(text) }

// EstimateTokens is the token count of text by the rules of Estimator.
func EstimateTokens(text string) int {
	var (
		total    int
		ascii    int // letters of the current letter run
		nonASCII int
		digits   int
	)
	flush := func() {
		total += (ascii+3)/4 + (nonASCII+1)/2 + (digits+2)/3
		ascii, nonASCII, digits = 0, 0, 0
	}
	// A run is either letters or digits; any other character ends it.
	for _, r := range text {
		switch {
		case isCJK(r):
			flush()
			total++
		case unicode.IsLetter(r) || unicode.IsMark(r):
			if digits > 0 {
				flush()
			}
			if r < 0x80 {
				ascii++
			} else {
				nonASCII++
			}
		case unicode.IsDigit(r):
			if ascii > 0 || nonASCII > 0 {
				flush()
			}
			digits++
		case unicode.IsSpace(r):
			flush()
		default:
			flush()
			total++
		}
	}
	flush()
	return total
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}
