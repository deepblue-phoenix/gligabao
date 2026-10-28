// Package chunk splits a long text into overlapping chunks sized in tokens, cutting at paragraph boundaries.
//
// It is what `remember` uses on a text larger than one chunk (see docs/chunking.md). The package has no
// dependencies on the rest of gligabao.
package memory_chunk

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// DefaultSize is the default chunk size in tokens (and so the size above which a text is chunked).
	DefaultSize = 1024
	// DefaultOverlapPercent is the default overlap, as a percentage of the chunk size.
	DefaultOverlapPercent = 20

	// MinSize and MaxSize bound Config.Size. The upper bound keeps a chunk within the 8191-token input limit of the
	// OpenAI embedding models, since every chunk is embedded as a whole.
	MinSize = 32
	MaxSize = 8000
	// MaxOverlapPercent bounds Config.OverlapPercent: more than half of a chunk repeated is mostly waste.
	MaxOverlapPercent = 50
)

// Config sets how a text is chunked.
type Config struct {
	// Size is the largest chunk, in tokens, overlap included. 0 means DefaultSize.
	Size int
	// OverlapPercent is how much of the previous chunk is repeated at the start of the next one, as a percentage of
	// Size (0 = no overlap). Use DefaultConfig for the default of DefaultOverlapPercent.
	OverlapPercent int
	// Tokenizer counts tokens. nil means Estimator.
	Tokenizer Tokenizer
}

// DefaultConfig is DefaultSize tokens with DefaultOverlapPercent overlap, counted by Estimator.
func DefaultConfig() Config {
	return Config{Size: DefaultSize, OverlapPercent: DefaultOverlapPercent}
}

// WithDefaults returns c with the defaults for an unset Size and Tokenizer.
func (c Config) WithDefaults() Config {
	if c.Size == 0 {
		c.Size = DefaultSize
	}
	if c.Tokenizer == nil {
		c.Tokenizer = Estimator{}
	}
	return c
}

// Validate reports whether the settings (with defaults applied) are usable.
func (c Config) Validate() error {
	c = c.WithDefaults()
	if c.Size < MinSize || c.Size > MaxSize {
		return fmt.Errorf("chunk size %d must be between %d and %d tokens", c.Size, MinSize, MaxSize)
	}
	if c.OverlapPercent < 0 || c.OverlapPercent > MaxOverlapPercent {
		return fmt.Errorf("chunk overlap %d%% must be between 0 and %d", c.OverlapPercent, MaxOverlapPercent)
	}
	return nil
}

// NeedsChunking reports whether text is larger than one chunk.
func (c Config) NeedsChunking(text string) bool {
	c = c.WithDefaults()
	return c.Tokenizer.Count(text) > c.Size
}

// Chunk is one piece of a text.
type Chunk struct {
	// Index is the position of the chunk in the text, from 0.
	Index int
	// Text is the chunk: text[Start:End] of the original, so consecutive chunks keep the original separators.
	Text string
	// Tokens is the token count of Text.
	Tokens int
	// Start and End are the byte offsets of Text in the original text.
	Start, End int
	// OverlapTokens is the size of the part at the start of Text that repeats the end of the previous chunk (0 for
	// the first chunk, and when no overlap fits).
	OverlapTokens int
}

//---------------------------------------------------------------------------------------------

// Split cuts text into chunks of at most Config.Size tokens.
//
//  1. The text is split into paragraphs (separated by blank lines).
//  2. A paragraph larger than a chunk is split further, into sentences (and lines), and a sentence that is still too
//     large into words (and, for a single huge word, into pieces of it). The result is a list of units, each fitting a
//     chunk and each starting and ending at the best boundary available.
//  3. Units are packed greedily into chunks, so a chunk ends at a paragraph boundary whenever the next paragraph
//     does not fit, and a paragraph is only cut when it alone exceeds the chunk size.
//  4. Every chunk but the first starts with the tail of the previous one, at most Size*OverlapPercent/100 tokens:
//     the last whole units that fit in that budget, or, if even the last unit is larger than the budget, its last
//     words. The overlap is shortened (or dropped) if the chunk would otherwise not have room for its first new unit.
//
// A text that fits one chunk yields one chunk. Empty or blank text yields none.
func Split(text string, cfg Config) ([]Chunk, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	budget := cfg.Size * cfg.OverlapPercent / 100
	// Units cut out of a sentence (words, pieces of a word) are kept small enough to leave room for the overlap.
	s := &splitter{text: text, tok: cfg.Tokenizer, size: cfg.Size, cutSize: cfg.Size - budget}
	units := s.units()
	if len(units) == 0 {
		return nil, errors.New("chunk: no text units") // unreachable: the text is not blank
	}

	var chunks []Chunk
	prevFirst := 0 // index of the first unit of the previous chunk (including its overlap)
	for i := 0; i < len(units); {
		// The overlap: whole units before i that fit the budget, else a word tail of the unit before i.
		ovStart, ovTokens := units[i].start, 0
		first := i
		if i > 0 && budget > 0 {
			j := i - 1
			for j >= prevFirst && ovTokens+units[j].tokens <= budget {
				ovTokens += units[j].tokens
				ovStart = units[j].start
				first = j
				j--
			}
			if first == i { // not even the last unit fits: repeat its last words
				ovStart, ovTokens = s.tail(units[i-1], budget)
				if ovTokens == 0 {
					ovStart = units[i].start
				}
			}
		}
		// Make room for the first new unit by shortening the overlap.
		for ovTokens > 0 && ovTokens+units[i].tokens > cfg.Size {
			if first < i { // drop the oldest overlap unit
				ovTokens -= units[first].tokens
				first++
				if first < i {
					ovStart = units[first].start
				} else {
					ovStart, ovTokens = units[i].start, 0
				}
			} else { // a word tail: drop it
				ovStart, ovTokens = units[i].start, 0
			}
		}
		used := ovTokens
		k := i
		for k < len(units) && used+units[k].tokens <= cfg.Size {
			used += units[k].tokens
			k++
		}
		if k == i { // unreachable: every unit fits a chunk alone and the overlap was dropped if needed
			return nil, errors.New("chunk: internal error: no progress")
		}
		end := units[k-1].end
		body := text[ovStart:end]
		chunks = append(chunks, Chunk{
			Index: len(chunks), Text: body, Tokens: cfg.Tokenizer.Count(body),
			Start: ovStart, End: end, OverlapTokens: ovTokens,
		})
		prevFirst = first
		i = k
	}
	return chunks, nil
}

//---------------------------------------------------------------------------------------------

// unit is a span text[start:end] (no leading or trailing whitespace) that fits one chunk.
type unit struct {
	start, end, tokens int
}

type splitter struct {
	text string
	tok  Tokenizer
	size int
	// cutSize is the largest unit made by cutting a sentence into words (or a word into pieces): size less the
	// overlap, so that a chunk made of such a unit still has room for its overlap.
	cutSize int
}

// span is a byte range of the text.
type span struct{ start, end int }

// units splits the whole text: paragraphs, with the oversized ones broken down further.
func (s *splitter) units() []unit {
	var out []unit
	for _, p := range s.paragraphs() {
		n := s.tok.Count(s.text[p.start:p.end])
		if n <= s.size {
			out = append(out, unit{p.start, p.end, n})
			continue
		}
		for _, sen := range s.sentences(p) {
			m := s.tok.Count(s.text[sen.start:sen.end])
			if m <= s.size {
				out = append(out, unit{sen.start, sen.end, m})
				continue
			}
			out = append(out, s.words(sen)...)
		}
	}
	return out
}

// paragraphs returns the blocks of text separated by blank lines, trimmed.
func (s *splitter) paragraphs() []span {
	var out []span
	cur := span{-1, -1}
	flush := func() {
		if cur.start >= 0 {
			out = append(out, cur)
		}
		cur = span{-1, -1}
	}
	for pos := 0; pos < len(s.text); {
		nl := strings.IndexByte(s.text[pos:], '\n')
		lineEnd, next := len(s.text), len(s.text)
		if nl >= 0 {
			lineEnd, next = pos+nl, pos+nl+1
		}
		line := s.text[pos:lineEnd]
		if t := strings.TrimSpace(line); t == "" {
			flush()
		} else {
			if cur.start < 0 {
				cur.start = pos + (len(line) - len(strings.TrimLeftFunc(line, unicode.IsSpace)))
			}
			cur.end = pos + len(strings.TrimRightFunc(line, unicode.IsSpace))
		}
		pos = next
	}
	flush()
	return out
}

// sentences splits a paragraph after sentence-ending punctuation (followed by whitespace, or by a CJK full stop
// anywhere) and at line breaks, so lists and verse split by line too.
func (s *splitter) sentences(p span) []span {
	var out []span
	emit := func(a, b int) {
		seg := s.text[a:b]
		lead := len(seg) - len(strings.TrimLeftFunc(seg, unicode.IsSpace))
		seg = strings.TrimRightFunc(seg[lead:], unicode.IsSpace)
		if seg != "" {
			out = append(out, span{a + lead, a + lead + len(seg)})
		}
	}
	segStart := p.start
	for i := p.start; i < p.end; {
		r, w := utf8.DecodeRuneInString(s.text[i:p.end])
		i += w
		end := false
		switch {
		case r == '\n':
			end = true
		case r == '。' || r == '！' || r == '？':
			end = true
		case r == '.' || r == '!' || r == '?' || r == '…':
			j := i
			for j < p.end {
				c, cw := utf8.DecodeRuneInString(s.text[j:p.end])
				if !strings.ContainsRune(`"')]}»”’`, c) {
					break
				}
				j += cw
			}
			if j == p.end {
				i, end = j, true
			} else if c, _ := utf8.DecodeRuneInString(s.text[j:p.end]); unicode.IsSpace(c) {
				i, end = j, true
			}
		}
		if end {
			emit(segStart, i)
			segStart = i
		}
	}
	emit(segStart, p.end)
	return out
}

// words packs the words of sen into units of at most cutSize tokens; a word that is too large alone is cut in pieces.
func (s *splitter) words(sen span) []unit {
	var out []unit
	cur := unit{start: -1}
	flush := func() {
		if cur.start >= 0 {
			out = append(out, cur)
		}
		cur = unit{start: -1}
	}
	for _, w := range s.fields(sen) {
		n := s.tok.Count(s.text[w.start:w.end])
		if n > s.cutSize {
			flush()
			out = append(out, s.pieces(w)...)
			continue
		}
		if cur.start >= 0 && cur.tokens+n > s.cutSize {
			flush()
		}
		if cur.start < 0 {
			cur = unit{start: w.start}
		}
		cur.end = w.end
		cur.tokens += n
	}
	flush()
	return out
}

// fields returns the whitespace-separated words of sp.
func (s *splitter) fields(sp span) []span {
	var out []span
	start := -1
	for i, r := range s.text[sp.start:sp.end] {
		if unicode.IsSpace(r) {
			if start >= 0 {
				out = append(out, span{sp.start + start, sp.start + i})
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, span{sp.start + start, sp.end})
	}
	return out
}

// pieces cuts one word that is larger than cutSize into the longest runs of characters that fit.
func (s *splitter) pieces(w span) []unit {
	var out []unit
	for w.start < w.end {
		word := s.text[w.start:w.end]
		// The longest rune-aligned prefix that fits, by binary search on the number of runes.
		n := utf8.RuneCountInString(word)
		lo, hi := 1, n // lo always fits: one character is at most a few tokens
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if s.tok.Count(word[:runeOffset(word, mid)]) <= s.cutSize {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		off := runeOffset(word, lo)
		out = append(out, unit{w.start, w.start + off, s.tok.Count(word[:off])})
		w.start += off
	}
	return out
}

// runeOffset is the byte offset of the n-th rune of s (n may be the number of runes).
func runeOffset(s string, n int) int {
	for i := range s {
		if n == 0 {
			return i
		}
		n--
	}
	return len(s)
}

// tail returns the start offset and token count of the last words of u that fit in budget tokens (0 tokens if not
// even the last word does).
func (s *splitter) tail(u unit, budget int) (start, tokens int) {
	ws := s.fields(span{u.start, u.end})
	start = u.end
	for i := len(ws) - 1; i >= 0; i-- {
		n := s.tok.Count(s.text[ws[i].start:ws[i].end])
		if tokens+n > budget {
			break
		}
		tokens += n
		start = ws[i].start
	}
	if tokens == 0 {
		return u.end, 0
	}
	return start, tokens
}
