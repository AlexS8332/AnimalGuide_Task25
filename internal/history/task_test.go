package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/facts"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/store"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/task"
)

// Память задачи (v25) живёт в ветке, как карточка фактов: ветка от точки
// начинается со снимка точки, дальше у каждой своя.
func TestTaskBelongsToBranch(t *testing.T) {
	c := newConv()
	root := c.Active
	var st task.State
	st.Apply(task.Patch{Goal: &task.GoalPatch{Text: "доклад о манулах", Quote: "доклад о манулах"}}, "Готовлю доклад о манулах.", 1)
	if ok, err := c.SetTask(root, st); !ok || err != nil {
		t.Fatalf("SetTask: %v %v", ok, err)
	}
	appendTurn(t, c, root, "Готовлю доклад о манулах.", "ок", facts.State{})
	cp, err := c.Mark(root, "")
	if err != nil {
		t.Fatal(err)
	}
	// После точки основная ветка меняет цель — снимок точки не меняется.
	next := c.Task()
	next.Apply(task.Patch{Goal: &task.GoalPatch{Text: "сравнить манула и ирбиса", Quote: "сравним манула и ирбиса"}}, "Давай сравним манула и ирбиса", 2)
	if ok, _ := c.SetTask(root, next); !ok {
		t.Fatal("новая версия не записана")
	}
	b, err := c.Fork(cp.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	c.Switch(b.ID)
	if c.Task().Goal != "доклад о манулах" {
		t.Fatalf("ветка от точки: %q", c.Task().Goal)
	}
	c.Switch(root)
	if c.Task().Goal != "сравнить манула и ирбиса" {
		t.Fatalf("основная ветка: %q", c.Task().Goal)
	}
	// Устаревший снимок (ход начался до ручной правки) не затирает новый.
	if ok, _ := c.SetTask(root, st); ok || c.Task().Goal != "сравнить манула и ирбиса" {
		t.Fatal("устаревший снимок записан")
	}
	if _, err := c.SetTask("nope", st); err == nil {
		t.Fatal("нет ошибки для чужой ветки")
	}
	// Clone — глубокий: правка копии не трогает диалог.
	cl := c.Clone()
	cl.Current().Task.Goal = "x"
	cl.Checkpoints[0].Task.Goal = "y"
	if c.Task().Goal == "x" || c.Checkpoints[0].Task.Goal == "y" {
		t.Fatal("Clone не глубокий")
	}
}

// Совместимость формата: поле новое, schema прежняя. Диалог без задачи
// пишется без ключа task (файлы прежних версий не меняются от записи), с
// задачей — читается обратно как было; файл v2 без ключа task читается.
func TestTaskFileFormat(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(store.NewDir(dir))
	c := newConv()
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "history", c.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"task": {`) { // «"task": false» — выключатель механизма, он не в счёт
		t.Fatalf("пустая задача попала в файл:\n%.400s", raw)
	}
	var doc map[string]any
	json.Unmarshal(raw, &doc)
	if doc["schema"] != float64(Kind.Current) {
		t.Fatalf("schema %v", doc["schema"])
	}

	st := task.State{Goal: "доклад", Constraints: []task.Item{{Text: "без латыни", Quote: "без латыни", Turn: 1}},
		Terms: []task.Term{{Term: "барс", Meaning: "ирбис", Turn: 1}}, Version: 2}
	c.SetTask(c.Active, st)
	c.Mark(c.Active, "точка")
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	again, err := s.Get(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Task().Goal != "доклад" || len(again.Task().Terms) != 1 || again.Checkpoints[0].Task.Version != 2 {
		t.Fatalf("задача не прочиталась: %+v", again.Task())
	}
	a, _ := json.Marshal(c)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Fatal("после записи и чтения диалог изменился")
	}
}

// Памятники прошлых форматов (testdata/legacy) поднимаются с пустой
// задачей: в старых файлах её не было.
func TestLegacyHasEmptyTask(t *testing.T) {
	for _, name := range []string{"v1-tree-e82e0ce3f4b8cd12.json", "v1-flat-8495068e20195ef2.json"} {
		_, c := legacy(t, name)
		for _, b := range c.Branches {
			if !b.Task.IsZero() {
				t.Fatalf("%s: в ветке %s задача %+v", name, b.Name, b.Task)
			}
		}
	}
}
