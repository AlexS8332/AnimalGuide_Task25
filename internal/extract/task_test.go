package extract

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm/llmtest"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/task"
)

// Задача пишется тем же запросом: один вызов модели на ход, секция "task"
// в том же JSON; цитаты сверяются с репликой человека.
func TestRunAppliesTask(t *testing.T) {
	const reply = `{"facts": {"set": [], "delete": []},
 "task": {"goal": {"text": "доклад для 5 класса о манулах и ирбисах", "quote": "готовлю доклад для 5 класса про манулов и ирбисов"},
  "add": [{"list": "constraints", "text": "без латыни", "quote": "без латыни"},
          {"list": "terms", "term": "барс", "meaning": "ирбис", "quote": "барсом я называю ирбиса"},
          {"list": "open", "text": "какие факты взять", "quote": "Рысь — лесная кошка"}]}}`
	var system, user string
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		system, user = req.Messages[0].Content, req.Messages[1].Content
		return llmtest.Text(reply), nil
	}}
	in := input("Я готовлю доклад для 5 класса про манулов и ирбисов, барсом я называю ирбиса. И без латыни.")
	in.Targets = Targets{Facts: true, Task: true}
	in.Task = task.State{Clarified: []task.Item{{Text: "уровень — школьники"}}, Version: 1}
	u, err := Extractor{LLM: fake, Model: llm.DefaultModel}.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if fake.Calls() != 1 {
		t.Fatalf("запросов %d — задача не должна добавлять запросов", fake.Calls())
	}
	if !strings.Contains(system, "ЗАДАЧА РАЗГОВОРА") || !strings.Contains(system, `"task": {"goal"`) {
		t.Fatal("в промпте нет раздела задачи")
	}
	if !strings.Contains(user, "=== Задача разговора этой ветки ===\nУточнено: уровень — школьники") {
		t.Fatalf("в запросе нет задачи ветки:\n%s", user)
	}
	st := u.Task
	if st.Goal != "доклад для 5 класса о манулах и ирбисах" || len(st.Constraints) != 1 || len(st.Terms) != 1 || len(st.Open) != 0 {
		t.Fatalf("задача: %+v", st)
	}
	// Пункт с цитатой из ответа справочника отклонён с причиной.
	var rejected bool
	for _, c := range u.TaskChanges {
		if c.Op == task.OpReject && c.List == task.ListOpen && c.Reason != "" {
			rejected = true
		}
	}
	if !rejected || !u.Changed() {
		t.Fatalf("правки: %+v", u.TaskChanges)
	}
	// Вход не тронут: правки — на копии.
	if in.Task.Goal != "" || in.Task.Version != 1 {
		t.Fatal("Input.Task изменён")
	}
}

// Механизм выключен: промпт извлекателя побайтно прежний, секция task в
// ответе модели игнорируется.
func TestTaskOffLeavesPromptAndState(t *testing.T) {
	off := System(Targets{Profile: true, Long: true, Work: true, Facts: true})
	if strings.Contains(off, "ЗАДАЧА РАЗГОВОРА") || strings.Contains(off, `"task"`) {
		t.Fatal("выключенная задача попала в промпт")
	}
	if !strings.HasSuffix(strings.SplitN(off, `"delete": ["ключ"]}`, 2)[1][:2], "}\n") {
		t.Fatal("пример формата сломан")
	}
	if strings.Contains(request(input("x")), "Задача разговора") {
		t.Fatal("выключенная задача попала в запрос")
	}
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		return llmtest.Text(`{"task": {"goal": {"text": "доклад", "quote": "доклад"}}}`), nil
	}}
	in := input("Готовлю доклад.")
	in.Targets = Targets{Facts: true}
	u, err := Extractor{LLM: fake}.Run(context.Background(), in)
	if err != nil || u.Task.Goal != "" || len(u.TaskChanges) != 0 {
		t.Fatalf("задача записана при выключенном механизме: %+v %v", u.Task, err)
	}
}

// Реплики, меняющие задачу, извлекатель не пропускает (Nothing → false),
// а вопросы о животном — по-прежнему пропускает.
func TestNothingKeepsTaskTurns(t *testing.T) {
	for _, s := range []string{
		"Давай теперь про ирбиса?", "А если сравнить их по весу?", "Значит, барс — это ирбис?",
		"Что подойдёт для презентации?", "Какая цель у заповедника?", "Вместо этого расскажи про ирбиса?",
		"Я имел в виду снежного барса?", "Это для 5 класса подойдёт?", "Какие виды выбрать?",
	} {
		if Nothing(s) {
			t.Errorf("%q: реплика может менять задачу — извлекатель нужен", s)
		}
	}
	for _, s := range []string{"А сколько они весят?", "Где живёт манул?", "Манул съедает мышь целиком?"} {
		if !Nothing(s) {
			t.Errorf("%q: вопрос о животном — извлекать нечего", s)
		}
	}
}
