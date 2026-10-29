package markdown

import (
	"bufio"
	"bytes"
	"fmt"
	"html"
	"strings"

	goldhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// EscapeInline preserves validated, NUL-free text when it is embedded in Markdown.
func EscapeInline(value string) string {
	var output strings.Builder
	output.Grow(len(value))
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\n':
			output.WriteString("&#10;")
		case '\r':
			output.WriteString("&#13;")
		case '\t':
			output.WriteString("&#9;")
		case ' ':
			if i == 0 || i == len(value)-1 {
				output.WriteString("&#32;")
			} else {
				output.WriteByte(' ')
			}
		default:
			if util.IsPunct(value[i]) {
				output.WriteByte('\\')
			}
			output.WriteByte(value[i])
		}
	}
	return output.String()
}

// DecodeInline resolves Markdown escapes and entities without interpreting HTML.
func DecodeInline(value string) (string, error) {
	var output bytes.Buffer
	writer := bufio.NewWriter(&output)
	goldhtml.DefaultWriter.Write(writer, []byte(value))
	if err := writer.Flush(); err != nil {
		return "", fmt.Errorf("decode Markdown text: %w", err)
	}
	return html.UnescapeString(output.String()), nil
}
