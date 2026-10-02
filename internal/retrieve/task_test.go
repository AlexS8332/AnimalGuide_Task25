package retrieve

import (
	"strings"
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/corpus"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/task"
)

func taskAliases() *Aliases {
	return AliasesOf([]corpus.Doc{
		{ID: "manul", Title: "Манул", Species: &corpus.Species{Latin: "Otocolobus manul", Ru: "манул"}},
		{ID: "irbis", Title: "Снежный барс", Species: &corpus.Species{Latin: "Panthera uncia", Ru: "снежный барс", Aliases: []string{"ирбис"}}},
		{ID: "harza", Title: "Харза", Species: &corpus.Species{Latin: "Martes flavigula", Ru: "харза"}},
		{ID: "wildcat", Title: "Лесной кот", Species: &corpus.Species{Latin: "Felis silvestris", Ru: "лесной кот", Aliases: []string{"дикая кошка"}}},
	})
}

// Вопрос-продолжение в длинном диалоге: окно ушло вперёд, вида нет ни в
// реплике, ни в контексте, — вид берётся из цели задачи, если она называет
// ровно один вид в единственном числе.
func TestRewriteCodeTakesSpeciesFromTaskGoal(t *testing.T) {
	al := taskAliases()
	q := Query{Text: "А сколько они весят?", Context: []string{"А где они зимуют?"},
		Goal: "доклад о мануле"}
	dense, bm25, expanded, note := rewriteCode(al, q)
	for _, want := range []string{"манул", "сколько они весят"} {
		if !strings.Contains(dense, want) {
			t.Fatalf("dense без %q: %q", want, dense)
		}
	}
	if !strings.Contains(bm25, "Otocolobus manul") {
		t.Fatalf("bm25 без латыни вида цели: %q", bm25)
	}
	if note != "" || !strings.Contains(strings.Join(expanded, "|"), "вид из цели задачи → манул") {
		t.Fatalf("трасса: %v %q", expanded, note)
	}
	// Цель о группе — вида из неё нет: множественное число, несколько видов,
	// общее название («дикие кошки»), неоднозначное («барс»).
	for _, goal := range []string{"доклад о манулах и ирбисах", "доклад о манулах", "про харз и манулов",
		"доклад для школьников о диких кошках Азии", "кошки Азии", "доклад о мануле и харзе", "доклад о барсе"} {
		q.Goal = goal
		if dense, _, list, note := rewriteCode(al, q); strings.Contains(dense, "манул") || strings.Contains(dense, "харз") ||
			strings.Contains(dense, "лесной") || strings.Contains(strings.Join(list, "|"), "из цели") || note == "" {
			t.Errorf("цель %q: %q %v %q", goal, dense, list, note)
		}
	}
	for _, goal := range []string{"рассказать про харзу", "Как живёт ирбис"} {
		q.Goal = goal
		if _, _, list, _ := rewriteCode(al, q); !strings.Contains(strings.Join(list, "|"), "вид из цели задачи") {
			t.Errorf("цель %q: вида нет: %v", goal, list)
		}
	}
	// Цели нет — прежнее поведение: контекст и вопрос с заметкой.
	q.Goal = ""
	if dense, _, _, note := rewriteCode(al, q); strings.Contains(dense, "манул") || note == "" {
		t.Fatalf("без цели: %q %q", dense, note)
	}
	// Вид из контекста важнее цели: разговор о харзе внутри доклада.
	q.Goal, q.Context = "доклад о мануле", []string{"Расскажи про харзу"}
	if dense, _, _, _ := rewriteCode(al, q); !strings.Contains(dense, "харз") || strings.Contains(dense, "манул") {
		t.Fatalf("контекст против цели: %q", dense)
	}
	// Самостоятельный вопрос о другом животном цель не трогает.
	if dense, _, _, _ := rewriteCode(al, Query{Text: "Сколько весит взрослый жираф?", Goal: "доклад о мануле"}); strings.Contains(dense, "манул") {
		t.Fatalf("цель прилипла к самостоятельному вопросу: %q", dense)
	}
}

// Термины задачи: «барс» словарь не раскрывает (неоднозначно), а «наш
// зверь» и вовсе не название — раскрывает задача.
func TestRewriteCodeExpandsTaskTerms(t *testing.T) {
	al := taskAliases()
	terms := []task.Term{{Term: "барс", Meaning: "ирбис"}, {Term: "наш зверь", Meaning: "манул"}}
	dense, _, expanded, _ := rewriteCode(al, Query{Text: "Где зимует барс?", Terms: terms})
	if !strings.Contains(dense, "ирбис") || !strings.Contains(dense, "снежный барс") {
		t.Fatalf("термин не раскрыт: %q %v", dense, expanded)
	}
	if !strings.Contains(strings.Join(expanded, "|"), "барс → ирбис") {
		t.Fatalf("трасса: %v", expanded)
	}
	dense, _, _, _ = rewriteCode(al, Query{Text: "Сколько весит наш зверь?", Terms: terms})
	if !strings.Contains(dense, "манул") {
		t.Fatalf("«наш зверь»: %q", dense)
	}
	// Термин в контексте: продолжение получает вид из раскрытой реплики.
	dense, _, _, _ = rewriteCode(al, Query{Text: "А чем он питается?", Context: []string{"Расскажи про нашего зверя"}, Terms: terms})
	if !strings.Contains(dense, "манул") {
		t.Fatalf("термин в контексте: %q", dense)
	}
	// Без задачи — как раньше.
	if dense, _, list, _ := rewriteCode(al, Query{Text: "Где зимует барс?"}); strings.Contains(dense, "ирбис") || len(list) != 0 {
		t.Fatalf("без задачи: %q %v", dense, list)
	}
}
