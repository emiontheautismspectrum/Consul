package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

func TestRetriesAreBounded(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "<html>bad gateway</html>", http.StatusBadGateway)
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, "k", "m", time.Second).WithRetries(1)
	_, _, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if !errors.Is(err, ErrUpstream) || calls.Load() != 2 {
		t.Errorf("err = %v, calls = %d, want ErrUpstream after 2 calls", err, calls.Load())
	}
}
