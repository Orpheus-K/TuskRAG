package answer

import (
	"strconv"
	"strings"
)

// Buffer only a potential numeric marker, including markers split across deltas.
type citationFilter struct {
	pending string
	limit   int
	used    map[int]bool
	invalid int
}

func newCitationFilter(limit int) *citationFilter {
	return &citationFilter{limit: limit, used: map[int]bool{}}
}
func (f *citationFilter) Push(text string, final bool) string {
	var out strings.Builder
	for _, r := range text {
		if f.pending == "" {
			if r == '[' {
				f.pending = "["
			} else {
				out.WriteRune(r)
			}
			continue
		}
		if r >= '0' && r <= '9' {
			f.pending += string(r)
			if len(f.pending) > 16 {
				f.invalid++
				f.pending = ""
			}
			continue
		}
		if r == ']' && len(f.pending) > 1 {
			n, err := strconv.Atoi(f.pending[1:])
			if err == nil && n >= 1 && n <= f.limit {
				out.WriteString("[" + strconv.Itoa(n) + "]")
				f.used[n] = true
			} else {
				f.invalid++
			}
			f.pending = ""
			continue
		}
		out.WriteString(f.pending)
		f.pending = ""
		if r == '[' {
			f.pending = "["
		} else {
			out.WriteRune(r)
		}
	}
	if final && f.pending != "" {
		if len(f.pending) > 1 {
			f.invalid++
		} else {
			out.WriteString(f.pending)
		}
		f.pending = ""
	}
	return out.String()
}
