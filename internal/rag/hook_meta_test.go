package rag

import (
	"strings"
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/features"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm/llmtest"
)

func TestMeta(t *testing.T) {
	for _, s := range []string{"Напомни, какая у нас цель и что мы решили?", "какая цель разговора", "Что мы уже выяснили?",
		"подведи итог", "напомни цель", "На чём мы остановились?"} {
		if !Meta(s) {
			t.Errorf("Meta(%q) = false", s)
		}
	}
	for _, s := range []string{"манул", "Чем питается манул?", "напомни, сколько весит харза", "целый день спит?", "нацелен на добычу"} {
		if Meta(s) {
			t.Errorf("Meta(%q) = true", s)
		}
	}
}

// Реплика о самом разговоре при rag.cite: без kb_search кодом и без
// kb_answer, правило — по памяти задачи; в итогах хода — Meta и текст.
func TestHookCiteMeta(t *testing.T) {
	const reply = "Цель — доклад для школьников о кошках Азии; договорились: без латыни."
	r := newCiteChat(t, &Hook{Searcher: searcher(t)}, func(req llm.Request, step int) llm.Response {
		return llmtest.Text(reply)
	})
	turn := r.ask(t, "Напомни, какая у нас цель и что мы решили?", citeSet(features.RAGFilter, features.RAGRewrite))
	if len(r.lead) != 1 {
		t.Fatalf("запросов ведущего %d", len(r.lead))
	}
	req := r.lead[0]
	if llmtest.HasTool(req, FinishName) || llmtest.HasTool(req, ToolName) {
		t.Fatal("на реплике о разговоре у ведущего kb_answer или kb_search")
	}
	if !strings.Contains(req.Messages[0].Content, "о самом разговоре") {
		t.Fatal("нет правила реплики о разговоре")
	}
	for _, m := range req.Messages {
		if m.Role == llm.RoleTool {
			t.Fatal("вызов kb_search кодом на реплике о разговоре")
		}
	}
	var v CiteView
	if !turn.Extra(string(features.RAGCite), &v) || !v.Meta || v.MetaSource != MetaSource || v.Text != reply || turn.Reply != reply {
		t.Fatalf("итог хода: %+v, ответ %q", v, turn.Reply)
	}
}
