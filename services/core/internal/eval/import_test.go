package eval

import (
	"strings"
	"testing"
)

func TestImportQuestions(t *testing.T) {
	manifest := Dataset{Version: "v1", Documents: []Document{{Key: "a", Title: "制度", Content: "申请制度"}}}
	answerable := `{"id":"q1","question":"如何申请？","expected":["a"],"answerable":true,"tags":["中文"]}`
	unanswerable := `{"id":"q2","question":"未知？","expected":[],"answerable":false,"tags":[]}`
	data, err := ImportQuestions(manifest, strings.NewReader(answerable+"\r\n"+unanswerable))
	if err != nil || len(data.Questions) != 2 || !data.Questions[0].Answerable || data.Questions[1].Answerable {
		t.Fatalf("import Chinese answerable and unanswerable questions: %+v %v", data, err)
	}
	for name, invalid := range map[string]string{
		"duplicate ID":     answerable + "\n" + answerable,
		"unknown document": strings.ReplaceAll(answerable, `"a"`, `"missing"`),
		"unknown field":    strings.Replace(answerable, `"id":`, `"extra":1,"id":`, 1),
		"trailing JSON":    answerable + `{}`,
		"truncated record": answerable + "\n{",
		"blank record":     answerable + "\n\n",
		"oversized record": strings.Repeat(" ", 1024*1024) + answerable,
	} {
		t.Run(name, func(t *testing.T) {
			partial, err := ImportQuestions(manifest, strings.NewReader(invalid))
			if err == nil || len(partial.Questions) != 0 {
				t.Fatalf("invalid import produced partial success: %+v %v", partial, err)
			}
		})
	}
}
