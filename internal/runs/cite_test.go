package runs

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/agent"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/agents"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/features"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/history"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm/llmtest"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/tools"
)

// citeTurn — ход справочной по базе в истории: реплика, вызов kb_search
// кодом и его выдача (длинная), вызов kb_answer и его приём.
func citeTurn(i int) []llm.Message {
	id := string(rune('a' + i))
	hits := `{"hits":[{"chunk_id":"manul/structure/001","text":"` + strings.Repeat("манул живёт в степях ", 150) + `"}]}`
	return []llm.Message{
		{Role: llm.RoleUser, Content: "вопрос " + id},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "s" + id, Type: "function", Function: llm.FunctionCall{Name: "kb_search", Arguments: `{"query":"q"}`}}}},
		{Role: llm.RoleTool, ToolCallID: "s" + id, Content: tools.Envelope("kb_search", hits)},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "f" + id, Type: "function", Function: llm.FunctionCall{Name: "kb_answer", Arguments: `{"status":"answered","answer":"Манул живёт в степях."}`}}}},
		{Role: llm.RoleTool, ToolCallID: "f" + id, Content: `{"accepted":true}`},
	}
}

// v25: у диалога с rag.cite окно 12 сообщений (два полных хода по пять),
// у прочих — прежние 8; выдача kb_search прошлых ходов сокращена compact.
func TestCiteWindowAndCompact(t *testing.T) {
	r := newRig(t)
	var msgs []llm.Message
	for i := range 4 {
		msgs = append(msgs, citeTurn(i)...)
	}
	reg := features.Catalog()
	cite := reg.Defaults().With(features.RAG, true).With(features.RAGCite, true)
	prep := func(m *Manager, fs features.Set) []llm.Message {
		c := history.New(llm.DefaultModel, nil, fs)
		turn := &Turn{Conv: c, Branch: c.Active, History: msgs, Features: fs, Em: agent.Nop{}}
		m.prepare(turn)
		return turn.Request.Window
	}
	w := prep(r.m, cite)
	if len(w) != 10 || w[0].Role != llm.RoleUser || w[0].Content != "вопрос c" {
		t.Fatalf("окно rag.cite: %d сообщений, первое %+v", len(w), w[0])
	}
	limit := history.DefaultKeepToolRunes + 400 // сокращённые данные + пометка и обёртка источника (без сокращения — больше 3000)
	for _, m := range w {
		if m.Role == llm.RoleTool && utf8.RuneCountInString(m.Content) > limit {
			t.Fatalf("выдача kb_search прошлого хода не сокращена: %d символов", utf8.RuneCountInString(m.Content))
		}
	}
	if total := history.Runes(w); total > 2*(limit+300) {
		t.Fatalf("окно раздуто: %d символов", total)
	}
	// Без rag.cite — прежние 8: один полный ход.
	if w := prep(r.m, reg.Defaults()); len(w) != 5 {
		t.Fatalf("окно без rag.cite: %d сообщений", len(w))
	}
	// Своё окно (-window задан явно) — и для rag.cite.
	m := r.manager()
	m.cfg.CiteWindow = 6
	if w := prep(m, cite); len(w) != 5 {
		t.Fatalf("окно rag.cite из -window: %d сообщений", len(w))
	}
}

// «Сравни» при rag.cite — ход ведущего в той же ветке, без развилки
// «до сравнения».
func TestCiteCompareStaysInLead(t *testing.T) {
	r := newRig(t)
	r.brain.LeadScript = func(req llm.Request, step int) llm.Response { return llmtest.Text("ответ") }
	cite := features.Catalog().Defaults().With(features.RAG, true).With(features.RAGCite, true)
	d, err := r.m.Create(StartOptions{Features: cite})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"манул", "сравни харзу и соболя по массе"} {
		s, err := r.m.Send(d.ID, agents.Request{Text: text})
		if err != nil {
			t.Fatal(err)
		}
		if v := wait(t, s); v.Route != agents.RouteLead || v.Kind != agents.KindMessage {
			t.Fatalf("%q: маршрут %s, вид %s", text, v.Route, v.Kind)
		}
	}
	got, _ := r.m.Get(d.ID)
	if len(got.BranchTree) != 1 || len(got.Checkpoints) != 0 {
		t.Fatalf("ветки: %d, точки: %d", len(got.BranchTree), len(got.Checkpoints))
	}
}
