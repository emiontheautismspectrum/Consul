package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageMarshalPlainAndMultimodal(t *testing.T) {
	b, _ := json.Marshal(Message{Role: "user", Content: "hi"})
	if string(b) != `{"role":"user","content":"hi"}` {
		t.Errorf("plain = %s", b)
	}
	b, _ = json.Marshal(Message{Role: "user", Parts: []ContentPart{TextPart("read"), ImagePart("image/png", []byte{1, 2, 3})}})
	want := `{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AQID"}}]}`
	if string(b) != want {
		t.Errorf("multimodal =\n%s\nwant\n%s", b, want)
	}
}

func TestExtractJSON(t *testing.T) {
	for _, in := range []string{"```json\n{\"a\":1}\n```", "Here: {\"a\":1} done", `{"a":1}`} {
		if got := ExtractJSON(in); !strings.HasPrefix(got, "{") || !json.Valid([]byte(got)) {
			t.Errorf("ExtractJSON(%q) = %q", in, got)
		}
	}
}
