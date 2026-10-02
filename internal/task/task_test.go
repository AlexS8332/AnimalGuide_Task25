package task

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

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
	// Бюджет: типичное наполнение — около 160 токенов (каталог механизмов).
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

// Цитата — подстрока реплики, а не «большинство слов»: пробы критика
// (цель, ограничение и термин из реплик, где их не было) отклоняются, все
// 13 цитат живых прогонов сценариев A и B принимаются.
func TestQuoteMustBeInReply(t *testing.T) {
	bad := []struct{ quote, user string }{
		{"Расскажи про ирбиса", "Расскажи про манула"},
		{"доклад про ирбиса", "Расскажи про манула"},
		{"Только краснокнижные", "Только про Азию, пожалуйста"},
		{"только краснокнижные, пожалуйста", "Только про Азию, пожалуйста"},
		{"барс это леопард", "А барс это кто?"},
		{"барс — это леопард", "А барс это кто?"},
		{"без латыни", "Теперь отвечай с латынью"},
		{"всегда без латыни", "Без латыни, пожалуйста"},
	}
	for _, c := range bad {
		if checkQuote(c.quote, c.user) == "" {
			t.Errorf("%q принята в реплике %q", c.quote, c.user)
		}
	}
	const (
		a1 = "Я готовлю доклад для школьников о диких кошках Азии. Объясняй простыми словами, без латыни и не больше пяти предложений. Начнём с манула: где он живёт?"
		a4 = "В докладе я буду называть ирбиса барсом. Где обитает барс?"
		b1 = "Я зоолог, проверяю систематику малых панд и куньих по MDD v2.5. Отвечай кратко и пока без латыни. Сколько видов в семействе пандовых по MDD?"
		b7 = "Теперь отвечай с латынью и статусами МСОП. Какой статус МСОП у харзы по MDD?"
	)
	good := []struct{ quote, user string }{
		{"Я готовлю доклад для школьников о диких кошках Азии.", a1},
		{"для школьников", a1},
		{"о диких кошках Азии", a1},
		{"Объясняй простыми словами", a1},
		{"без латыни", a1},
		{"не больше пяти предложений", a1},
		{"В докладе я буду называть ирбиса барсом.", a4},
		{"проверяю систематику малых панд и куньих по MDD v2.5", b1},
		{"Отвечай кратко", b1},
		{"пока без латыни", b1},
		{"Теперь отвечай с латынью", b7}, // снятие «пока без латыни»
		{"Теперь отвечай с латынью", b7},
		{"и статусами МСОП", b7},
		// Модель поправила падеж или выкинула слово — значимые слова на месте.
		{"«Объясняй простыми словами!»", a1},
		{"называть ирбиса барсом", a4},
		{"доклад о диких кошках", a1},
	}
	for _, c := range good {
		if why := checkQuote(c.quote, c.user); why != "" {
			t.Errorf("%q не принята в %q: %s", c.quote, c.user, why)
		}
	}
	// Проба целиком: цель «доклад про ирбиса» на «Расскажи про манула» не
	// ставится, задача не меняется.
	var s State
	cs := s.Apply(Patch{Goal: &GoalPatch{Text: "доклад про ирбиса", Quote: "Расскажи про ирбиса"},
		Add: []PatchItem{{List: ListTerms, Term: "барс", Meaning: "леопард", Quote: "барс это леопард"}}}, "Расскажи про манула", 3)
	if !s.Empty() || s.Version != 0 || len(cs) != 2 || cs[0].Op != OpReject || cs[1].Op != OpReject {
		t.Fatalf("проба принята: %+v %+v", s, cs)
	}
}

// Блок длиннее MaxBlockRunes — без самых старых пунктов (цель и
// ограничения остаются) и с хвостом «(+N пунктов на панели)».
func TestBlockLimit(t *testing.T) {
	s := State{Goal: "доклад для школьников о диких кошках Азии",
		Constraints: []Item{{Text: "без латыни", Turn: 1}, {Text: "не больше пяти предложений", Turn: 1}}}
	long := func(i int) string { return fmt.Sprintf("пункт %d: %s", i, strings.Repeat("слово ", 24)) }
	for i := range MaxItems {
		s.Clarified = append(s.Clarified, Item{Text: long(i), Turn: 2 + i})
		s.Open = append(s.Open, Item{Text: long(100 + i), Turn: 3 + i})
	}
	s.Terms = []Term{{Term: "барс", Meaning: "ирбис", Turn: 20}}
	b := s.Block()
	if n := utf8.RuneCountInString(b); n > MaxBlockRunes {
		t.Fatalf("блок %d знаков", n)
	}
	for _, want := range []string{"Цель: доклад", "Ограничения: без латыни; не больше пяти предложений", "«барс» = ирбис", long(107), "пунктов на панели)"} {
		if !strings.Contains(b, want) {
			t.Fatalf("в блоке нет %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, long(0)+";") || strings.Contains(b, long(0)+"\n") {
		t.Fatalf("самый старый пункт остался:\n%s", b)
	}
	if s.Block() != s.Clone().Block() {
		t.Fatal("блок не детерминирован")
	}
	// Короткое состояние — без хвоста.
	if strings.Contains((State{Goal: "доклад"}).Block(), "на панели") {
		t.Fatal("хвост у короткого блока")
	}
}
