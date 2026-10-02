package rag

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/task"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/tools"
)

// Память задачи (v25) в поиске: при механизме task хук передаёт термины и
// цель ветки в переписывание запроса (rag.rewrite). Продолжение «а сколько
// они весят?» без вида в реплике и в окне ищет вид из цели.
func TestHookPassesTaskToRewrite(t *testing.T) {
	h := &Hook{Searcher: searcher(t), K: 3}
	search := func(spec string) SearchResult {
		t.Helper()
		tr, rec := hookTurn(t, spec, "А сколько они весят?")
		tr.Task = task.State{Goal: "доклад о мануле", Terms: []task.Term{{Term: "наш зверь", Meaning: "манул"}}, Version: 2}
		if err := h.Before(context.Background(), tr); err != nil {
			t.Fatal(err)
		}
		var kbs tools.Tool
		for _, x := range tr.Request.Tools {
			if x.Spec().Name == ToolName {
				kbs = x
			}
		}
		out, err := kbs.Call(context.Background(), json.RawMessage(`{"query":"А сколько они весят?"}`))
		if err != nil {
			t.Fatal(err)
		}
		var res SearchResult
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		if strings.Contains(spec, "+task") && !strings.Contains(rec.Events[0].Detail, "Память задачи в переписывании") {
			t.Fatalf("журнал без задачи: %s", rec.Events[0].Detail)
		}
		return res
	}
	on := search("+rag,+rag.rewrite,+task")
	if !strings.Contains(on.Rewritten, "манул") {
		t.Fatalf("вид из цели не дошёл до поиска: %+v", on)
	}
	// Механизм выключен — поиск прежний: задача ветки не трогает запрос.
	if off := search("+rag,+rag.rewrite"); strings.Contains(off.Rewritten, "манул") {
		t.Fatalf("задача дошла до поиска при выключенном механизме: %q", off.Rewritten)
	}
}
