package history

import (
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm"
)

func call(id, name string) llm.ToolCall {
	return llm.ToolCall{ID: id, Function: llm.FunctionCall{Name: name, Arguments: "{}"}}
}

// Ход с одним отказом kb_answer (как A-4 живого прогона): пара «вызов —
// ошибка» уходит из истории, принятый вызов и kb_search остаются, история
// валидна (у каждого ответа инструмента есть вызов).
func TestDropRejected(t *testing.T) {
	ms := []llm.Message{
		{Role: llm.RoleUser, Content: "Где обитает барс?"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call("s1", "kb_search")}},
		{Role: llm.RoleTool, ToolCallID: "s1", Content: `{"error":"база недоступна"}`},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call("a1", "kb_answer")}},
		{Role: llm.RoleTool, ToolCallID: "a1", Content: `{"error":"ответ не принят: чисел ответа 13 нет ни в одной цитате"}`},
		{Role: llm.RoleAssistant, Content: "Поправлю.", ToolCalls: []llm.ToolCall{call("a2", "kb_answer"), call("a3", "kb_answer")}},
		{Role: llm.RoleTool, ToolCallID: "a2", Content: `{"accepted":true}`},
		{Role: llm.RoleTool, ToolCallID: "a3", Content: `{"error":"результат уже принят этим же ответом"}`},
	}
	got := DropRejected(ms, "kb_answer")
	if len(got) != 5 {
		t.Fatalf("сообщений %d: %+v", len(got), got)
	}
	if got[2].Content != `{"error":"база недоступна"}` {
		t.Fatal("ошибка обычного инструмента удалена")
	}
	if len(got[3].ToolCalls) != 1 || got[3].ToolCalls[0].ID != "a2" || got[3].Content != "Поправлю." || got[4].ToolCallID != "a2" {
		t.Fatalf("принятый вызов: %+v", got[3:])
	}
	if len(ms[5].ToolCalls) != 2 {
		t.Fatal("вход изменён")
	}
	// Валидность: у каждого ответа инструмента — вызов выше, у каждого
	// вызова — ответ.
	calls, answers := map[string]bool{}, map[string]bool{}
	for _, m := range got {
		for _, c := range m.ToolCalls {
			calls[c.ID] = true
		}
		if m.Role == llm.RoleTool {
			if !calls[m.ToolCallID] {
				t.Fatalf("ответ без вызова: %s", m.ToolCallID)
			}
			answers[m.ToolCallID] = true
		}
	}
	for id := range calls {
		if !answers[id] {
			t.Fatalf("вызов без ответа: %s", id)
		}
	}
	// Без отказов и без имён — та же история.
	if out := DropRejected(got, "kb_answer"); len(out) != len(got) {
		t.Fatal("повторный проход что-то удалил")
	}
	if out := DropRejected(ms); len(out) != len(ms) {
		t.Fatal("без имён удалено")
	}
}
