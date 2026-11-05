package mime

import (
	"strings"
	"unicode"
)

// missingWordShare is how many words of a text may stand for one word that the
// other text does not have: one word in 20. Two versions of a body that say the
// same still differ a little, for example in the alt text of images.
const missingWordShare = 20

// maxMissingWords is the most words that a text of any length may have that the
// other text does not have.
const maxMissingWords = 8

// TextsDiffer reports whether other, a second version of the body of a message,
// says something that shown, the version that a mail client shows, does not say.
// Both are plain text: a caller turns HTML into text first.
//
// The versions of one message are written in different ways even when they say
// the same, so the words are compared, not the text. Case, punctuation, white
// space and links do not count, and neither does the order of the words. Text
// that only shown has does not count either: the reader sees it.
//
// A text of fewer than 20 words may not have one word that shown does not have.
// A longer text may have one in 20, and at most 8. So a few words added to a long
// text are not reported. This result is a hint for the reader. It is not a
// security check: the other version is not returned to the agent.
func TextsDiffer(shown, other string) bool {
	seen := map[string]bool{}
	for _, word := range textWords(shown) {
		seen[word] = true
	}
	words := textWords(other)
	missing := 0
	for _, word := range words {
		if !seen[word] {
			missing++
		}
	}
	return missing > min(len(words)/missingWordShare, maxMissingWords)
}

// textWords returns the words of a text in lower case, without punctuation and
// without links. HTMLToText writes a link in its own way, so a link says nothing
// about whether two texts are the same.
func textWords(text string) []string {
	var words []string
	for _, field := range strings.Fields(text) {
		field = strings.ToLower(field)
		if isLinkText(field) {
			continue
		}
		words = append(words, strings.FieldsFunc(field, notWordRune)...)
	}
	return words
}

// isLinkText reports whether a piece of text without spaces is a link. field is
// in lower case.
func isLinkText(field string) bool {
	return strings.Contains(field, "://") || strings.Contains(field, "mailto:") || strings.Contains(field, "www.")
}

func notWordRune(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }
