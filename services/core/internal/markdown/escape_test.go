package markdown

import "testing"

func TestInlineTextRoundTrip(t *testing.T) {
	for _, value := range []string{"", " 中文 &amp; <标签> ", "A|B\\C [link](target)", "第一行\n\t第二行\r结束", "  "} {
		decoded, err := DecodeInline(EscapeInline(value))
		if err != nil || decoded != value {
			t.Fatalf("inline text changed: original=%q decoded=%q err=%v", value, decoded, err)
		}
	}
}
