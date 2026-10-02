package persona

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/features"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/runs"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/task"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/taskapi"
)

const taskReply = `{"profile":{"set":[]},"memory":{"set":[]},"facts":{"set":[]},
 "task":{"goal":{"text":"доклад для 5 класса о манулах","quote":"готовлю доклад для 5 класса о манулах"},
  "add":[{"list":"constraints","text":"без латыни","quote":"без латыни"},
         {"list":"open","text":"выбрать питомца","quote":"Ты угадал: рыси едят зайцев"}]}}`

// taskBlock — текст блока задачи в запросе ведущего ("" — блока нет).
func taskBlock(req llm.Request) string {
	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem && strings.HasPrefix(m.Content, "Задача разговора (ведёт код") {
			return m.Content
		}
	}
	return ""
}

// Механизм task: извлекатель пишет задачу тем же запросом, блок уходит
// ведущему в этом же ходе (уже с правкой), чипы — в Extras["task"];
// выключен — ни блока, ни раздела в промпте извлекателя.
func TestTaskWrittenByExtractorAndSentToLead(t *testing.T) {
	r := newRig(t)
	var extractSys []string
	r.brain.Extract = func(req llm.Request) (string, error) {
		extractSys = append(extractSys, req.Messages[0].Content)
		return taskReply, nil
	}
	on := features.Catalog().Defaults().With(features.Task, true)
	v, d := r.turn(t, "", "Готовлю доклад для 5 класса о манулах. И без латыни, пожалуйста: где живёт манул?", on)
	if v.Status != runs.StatusDone {
		t.Fatalf("ход: %+v", v)
	}
	if d.Task.Goal != "доклад для 5 класса о манулах" || len(d.Task.Constraints) != 1 || len(d.Task.Open) != 0 {
		t.Fatalf("задача ветки: %+v", d.Task)
	}
	if r.brain.Calls("extract") != 1 || d.Meter.Calls != 1 {
		t.Fatalf("задача добавила запросов: extract %d, meter %+v", r.brain.Calls("extract"), d.Meter)
	}
	block := taskBlock(r.leadReqs[0])
	if !strings.Contains(block, "Цель: доклад для 5 класса о манулах") || !strings.Contains(block, "Ограничения: без латыни") {
		t.Fatalf("блок задачи этого хода:\n%s", block)
	}
	var changes []task.Change
	if !d.TurnList[0].Extra("task", &changes) || len(changes) != 3 || changes[0].Op != task.OpSetGoal || changes[2].Op != task.OpReject {
		t.Fatalf("чипы задачи: %+v", changes)
	}
	if !strings.Contains(extractSys[0], "ЗАДАЧА РАЗГОВОРА") {
		t.Fatal("в промпте извлекателя нет раздела задачи")
	}

	// Выключили механизм — блока нет, раздел из промпта извлекателя ушёл,
	// а задача ветки хранится как была.
	if _, err := r.m.SetFeature(v.ConversationID, features.Task, false); err != nil {
		t.Fatal(err)
	}
	_, d = r.turn(t, v.ConversationID, "Мне для урока: а чем он питается?", features.Set{})
	if b := taskBlock(r.leadReqs[len(r.leadReqs)-1]); b != "" {
		t.Fatalf("выключенный механизм дал блок:\n%s", b)
	}
	if strings.Contains(extractSys[len(extractSys)-1], "ЗАДАЧА РАЗГОВОРА") {
		t.Fatal("выключенная задача в промпте извлекателя")
	}
	if d.Task.Goal == "" {
		t.Fatal("задача ветки пропала при выключении механизма")
	}
}

// Панель «Задача»: GET/PUT/POST/DELETE текущей ветки; ручная правка
// уходит ведущему следующим ходом.
func TestTaskAPI(t *testing.T) {
	r := newRig(t)
	on := features.Catalog().Defaults().With(features.Task, true)
	d, err := r.m.Create(runs.StartOptions{Features: on})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, e := range taskapi.Extension(r.m) {
		mux.Handle(e.Prefix, e.Handler)
	}
	call := func(method, body string) (int, taskapi.View) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/task/"+d.ID, bytes.NewBufferString(body)))
		var v taskapi.View
		json.Unmarshal(rec.Body.Bytes(), &v)
		return rec.Code, v
	}
	if code, v := call(http.MethodGet, ""); code != 200 || !v.On || v.Block != "" || v.Limits.Items != task.MaxItems {
		t.Fatalf("GET пустой: %d %+v", code, v)
	}
	code, v := call(http.MethodPut, `{"goal":" сравнить манула и ирбиса ","constraints":[{"text":"без латыни"},{"text":"Без латыни"}],"terms":[{"term":"барс","meaning":"ирбис"}],"version":99}`)
	if code != 200 || v.Task.Goal != "сравнить манула и ирбиса" || len(v.Task.Constraints) != 1 || v.Task.Version != 1 || !strings.Contains(v.Block, "«барс» = ирбис") {
		t.Fatalf("PUT: %d %+v", code, v)
	}
	code, v = call(http.MethodPost, `{"op":"add","list":"open","text":"кто тяжелее"}`)
	if code != 200 || len(v.Task.Open) != 1 || v.Task.Version != 2 {
		t.Fatalf("POST add: %d %+v", code, v)
	}
	if code, _ := call(http.MethodPost, `{"op":"remove","list":"open","text":"нет такого"}`); code != 400 {
		t.Fatalf("POST remove несуществующего: %d", code)
	}
	if code, _ := call(http.MethodPost, `{"op":"fly"}`); code != 400 {
		t.Fatalf("неизвестное действие: %d", code)
	}
	_, after := r.turn(t, d.ID, "А сколько они весят?", features.Set{})
	if b := taskBlock(r.leadReqs[len(r.leadReqs)-1]); !strings.Contains(b, "Цель: сравнить манула и ирбиса") || !strings.Contains(b, "Открыто: кто тяжелее") {
		t.Fatalf("ручная правка не дошла до ведущего:\n%s", b)
	}
	if after.Task.Version != 2 {
		t.Fatalf("ход переписал задачу: %+v", after.Task)
	}
	if code, v := call(http.MethodDelete, ""); code != 200 || !v.Task.Empty() || v.Block != "" {
		t.Fatalf("DELETE: %d %+v", code, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/task/0123456789abcdef", nil))
	if rec.Code != 404 {
		t.Fatalf("чужой диалог: %d", rec.Code)
	}
}
