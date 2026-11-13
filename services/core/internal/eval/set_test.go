package eval

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func validSet() SetDefinition {
	return SetDefinition{Name: "知识库验收", Cases: []CaseDefinition{
		{ID: "q1", Question: "申请条件？", Answerable: true, Expected: []ExpectedLocation{{DocumentID: 1, SectionID: 2, Page: 3}, {DocumentID: 4}}, ReferenceAnswer: "按制度申请。", Tags: []string{"跨文档"}},
		{ID: "q2", Question: "未记载内容？", Answerable: false, Expected: []ExpectedLocation{}, Tags: []string{"无答案"}},
	}}
}

func TestSetRoundTrip(t *testing.T) {
	want := validSet()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeSet(strings.NewReader(string(data)))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestSetRejectsInvalidDefinitions(t *testing.T) {
	for name, change := range map[string]func(*SetDefinition){
		"blank name":            func(s *SetDefinition) { s.Name = "  " },
		"empty cases":           func(s *SetDefinition) { s.Cases = nil },
		"too many cases":        func(s *SetDefinition) { s.Cases = make([]CaseDefinition, 10001) },
		"duplicate ID":          func(s *SetDefinition) { s.Cases[1].ID = s.Cases[0].ID },
		"blank question":        func(s *SetDefinition) { s.Cases[0].Question = "\t" },
		"query byte limit":      func(s *SetDefinition) { s.Cases[0].Question = strings.Repeat("中", 1366) },
		"NUL query":             func(s *SetDefinition) { s.Cases[0].Question = "x\x00y" },
		"bad UTF8":              func(s *SetDefinition) { s.Cases[0].Question = string([]byte{255}) },
		"negative document":     func(s *SetDefinition) { s.Cases[0].Expected[0].DocumentID = -1 },
		"negative section":      func(s *SetDefinition) { s.Cases[0].Expected[0].SectionID = -1 },
		"negative page":         func(s *SetDefinition) { s.Cases[0].Expected[0].Page = -1 },
		"duplicate target":      func(s *SetDefinition) { s.Cases[0].Expected[1] = s.Cases[0].Expected[0] },
		"no positive targets":   func(s *SetDefinition) { s.Cases[0].Expected = nil },
		"negative with targets": func(s *SetDefinition) { s.Cases[0].Answerable = false },
		"duplicate tag":         func(s *SetDefinition) { s.Cases[0].Tags = []string{"表格", "表格"} },
		"blank tag":             func(s *SetDefinition) { s.Cases[0].Tags = []string{" "} },
		"reference size":        func(s *SetDefinition) { s.Cases[0].ReferenceAnswer = strings.Repeat("a", 32769) },
		"total size": func(s *SetDefinition) {
			for i := range 300 {
				c := s.Cases[0]
				c.ID = strings.Repeat("a", i/100+1) + string(rune(0x4e00+i))
				c.ReferenceAnswer = strings.Repeat("a", 32768)
				s.Cases = append(s.Cases, c)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			set := validSet()
			change(&set)
			if !errors.Is(ValidateSet(set), ErrInvalidCase) {
				t.Fatal("invalid set accepted")
			}
		})
	}
}

func TestDecodeSetRejectsMalformedInput(t *testing.T) {
	data, err := json.Marshal(validSet())
	if err != nil {
		t.Fatal(err)
	}
	valid := string(data)
	for name, input := range map[string]string{
		"trailing":              valid + " {}",
		"truncated":             valid[:len(valid)-1],
		"unknown field":         strings.Replace(valid, `"name":`, `"unknown":1,"name":`, 1),
		"unknown case field":    strings.Replace(valid, `"question":`, `"unknown":1,"question":`, 1),
		"missing answerability": strings.ReplaceAll(valid, `"answerable":false,`, ""),
		"null answerability":    strings.ReplaceAll(valid, `"answerable":false`, `"answerable":null`),
		"invalid UTF8":          valid + string([]byte{255}),
		"too large":             valid + strings.Repeat(" ", MaxSetBytes),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeSet(strings.NewReader(input))
			if err == nil || len(got.Cases) != 0 {
				t.Fatalf("partial success: %+v %v", got, err)
			}
		})
	}
}
