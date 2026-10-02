package rag

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/features"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm/llmtest"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/retrieve"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/tools"
)

func TestMeta(t *testing.T) {
	for _, s := range []string{"Напомни, какая у нас цель и что мы решили?", "какая цель разговора", "Что мы уже выяснили?",
		"подведи итог", "напомни цель", "На чём мы остановились?", "Напомни, какие ограничения мы приняли?", "Что мы решили по формату?",
		"Напомни, о чём мы говорили?", "Какая у нас цель?", "Итак, какая была задача?"} {
		if !Meta(s) {
			t.Errorf("Meta(%q) = false", s)
		}
	}
	for _, s := range []string{"манул", "Чем питается манул?", "напомни, сколько весит харза", "целый день спит?", "нацелен на добычу",
		// Ложные срабатывания из проверки v25: «цель» без признака разговора.
		"С какой целью ирбис метит территорию?", "Какая цель у заповедника для манулов?", "Что является целью охоты волка?",
		"Какие цели у программы реинтродукции переднеазиатского леопарда?", "Почему у тигра цель — крупная добыча?",
		// Смешанные: вопрос о животном в той же реплике.
		"Напомни цель и скажи, сколько весит манул", "Что мы уже выяснили о питании манула? И сколько он весит?",
		"Что мы знаем о манулах?"} {
		if Meta(s) {
			t.Errorf("Meta(%q) = true", s)
		}
	}
}

// Контрольные реплики сценариев A и B — о разговоре и со словарём
// названий; вопросы о животных сценариев — нет; вид корпуса в реплике о
// разговоре делает её вопросом о животном.
func TestMetaWithNames(t *testing.T) {
	names, err := (&retrieve.Pipeline{Searcher: searcher(t)}).Names(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "eval", "dialogs", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var sc struct {
			Turns []struct {
				Text  string   `json:"text"`
				Marks []string `json:"marks"`
			} `json:"turns"`
		}
		if err := json.Unmarshal(raw, &sc); err != nil {
			t.Fatal(err)
		}
		controls := 0
		for _, tr := range sc.Turns {
			control := slices.Contains(tr.Marks, "goal") // dialogs.MarkGoal (dialogs импортирует rag)
			if control {
				controls++
			}
			if got := MetaWith(tr.Text, names); got != control {
				t.Errorf("%s: MetaWith(%q) = %v", name, tr.Text, got)
			}
		}
		if controls < 3 {
			t.Errorf("%s: контрольных реплик %d", name, controls)
		}
	}
	for _, s := range []string{"Что мы решили про манула?", "Напомни, что мы выяснили о харзе"} {
		if !Meta(s) || MetaWith(s, names) {
			t.Errorf("%q: Meta %v, MetaWith %v", s, Meta(s), MetaWith(s, names))
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
	for _, name := range append([]string{"open_card", "read_card_section"}, tools.SourceTools...) {
		if llmtest.HasTool(req, name) {
			t.Fatalf("на реплике о разговоре у ведущего %s", name)
		}
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
