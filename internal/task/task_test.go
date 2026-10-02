package task

import (
	"strings"
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/tokens"
)

func TestApplyNeedsQuoteFromUser(t *testing.T) {
	var s State
	user := "Я готовлю доклад для пятиклассников о кошках Азии, и пиши без латыни."
	cs := s.Apply(Patch{
		Goal: &GoalPatch{Text: "доклад для 5 класса о кошках Азии", Quote: "готовлю доклад для пятиклассников о кошках Азии"},
		Add: []PatchItem{
			{List: ListConstraints, Text: "без латыни", Quote: "без латыни"},
			{List: ListClarified, Text: "уровень — 5 класс", Quote: ""},                    // нет цитаты
			{List: ListOpen, Text: "какие виды взять", Quote: "какие виды взять в доклад"}, // цитата не из реплики
			{List: "weird", Text: "x", Quote: "доклад"},                                    // нет списка
			{List: ListConstraints, Text: "Без латыни", Quote: "без латыни"},               // дубль
			{List: ListTerms, Term: "барс", Meaning: "ирбис", Quote: "барс — это ирбис"},   // не из реплики
			{List: ListTerms, Text: "кошки = дикие кошки", Quote: "о кошках Азии"},         // термин в text
		},
	}, user, 1)
	if s.Goal != "доклад для 5 класса о кошках Азии" || s.GoalTurn != 1 || len(s.Constraints) != 1 || len(s.Clarified) != 0 || len(s.Open) != 0 {
		t.Fatalf("состояние: %+v", s)
	}
	if len(s.Terms) != 1 || s.Terms[0].Term != "кошки" || s.Terms[0].Meaning != "дикие кошки" {
		t.Fatalf("термины: %+v", s.Terms)
	}
	rejects := 0
	for _, c := range cs {
		if c.Op == OpReject {
			rejects++
			if c.Reason == "" {
				t.Fatalf("отказ без причины: %+v", c)
			}
		}
	}
	if rejects != 4 || s.Version != 3 {
		t.Fatalf("отказов %d, версия %d: %+v", rejects, s.Version, cs)
	}
	if got := Summary(cs); !strings.Contains(got, "цель:") || !strings.Contains(got, "+ограничение: без латыни") {
		t.Fatalf("сводка: %s", got)
	}
}

func TestSetGoalReplacesAndKeepsOld(t *testing.T) {
	s := State{Goal: "доклад о манулах", Version: 1}
	cs := s.Apply(Patch{Goal: &GoalPatch{Text: "сравнить манула и ирбиса", Quote: "давай лучше сравним манула и ирбиса"}},
		"Нет, давай лучше сравним манула и ирбиса.", 5)
	if len(cs) != 1 || cs[0].Op != OpSetGoal || cs[0].Old != "доклад о манулах" || s.Goal != "сравнить манула и ирбиса" || s.Version != 2 {
		t.Fatalf("%+v %+v", cs, s)
	}
	// Та же цель — не правка.
	if cs := s.Apply(Patch{Goal: &GoalPatch{Text: "Сравнить манула и ирбиса", Quote: "сравним манула"}}, "сравним манула", 6); len(cs) != 0 || s.Version != 2 {
		t.Fatalf("та же цель: %+v", cs)
	}
	// Цель из ответа справочника не ставится.
	cs = s.Apply(Patch{Goal: &GoalPatch{Text: "выбрать питомца", Quote: "выбор питомца"}}, "А сколько они весят?", 7)
	if len(cs) != 1 || cs[0].Op != OpReject || s.Goal != "сравнить манула и ирбиса" {
		t.Fatalf("чужая цитата: %+v", cs)
	}
}

func TestRemoveByTextAndTerm(t *testing.T) {
	s := State{
		Goal:        "доклад",
		Constraints: []Item{{Text: "без латыни"}, {Text: "не больше 5 предложений"}},
		Terms:       []Term{{Term: "барс", Meaning: "ирбис"}},
		Open:        []Item{{Text: "какие виды взять"}},
	}
	user := "Латынь теперь можно, и барс — это просто барс; какие виды взять — решили."
	cs := s.Apply(Patch{Remove: []PatchItem{
		{List: ListConstraints, Text: "без латинских названий", Quote: "латынь теперь можно"}, // не та формулировка — по основам не совпало
		{List: ListConstraints, Text: "Без латыни", Quote: "латынь теперь можно"},
		{List: ListTerms, Term: "барса", Quote: "барс — это просто барс"},
		{List: ListOpen, Text: "какие виды взять", Quote: "какие виды взять — решили"},
		{List: ListOpen, Text: "чего нет", Quote: "решили"},
	}}, user, 3)
	if len(s.Constraints) != 1 || len(s.Terms) != 0 || s.Open != nil {
		t.Fatalf("после снятия: %+v (%+v)", s, cs)
	}
	if s.Version != 3 {
		t.Fatalf("версия %d", s.Version)
	}
	// Снятие цели.
	cs = s.Apply(Patch{Remove: []PatchItem{{List: ListGoal, Quote: "забудь про доклад"}}}, "Забудь про доклад.", 4)
	if s.Goal != "" || len(cs) != 1 || cs[0].Op != OpRemove || cs[0].Text != "доклад" {
		t.Fatalf("снятие цели: %+v %+v", cs, s)
	}
}

func TestLimitsDropOldestAndClip(t *testing.T) {
	var s State
	user := strings.Repeat("пункт ", 3)
	for i := range MaxItems + 2 {
		s.Apply(Patch{Add: []PatchItem{{List: ListClarified, Text: "пункт " + string(rune('а'+i)), Quote: "пункт"}}}, user, i+1)
	}
	if len(s.Clarified) != MaxItems || s.Clarified[0].Text != "пункт в" {
		t.Fatalf("предел: %+v", s.Clarified)
	}
	long := strings.Repeat("я", MaxRunes+50)
	s.Apply(Patch{Add: []PatchItem{{List: ListConstraints, Text: long, Quote: "пункт"}}}, user, 20)
	if n := len([]rune(s.Constraints[0].Text)); n != MaxRunes+1 {
		t.Fatalf("длина пункта %d", n)
	}
}

func TestTermUpdateMeaning(t *testing.T) {
	s := State{Terms: []Term{{Term: "наш зверь", Meaning: "манул"}}}
	cs := s.Apply(Patch{Add: []PatchItem{{List: ListTerms, Term: "Наш зверь", Meaning: "ирбис", Quote: "наш зверь теперь ирбис"}}},
		"Наш зверь теперь ирбис", 2)
	if len(s.Terms) != 1 || s.Terms[0].Meaning != "ирбис" || len(cs) != 1 || cs[0].Old != "наш зверь = манул" {
		t.Fatalf("%+v %+v", s.Terms, cs)
	}
}

func TestCloneIsDeep(t *testing.T) {
	a := State{Clarified: []Item{{Text: "x"}}, Terms: []Term{{Term: "барс", Meaning: "ирбис"}}, Constraints: []Item{{Text: "y"}}, Open: []Item{{Text: "z"}}}
	b := a.Clone()
	b.Clarified[0].Text, b.Terms[0].Meaning, b.Constraints[0].Text, b.Open[0].Text = "1", "2", "3", "4"
	if a.Clarified[0].Text != "x" || a.Terms[0].Meaning != "ирбис" || a.Constraints[0].Text != "y" || a.Open[0].Text != "z" {
		t.Fatalf("копия не глубокая: %+v", a)
	}
}

func TestBlock(t *testing.T) {
	if (State{}).Block() != "" || (State{Version: 3}).Block() != "" {
		t.Fatal("пустое состояние даёт блок")
	}
	s := State{
		Goal:        "доклад для 5 класса о диких кошках Азии",
		Clarified:   []Item{{Text: "уровень — школьники 5 класса", Quote: "для пятиклассников"}, {Text: "интересуют манул и ирбис"}},
		Constraints: []Item{{Text: "без латыни"}, {Text: "не больше 5 предложений"}},
		Terms:       []Term{{Term: "барс", Meaning: "ирбис (снежный барс)"}, {Term: "наш зверь", Meaning: "манул"}},
		Open:        []Item{{Text: "какие виды взять в доклад"}},
	}
	b := s.Block()
	for _, want := range []string{"Задача разговора (ведёт код; меняется только словами человека)", "Цель: доклад",
		"Уточнено: уровень — школьники 5 класса; интересуют манул и ирбис", "Ограничения: без латыни; не больше 5 предложений",
		"Термины: «барс» = ирбис (снежный барс); «наш зверь» = манул", "Открыто: какие виды взять в доклад"} {
		if !strings.Contains(b, want) {
			t.Fatalf("в блоке нет %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "пятиклассников") {
		t.Fatal("цитата попала в блок")
	}
	// Бюджет: типичное наполнение — около 150 токенов (каталог механизмов).
	if n := tokens.Default.Text(b); n > 190 {
		t.Fatalf("блок ≈%.0f токенов:\n%s", n, b)
	}
	if s.Block() != s.Clone().Block() {
		t.Fatal("блок не детерминирован")
	}
}

func TestExpand(t *testing.T) {
	s := State{Terms: []Term{{Term: "барс", Meaning: "ирбис"}, {Term: "наш зверь", Meaning: "манул"}}}
	got, list := s.Expand("Где живёт барс?")
	if got != "Где живёт барс? ирбис" || len(list) != 1 || list[0] != "барс → ирбис" {
		t.Fatalf("%q %v", got, list)
	}
	if got, _ := s.Expand("А что ест нашего зверя зимой?"); !strings.HasSuffix(got, " манул") {
		t.Fatalf("падеж многословного термина: %q", got)
	}
	if got, list := s.Expand("Где живёт барсук?"); list != nil || got != "Где живёт барсук?" {
		t.Fatalf("барсук — не барс: %q", got)
	}
	if got, list := s.Expand("Барс, он же ирбис, где живёт?"); list != nil || got != "Барс, он же ирбис, где живёт?" {
		t.Fatalf("значение уже в запросе: %q", got)
	}
}

func TestClean(t *testing.T) {
	s := State{Goal: "  доклад  ", Constraints: []Item{{Text: " без латыни "}, {Text: "Без латыни"}, {Text: " "}},
		Terms: []Term{{Term: "барс", Meaning: ""}, {Term: "барс", Meaning: "ирбис"}, {Term: "Барс", Meaning: "снежный барс"}}, Version: 4}
	c := s.Clean()
	if c.Goal != "доклад" || len(c.Constraints) != 1 || len(c.Terms) != 1 || c.Terms[0].Meaning != "ирбис" || c.Version != 4 {
		t.Fatalf("%+v", c)
	}
}
