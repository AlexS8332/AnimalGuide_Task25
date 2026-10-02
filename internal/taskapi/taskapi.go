// Package taskapi — REST панели «Задача» (v25): память задачи текущей
// ветки диалога руками. Пишет её извлекатель по словам человека
// (internal/persona), а здесь человек правит сам — без цитат: он и есть
// источник (ФТ-18: раскладку предлагает извлекатель, правит человек).
//
// Отдельный пакет, а не persona: адрес задачи — диалог и его текущая
// ветка, а не человек; ручки нужен runs.Manager (правка вне хода под его
// замком), как окну подборок (compiler.Extension).
//
// Маршруты (conv — идентификатор диалога):
//
//	GET    /api/task/{conv} — задача текущей ветки и её блок
//	PUT    /api/task/{conv} — заменить целиком (тело — task.State)
//	POST   /api/task/{conv} — одна правка {op, list, text, term, meaning}
//	DELETE /api/task/{conv} — очистить
//
// Ответ всех четырёх — View. Во время хода правка — 409 (runs.ErrBusy):
// ход записал бы поверх свой снимок.
package taskapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/features"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/paths"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/runs"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/server"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/task"
)

// Prefix — корень маршрутов.
const Prefix = "/api/task/"

// Extension — маршруты панели «Задача».
func Extension(m *runs.Manager) []server.Extension {
	return []server.Extension{{Prefix: Prefix, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handle(w, r, m) })}}
}

// Limits — пределы для подсказок панели.
type Limits struct {
	Items int `json:"items"`
	Runes int `json:"runes"`
	Goal  int `json:"goal"`
}

// View — ответ: задача текущей ветки, её блок в том виде, в каком его
// получает ведущий, и включён ли механизм (выключен — задача хранится и
// правится, но в запрос не уходит).
type View struct {
	Conversation string     `json:"conversation"`
	Branch       string     `json:"branch"`
	Task         task.State `json:"task"`
	Block        string     `json:"block"`
	On           bool       `json:"on"`
	Limits       Limits     `json:"limits"`
}

// Edit — одна правка руками.
type Edit struct {
	Op      string `json:"op"` // set_goal | add | remove | clear
	List    string `json:"list,omitempty"`
	Text    string `json:"text,omitempty"`
	Term    string `json:"term,omitempty"`
	Meaning string `json:"meaning,omitempty"`
}

func handle(w http.ResponseWriter, r *http.Request, m *runs.Manager) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, Prefix), "/")
	if !paths.ValidHex(id) {
		server.WriteError(w, http.StatusBadRequest, "некорректный идентификатор диалога")
		return
	}
	var (
		d   runs.Detail
		err error
	)
	switch r.Method {
	case http.MethodGet:
		var ok bool
		if d, ok = m.Get(id); !ok {
			err = runs.ErrNotFound
		}
	case http.MethodPut:
		var body task.State
		if !server.ReadJSON(w, r, &body) {
			return
		}
		d, err = m.EditTask(id, func(s *task.State) error {
			body.Version = s.Version
			*s = body
			return nil
		})
	case http.MethodPost:
		var body Edit
		if !server.ReadJSON(w, r, &body) {
			return
		}
		d, err = m.EditTask(id, func(s *task.State) error {
			return s.Manual(body.Op, task.PatchItem{List: body.List, Text: body.Text, Term: body.Term, Meaning: body.Meaning})
		})
	case http.MethodDelete:
		d, err = m.EditTask(id, func(s *task.State) error { return s.Manual(task.ManualClear, task.PatchItem{}) })
	default:
		server.WriteError(w, http.StatusMethodNotAllowed, "нужен GET, PUT, POST или DELETE")
		return
	}
	if err != nil {
		server.WriteError(w, status(err), err.Error())
		return
	}
	server.WriteJSON(w, http.StatusOK, ViewOf(d))
}

// ViewOf — задача из диалога.
func ViewOf(d runs.Detail) View {
	return View{Conversation: d.ID, Branch: d.Branch, Task: d.Task, Block: d.Task.Block(), On: d.Features.On(features.Task),
		Limits: Limits{Items: task.MaxItems, Runes: task.MaxRunes, Goal: task.MaxGoal}}
}

func status(err error) int {
	switch {
	case errors.Is(err, runs.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, runs.ErrBusy):
		return http.StatusConflict
	}
	return http.StatusBadRequest
}
