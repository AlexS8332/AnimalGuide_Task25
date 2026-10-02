package taskapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/agent"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/agents"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/agents/agentstest"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/features"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/history"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm/llmtest"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/runs"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/store"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/task"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/tools"
)

// rig — менеджер ходов с подставной моделью и маршруты панели. Ведущий
// ждёт release: пока канал не закрыт, ход идёт (проверка 409 во время хода).
type rig struct {
	m       *runs.Manager
	mux     *http.ServeMux
	conv    string
	release chan struct{}
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{release: make(chan struct{})}
	brain := &agentstest.Brain{LeadScript: func(req llm.Request, step int) llm.Response {
		<-r.release
		return llmtest.Text("Манул живёт в степях.")
	}}
	fake := &llmtest.Fake{Fn: brain.Chat}
	reg := features.Catalog()
	r.m = runs.NewManager(runs.Config{
		Agents: agents.Deps{Runner: agent.Runner{LLM: fake, Model: llm.DefaultModel}, Features: reg,
			Sources: agents.Local{Registry: tools.MustRegistry()}},
		Store: history.NewStore(store.NewDir(t.TempDir())), Registry: reg, Defaults: reg.Defaults(), Timeout: time.Minute,
	})
	d, err := r.m.Create(runs.StartOptions{Features: reg.Defaults().With(features.Task, true)})
	if err != nil {
		t.Fatal(err)
	}
	r.conv = d.ID
	r.mux = http.NewServeMux()
	for _, e := range Extension(r.m) {
		r.mux.Handle(e.Prefix, e.Handler)
	}
	return r
}

func (r *rig) call(method, conv, body string) (int, View, string) {
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, httptest.NewRequest(method, Prefix+conv, bytes.NewBufferString(body)))
	var v View
	json.Unmarshal(rec.Body.Bytes(), &v)
	return rec.Code, v, rec.Body.String()
}

func TestGetPutPostDelete(t *testing.T) {
	r := newRig(t)
	code, v, _ := r.call(http.MethodGet, r.conv, "")
	if code != 200 || !v.On || v.Conversation != r.conv || v.Branch == "" || v.Block != "" || v.Task.Version != 0 ||
		v.Limits != (Limits{Items: task.MaxItems, Runes: task.MaxRunes, Goal: task.MaxGoal}) {
		t.Fatalf("GET: %d %+v", code, v)
	}
	// PUT с текущей версией: состояние почищено, версия +1, блок собран.
	code, v, _ = r.call(http.MethodPut, r.conv, `{"goal":" доклад о манулах ","constraints":[{"text":"без латыни"},{"text":"Без латыни"}],"version":0}`)
	if code != 200 || v.Task.Goal != "доклад о манулах" || len(v.Task.Constraints) != 1 || v.Task.Version != 1 ||
		!strings.Contains(v.Block, "Ограничения: без латыни") {
		t.Fatalf("PUT: %d %+v", code, v)
	}
	// Одна правка.
	code, v, _ = r.call(http.MethodPost, r.conv, `{"op":"add","list":"terms","text":"барс = ирбис"}`)
	if code != 200 || len(v.Task.Terms) != 1 || v.Task.Version != 2 {
		t.Fatalf("POST: %d %+v", code, v)
	}
	if code, _, body := r.call(http.MethodPost, r.conv, `{"op":"fly"}`); code != 400 || !strings.Contains(body, "неизвестное действие") {
		t.Fatalf("POST неизвестное: %d %s", code, body)
	}
	if code, _, _ := r.call(http.MethodPost, r.conv, `{not json`); code != 400 {
		t.Fatalf("POST не JSON: %d", code)
	}
	code, v, _ = r.call(http.MethodDelete, r.conv, "")
	if code != 200 || !v.Task.Empty() || v.Block != "" || v.Task.Version != 3 {
		t.Fatalf("DELETE: %d %+v", code, v)
	}
	if code, _, _ := r.call(http.MethodPatch, r.conv, ""); code != http.StatusMethodNotAllowed {
		t.Fatalf("PATCH: %d", code)
	}
}

func TestNotFoundAndBadID(t *testing.T) {
	r := newRig(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPost, http.MethodDelete} {
		body := `{"op":"clear"}`
		if method == http.MethodPut {
			body = `{"version":0}`
		}
		if code, _, _ := r.call(method, "0123456789abcdef", body); code != 404 {
			t.Fatalf("%s чужого диалога: %d", method, code)
		}
	}
	if code, _, _ := r.call(http.MethodGet, "not-hex", ""); code != 400 {
		t.Fatalf("плохой идентификатор: %d", code)
	}
}

// PUT с версией, которая уже не текущая (форму открыли до хода или правки
// в другой вкладке), — 409 «задача изменилась, обновите», состояние не
// тронуто.
func TestPutStaleVersion(t *testing.T) {
	r := newRig(t)
	if code, _, _ := r.call(http.MethodPut, r.conv, `{"goal":"доклад","version":0}`); code != 200 {
		t.Fatalf("первый PUT: %d", code)
	}
	code, _, body := r.call(http.MethodPut, r.conv, `{"goal":"совсем другое","version":0}`)
	if code != http.StatusConflict || !strings.Contains(body, "задача изменилась, обновите") {
		t.Fatalf("устаревшая версия: %d %s", code, body)
	}
	if _, v, _ := r.call(http.MethodGet, r.conv, ""); v.Task.Goal != "доклад" || v.Task.Version != 1 {
		t.Fatalf("состояние тронуто: %+v", v.Task)
	}
	// Версия из будущего — тоже не текущая.
	if code, _, _ := r.call(http.MethodPut, r.conv, `{"goal":"x","version":7}`); code != http.StatusConflict {
		t.Fatalf("версия из будущего: %d", code)
	}
}

// Во время хода любая правка — 409: ход записал бы поверх свой снимок.
func TestConflictDuringTurn(t *testing.T) {
	r := newRig(t)
	s, err := r.m.Send(r.conv, agents.Request{Text: "Где живёт манул?"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, body string }{
		{http.MethodPut, `{"goal":"доклад","version":0}`},
		{http.MethodPost, `{"op":"set_goal","text":"доклад"}`},
		{http.MethodDelete, ""},
	} {
		if code, _, body := r.call(c.method, r.conv, c.body); code != http.StatusConflict || !strings.Contains(body, "ещё отвечает") {
			t.Fatalf("%s во время хода: %d %s", c.method, code, body)
		}
	}
	// Чтение во время хода можно.
	if code, _, _ := r.call(http.MethodGet, r.conv, ""); code != 200 {
		t.Fatalf("GET во время хода: %d", code)
	}
	close(r.release)
	if v := s.Wait(10 * time.Second); v.Status == runs.StatusRunning {
		t.Fatal("ход не закончился")
	}
	if code, _, _ := r.call(http.MethodPost, r.conv, `{"op":"set_goal","text":"доклад"}`); code != 200 {
		t.Fatalf("после хода: %d", code)
	}
}
