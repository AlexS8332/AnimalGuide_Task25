//go:build edge

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/agents"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/runs"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/task"
)

// edgeTask — подставной REST памяти задачи (v25) по пути адаптера taskAPI
// в web/app.js: GET /api/task/{id} → State (как internal/taskapi), PUT State →
// State (версия +1; версия клиента не текущая — 409). Хранит в памяти; незнакомый диалог — пустое
// состояние. puts — тела PUT, чтобы сценарий сверил, что ушло на сервер.
type edgeTask struct {
	mu     sync.Mutex
	states map[string]task.State
	puts   []task.State
}

func newEdgeTask() *edgeTask { return &edgeTask{states: map[string]task.State{}} }

var edgeTaskPath = regexp.MustCompile(`^/api/task/([0-9a-f]+)$`)

// serve отвечает на путь памяти задачи; иначе false — запрос уходит
// приложению.
func (e *edgeTask) serve(w http.ResponseWriter, r *http.Request) bool {
	m := edgeTaskPath.FindStringSubmatch(r.URL.Path)
	if m == nil {
		return false
	}
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		reply(http.StatusOK, e.states[m[1]])
	case http.MethodPut:
		var st task.State
		if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
			reply(http.StatusBadRequest, map[string]string{"error": err.Error()})
			return true
		}
		e.puts = append(e.puts, st)
		// Как internal/taskapi: версия клиента не текущая — 409.
		if cur := e.states[m[1]].Version; st.Version != cur {
			reply(http.StatusConflict, map[string]string{"error": "задача изменилась, обновите: у вас версия " +
				strconv.Itoa(st.Version) + ", на сервере " + strconv.Itoa(cur)})
			return true
		}
		st.Version = e.states[m[1]].Version + 1
		e.states[m[1]] = st
		reply(http.StatusOK, st)
	default:
		reply(http.StatusMethodNotAllowed, map[string]string{"error": "метод не поддерживается"})
	}
	return true
}

// edgeTaskReply — реплика, на которой «извлекатель» правит задачу: хук
// edgeTaskHook кладёт в ход изменения, как это сделает механизм task.
const edgeTaskReply = "Готовлю доклад для школьников о кошках Азии. Без латыни, не больше пяти предложений. Барс — это ирбис."

// edgeTaskHook — вместо извлекателя S1: изменения задачи за ход в extras
// хода под ключом task (список task.Change), с отклонённым пунктом и
// разметкой в тексте — она должна остаться буквами.
type edgeTaskHook struct{}

func (edgeTaskHook) Name() string                             { return "edge-task" }
func (edgeTaskHook) Before(context.Context, *runs.Turn) error { return nil }
func (edgeTaskHook) After(_ context.Context, t *runs.Turn) error {
	if !strings.Contains(t.Request.Text, "доклад для школьников") {
		return nil
	}
	t.Extra(task.BlockName, []task.Change{
		{Op: "set_goal", List: "goal", Text: "доклад для школьников о кошках Азии", Old: "рассказ о манулах"},
		{Op: "add", List: "constraints", Text: "без латыни"},
		{Op: "add", List: "constraints", Text: "не больше пяти предложений"},
		{Op: "add", List: "terms", Text: "барс = ирбис"},
		{Op: "remove", List: "open", Text: "для какого класса"},
		{Op: "reject", List: "clarified", Text: `<img src=x onerror="window.__xss=25">только Азия`, Reason: "нет цитаты в реплике человека"},
	})
	return nil
}

// edgeTaskState — состояние задачи подставного диалога: цель, два
// ограничения, термин, открытый вопрос (с разметкой — буквами).
func edgeTaskState() task.State {
	return task.State{
		Goal: "доклад для школьников о кошках Азии", GoalQuote: "Готовлю доклад для школьников о кошках Азии", GoalTurn: 1,
		Constraints: []task.Item{
			{Text: "без латыни", Quote: "Без латыни", Turn: 1},
			{Text: "не больше пяти предложений", Quote: "не больше пяти предложений", Turn: 1},
		},
		Terms:   []task.Term{{Term: "барс", Meaning: "ирбис", Quote: "Барс — это ирбис", Turn: 1}},
		Open:    []task.Item{{Text: `<b>какие</b> виды взять`, Quote: "что ещё не решено", Turn: 1}},
		Version: 4,
	}
}

// seedTask заводит диалог с памятью задачи: механизм task включён, один ход
// с изменениями задачи, состояние — в подставном REST.
func (a *edgeApp) seedTask(t *testing.T) {
	t.Helper()
	fs, err := a.m.Registry().Parse("+task", a.m.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.m.Create(runs.StartOptions{Features: fs, Title: "доклад о кошках"})
	if err != nil {
		t.Fatal(err)
	}
	a.taskConv = d.ID
	a.turn(t, a.taskConv, agents.Request{Text: edgeTaskReply})
	a.task.set(a.taskConv, edgeTaskState())
}

func (e *edgeTask) set(conv string, st task.State) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.states[conv] = st
}
