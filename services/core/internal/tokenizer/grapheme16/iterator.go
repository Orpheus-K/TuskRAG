// Package graphemes pins the Unicode 16 grapheme segmentation used by the
// reference BGE-M3 tokenizer.
package graphemes

import "errors"

// Next returns one grapheme and the remaining input.
func Next(input string) (string, string, error) {
	if input == "" {
		return "", "", nil
	}
	advance, part, err := splitFunc[string](input, true)
	if err != nil {
		return "", "", err
	}
	if advance <= 0 || advance > len(input) || len(part) != advance {
		return "", "", errors.New("invalid grapheme boundary")
	}
	return part, input[advance:], nil
}
