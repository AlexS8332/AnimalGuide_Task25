package dialogs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/features"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/history"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/rag"
)

func corpus(t *testing.T) map[string]bool {
	t.Helper()
	docs, err := CorpusDocs(filepath.Join("..", "..", "corpus"))
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

// Сценарии задания 25 валидны: 10–15 реплик, doc_id из корпуса, есть
// контрольные реплики, последняя реплика — контрольная.
func TestScenarioFiles(t *testing.T) {
	docs := corpus(t)
	for _, name := range []string{"a.json", "b.json"} {
		s, err := Load(filepath.Join("..", "..", "eval", "dialogs", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := Validate(s, docs); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.Preset != "rag" {
			t.Errorf("%s: пресет %q", name, s.Preset)
		}
		if !s.Turns[len(s.Turns)-1].Has(MarkGoal) {
			t.Errorf("%s: последняя реплика не контрольная", name)
		}
		controls, unknown := 0, 0
		for _, tr := range s.Turns {
			if tr.Has(MarkGoal) {
				controls++
				// Контрольная реплика при rag.cite идёт по памяти задачи.
				if !rag.Meta(tr.Text) {
					t.Errorf("%s: контрольная %q не распознана как реплика о разговоре", name, tr.Text)
				}
			} else if rag.Meta(tr.Text) {
				t.Errorf("%s: %q принята бы за реплику о разговоре", name, tr.Text)
			}
			if tr.Unknown {
				unknown++
			}
		}
		if controls < 3 {
			t.Errorf("%s: контрольных реплик %d", name, controls)
		}
		if s.ID == "B" && unknown == 0 {
			t.Error("B: нет вопроса вне базы")
		}
	}
}

func TestValidateProblems(t *testing.T) {
	s := Scenario{Schema: 1, ID: "x", Goal: Goal{Text: "цель", Keywords: []string{"цел"}}}
	for range 9 {
		s.Turns = append(s.Turns, Turn{Text: "вопрос", Docs: []string{"manul"}})
	}
	s.Turns = append(s.Turns, Turn{Text: "напомни цель", Marks: []string{MarkGoal}})
	if err := Validate(s, corpus(t)); err != nil {
		t.Fatal(err)
	}
	bad := s
	bad.Turns = append([]Turn(nil), s.Turns...)
	bad.Turns[0] = Turn{Text: " ", Docs: []string{"nope|manul"}, Marks: []string{"странная"}}
	bad.Turns[1] = Turn{Text: "вне базы", Unknown: true, Docs: []string{"manul"}}
	bad.Turns[2] = Turn{Text: "без docs"}
	bad.Rules = []Rule{{Kind: "rhyme", From: 1}, {Kind: RuleMaxSentences, From: 1}, {Kind: RuleLatin, From: 3, To: 2}, {Kind: RuleIUCN, Turns: []int{99}}}
	err := Validate(bad, corpus(t))
	for _, want := range []string{"реплика 1 пустая", "неизвестная метка", `doc_id "nope"`, "вне базы с ожидаемыми docs", "реплика 3: нет ожидаемых docs",
		"неизвестный вид", "max_sentences без n", "from 3, to 2", "нет хода 99"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("нет проблемы %q: %v", want, err)
		}
	}
	short := s
	short.Turns = s.Turns[:5]
	if err := Validate(short, nil); err == nil || !strings.Contains(err.Error(), "реплик 5") {
		t.Errorf("короткий сценарий: %v", err)
	}
	if _, err := Parse([]byte(`{"schema":1,"unknonw":true}`)); err == nil {
		t.Error("неизвестное поле")
	}
}

func answered(answer string, chunks ...string) Observed {
	v := &rag.CiteView{Cited: rag.Cited{Status: rag.StatusAnswered, Answer: answer}, Check: rag.CiteCheck{OK: true, HasSources: len(chunks) > 0}}
	for i, c := range chunks {
		v.Sources = append(v.Sources, rag.CiteViewSrc{N: i + 1, ChunkID: c, Issued: true})
		v.Cited.Sources = append(v.Cited.Sources, rag.CitedSource{ChunkID: c})
	}
	v.Text = answer + "\n\n**Источники:** …"
	return Observed{Reply: v.Text, Route: "lead", Cite: v}
}

func scenario() Scenario {
	return Scenario{Schema: 1, ID: "T", Goal: Goal{Text: "доклад о кошках", Keywords: []string{"доклад|сообщени", "кош|кот", "ази"}},
		Rules: []Rule{{Kind: RuleNoLatin, From: 1, To: 2}, {Kind: RuleMaxSentences, N: 2, From: 1}, {Kind: RuleLatin, From: 3}, {Kind: RuleIUCN, Turns: []int{3}}},
		Turns: []Turn{
			{Text: "манул", Docs: []string{"manul"}},
			{Text: "харза и соболь", Docs: []string{"yellow-throated-marten", "sable|mdd-carnivora"}},
			{Text: "статус", Docs: []string{"mdd-carnivora"}},
			{Text: "фосса", Unknown: true},
			{Text: "напомни цель", Marks: []string{MarkGoal}},
		}}
}

func byName(cs []Check) map[string]Check {
	m := map[string]Check{}
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
}

func TestCheckTurnAnswered(t *testing.T) {
	s := scenario()
	c := byName(CheckTurn(s, 1, answered("Манул живёт в степях Азии.", "manul/structure/001", "felidae/structure/002")))
	if !c[CheckReply].OK || !c[CheckSources].OK || !c[CheckDocs].OK || !c[RuleNoLatin].OK || !c[RuleMaxSentences].OK {
		t.Fatalf("хороший ответ: %+v", c)
	}
	// Латынь в ответе, три предложения, нет источников.
	c = byName(CheckTurn(s, 1, answered("Манул (Otocolobus manul) живёт в Азии. Он мал. Он пушист.")))
	if c[CheckSources].OK || c[CheckDocs].OK || c[RuleNoLatin].OK || c[RuleNoLatin].Got != "Otocolobus manul" || c[RuleMaxSentences].OK {
		t.Fatalf("плохой ответ: %+v", c)
	}
	// Латынь только в цитате — не нарушение: меряется ответ без обвязки.
	o := answered("Харза крупнее соболя.", "yellow-throated-marten/structure/003", "mdd-carnivora/structure/010")
	o.Cite.Cited.Quotes = []rag.CitedQuote{{ChunkID: "mdd-carnivora/structure/010", Text: "Martes zibellina (Linnaeus, 1758)"}}
	c = byName(CheckTurn(s, 2, o))
	if !c[CheckDocs].OK || !c[RuleNoLatin].OK {
		t.Fatalf("альтернативы docs и латынь в цитате: %+v", c)
	}
	c = byName(CheckTurn(s, 2, answered("Харза крупнее.", "yellow-throated-marten/structure/003")))
	if c[CheckDocs].OK || !strings.Contains(c[CheckDocs].Got, "нет: sable|mdd-carnivora") {
		t.Fatalf("недостающий doc: %+v", c[CheckDocs])
	}
	// С 3-го хода — латынь и статус МСОП обязательны.
	c = byName(CheckTurn(s, 3, answered("Martes flavigula — LC.", "mdd-carnivora/structure/010")))
	if !c[RuleLatin].OK || !c[RuleIUCN].OK || c[RuleNoLatin].Name != "" {
		t.Fatalf("латынь и МСОП: %+v", c)
	}
	c = byName(CheckTurn(s, 3, answered("Харза — вид, вызывающий наименьшие опасения.", "mdd-carnivora/structure/010")))
	if c[RuleLatin].OK || !c[RuleIUCN].OK {
		t.Fatalf("без латыни, со статусом словами: %+v", c)
	}
}

func TestCheckTurnUnknownMetaAndErrors(t *testing.T) {
	s := scenario()
	unk := Observed{Reply: "Не знаю: в базе нет фоссы.", Cite: &rag.CiteView{Cited: rag.Cited{Status: rag.StatusUnknown, Answer: "в базе нет фоссы"}}}
	c := byName(CheckTurn(s, 4, unk))
	if !c[CheckSources].OK || !c[RuleLatin].NA || c[CheckDocs].Name != "" {
		t.Fatalf("«не знаю» на вопросе вне базы: %+v", c)
	}
	// Ответ по существу там, где ждали «не знаю», — провал.
	if c := byName(CheckTurn(s, 4, answered("Фосса весит 10 кг.", "mdd-carnivora/structure/001"))); c[CheckSources].OK {
		t.Fatal("ответ вместо «не знаю»")
	}
	// «Не знаю» там, где ответ в базе, — провал источников.
	if c := byName(CheckTurn(s, 1, unk)); c[CheckSources].OK {
		t.Fatal("«не знаю» вместо ответа")
	}
	meta := Observed{Reply: "Цель — доклад о диких кошках Азии.", Cite: &rag.CiteView{Meta: true, MetaSource: rag.MetaSource}}
	c = byName(CheckTurn(s, 5, meta))
	if !c[CheckSources].OK || !c[CheckGoal].OK || !c[RuleLatin].NA {
		t.Fatalf("контрольная реплика: %+v", c)
	}
	meta.Reply = "Мы говорили о манулах."
	c = byName(CheckTurn(s, 5, meta))
	if c[CheckGoal].OK || !strings.Contains(c[CheckGoal].Got, "доклад|сообщени") || !strings.Contains(c[CheckGoal].Got, "ази") {
		t.Fatalf("цель не названа: %+v", c[CheckGoal])
	}
	// Ход мимо ведущего (без rag.cite) — нет источников.
	if c := byName(CheckTurn(s, 1, Observed{Reply: "Манул. Разделы карточки свёрнуты.", Route: "card"})); c[CheckSources].OK || c[CheckSources].Got != "none" {
		t.Fatalf("карточка без источников: %+v", c[CheckSources])
	}
	cs := CheckTurn(s, 1, Observed{Error: "ход не удался"})
	if len(cs) != 1 || cs[0].OK {
		t.Fatalf("ошибка хода: %+v", cs)
	}
}

func TestDetectors(t *testing.T) {
	if !HasLatin("Lynx lynx") || HasLatin("Ailuridae") || HasLatin("[manul/structure/004]") {
		t.Error("HasLatin")
	}
	if !HasIUCN("статус EN") || !HasIUCN("Уязвимый вид") || HasIUCN("en route") {
		t.Error("HasIUCN")
	}
	if Sentences("Раз. Два! Три?") != 3 {
		t.Error("Sentences")
	}
	if m := GoalNamed("Готовим ДОКЛАД о кошках Азии для школьников", []string{"доклад", "кош|кот", "ази", "школ"}); len(m) != 0 {
		t.Errorf("GoalNamed: %v", m)
	}
	if m := GoalNamed("сравнение ёжиков", []string{"еж"}); len(m) != 0 {
		t.Errorf("ё: %v", m)
	}
}

type fakeAsker struct {
	replies []Observed
	i       int
	failAt  int
}

func (f *fakeAsker) Ask(_ context.Context, text string) (Observed, error) {
	f.i++
	if f.i == f.failAt {
		return Observed{}, errors.New("сервер не отвечает")
	}
	return f.replies[f.i-1], nil
}

func TestPlayAndReport(t *testing.T) {
	s := scenario()
	good := []Observed{
		answered("Манул живёт в Азии.", "manul/structure/001"),
		answered("Харза тяжелее соболя.", "yellow-throated-marten/structure/003", "sable/structure/007"),
		answered("Martes flavigula — LC.", "mdd-carnivora/structure/010"),
		{Reply: "Не знаю.", Cite: &rag.CiteView{Cited: rag.Cited{Status: rag.StatusUnknown}}},
		{Reply: "Цель — доклад о кошках Азии.", Cite: &rag.CiteView{Meta: true}},
	}
	var seen int
	rep, err := Play(context.Background(), s, &fakeAsker{replies: good}, func(TurnReport) { seen++ })
	if err != nil || seen != 5 || !rep.Passed() {
		t.Fatalf("прогон: %v, ходов %d, %s", err, seen, rep.Summary())
	}
	if !strings.Contains(rep.Summary(), "пройден: reply 5/5, sources 5/5, docs 3/3") {
		t.Fatalf("итог: %s", rep.Summary())
	}
	bad := append([]Observed(nil), good...)
	bad[4] = Observed{Reply: "Не помню.", Cite: &rag.CiteView{Meta: true}}
	rep, _ = Play(context.Background(), s, &fakeAsker{replies: bad}, nil)
	if rep.Passed() || len(rep.Turns[4].Failed()) != 1 || !strings.Contains(rep.Summary(), "НЕ пройден") {
		t.Fatalf("провал цели: %s", rep.Summary())
	}
	rep, err = Play(context.Background(), s, &fakeAsker{replies: good, failAt: 3}, nil)
	if err == nil || len(rep.Turns) != 2 || !strings.Contains(err.Error(), "реплика 3") {
		t.Fatalf("обрыв прогона: %v, ходов %d", err, len(rep.Turns))
	}
	if (Report{}).Passed() {
		t.Error("пустой отчёт пройден")
	}
}

// Наблюдение из записи хода: ответ, итог rag.cite; незавершённый ход —
// ошибка даже без её текста.
func TestFromTurn(t *testing.T) {
	tr := history.Turn{ID: "t1", Status: history.TurnDone, Route: "lead", Reply: "Ответ."}
	tr.SetExtra(string(features.RAGCite), rag.CiteView{Cited: rag.Cited{Status: rag.StatusAnswered, Answer: "Ответ."},
		Sources: []rag.CiteViewSrc{{N: 1, ChunkID: "manul/structure/001"}}})
	o := FromTurn(tr)
	if o.TurnID != "t1" || o.Route != "lead" || o.Status() != "answered" || len(o.Docs()) != 1 || o.Docs()[0] != "manul" {
		t.Fatalf("ход: %+v", o)
	}
	if o := FromTurn(history.Turn{Status: history.TurnFailed}); o.Status() != "error" || o.Cite != nil {
		t.Fatalf("неудачный ход: %+v", o)
	}
	if o := FromTurn(history.Turn{Status: history.TurnDone, Reply: "Карточка."}); o.Status() != "none" {
		t.Fatalf("без rag.cite: %+v", o)
	}
}

// «Без латыни» ловит и одиночное слово латиницей от четырёх букв (род,
// семейство); коды МСОП, «MDD» и аббревиатуры заглавными — не латынь.
func TestNoLatinSingleWords(t *testing.T) {
	for _, s := range []string{"Харза из рода Martes.", "Семейство Mustelidae велико.", "Подсемейство Mustelinae.", "Малая панда (Ailurus) — пандовые."} {
		if LatinFound(s) == "" {
			t.Errorf("%q: латынь не найдена", s)
		}
	}
	for _, s := range []string{"Статус LC, у панды — EN; CR, VU, NT, EW, EX, DD, NE.", "По MDD v2.5 — 72 вида.", "По данным IUCN и GBIF.", "Манул живёт в Азии."} {
		if m := LatinFound(s); m != "" {
			t.Errorf("%q: ложная латынь %q", s, m)
		}
	}
	s := scenario()
	if c := byName(CheckTurn(s, 1, answered("Это род Felis.", "manul/structure/001"))); c[RuleNoLatin].OK || c[RuleNoLatin].Got != "Felis" {
		t.Fatalf("род латиницей: %+v", c[RuleNoLatin])
	}
}

// must — числа и слова ответа; grounded — опора на цитаты у answered
// (признак проверки rag.cite, без него — NumbersMissing и
// SpeciesMismatch пусты).
func TestMustAndGrounded(t *testing.T) {
	if m := MustMissing("Соболь весит 0.8–1,4 кг, харза — до 5,8.", []string{"0,8", "1,4", "соб"}); len(m) != 0 {
		t.Fatalf("must: %v", m)
	}
	if m := MustMissing("Около 58 тысяч, 12 кг.", []string{"8", "12", "манул"}); len(m) != 2 || m[0] != "8" || m[1] != "манул" {
		t.Fatalf("8 в 58 и слово: %v", m)
	}
	s := scenario()
	s.Turns[1].Must = []string{"0,8", "1,4"}
	o := answered("Харза тяжелее: соболь весит 0,8–1,4 кг.", "yellow-throated-marten/structure/003", "sable/structure/007")
	c := byName(CheckTurn(s, 2, o))
	if !c[CheckMust].OK || !c[CheckGrounded].OK {
		t.Fatalf("хороший ответ: %+v", c)
	}
	o = answered("Харза тяжелее соболя.", "yellow-throated-marten/structure/003", "sable/structure/007")
	o.Cite.Check.NumbersMissing = []string{"13"}
	c = byName(CheckTurn(s, 2, o))
	if c[CheckMust].OK || c[CheckMust].Got != "нет: 0,8, 1,4" || c[CheckGrounded].OK || !strings.Contains(c[CheckGrounded].Got, "13") {
		t.Fatalf("без чисел и с числом вне цитат: %+v", c)
	}
	// Отчётные: провалом хода не делают.
	if f := (TurnReport{Checks: CheckTurn(s, 2, o)}).Failed(); len(f) != 0 {
		t.Fatalf("отчётные проверки провалили ход: %+v", f)
	}
	// Признак проверки rag.cite важнее косвенных.
	yes := true
	o.Grounded = &yes
	if c := byName(CheckTurn(s, 2, o)); !c[CheckGrounded].OK {
		t.Fatalf("grounded из проверки: %+v", c[CheckGrounded])
	}
	// У «не знаю» и о разговоре — не определить; must у них запрещён.
	unk := Observed{Reply: "Не знаю.", Cite: &rag.CiteView{Cited: rag.Cited{Status: rag.StatusUnknown}}}
	if c := byName(CheckTurn(s, 4, unk)); !c[CheckGrounded].NA {
		t.Fatalf("grounded у «не знаю»: %+v", c[CheckGrounded])
	}
	bad := s
	bad.Turns = append([]Turn(nil), s.Turns...)
	bad.Turns[3].Must = []string{"1"}
	if err := Validate(bad, nil); err == nil || !strings.Contains(err.Error(), "must — только у вопроса по базе") {
		t.Fatalf("must у вопроса вне базы: %v", err)
	}
	// Grounded из записи хода — поле check.grounded итога rag.cite.
	tr := history.Turn{ID: "t1", Status: history.TurnDone, Reply: "Ответ."}
	tr.SetExtra(string(features.RAGCite), map[string]any{"cited": map[string]any{"status": "answered", "answer": "Ответ."},
		"check": map[string]any{"ok": true, "has_sources": true, "grounded": false}})
	if o := FromTurn(tr); o.Grounded == nil || *o.Grounded {
		t.Fatalf("grounded из хода: %+v", o.Grounded)
	}
}

// Источники показаны (ТЗ): ответ по базе — со списком; «не знаю» — даже с
// пустой выдачей; о разговоре — память задачи; ход мимо rag.cite — нет.
func TestSourcesShown(t *testing.T) {
	cases := []struct {
		o    Observed
		want bool
	}{
		{answered("Манул живёт в Азии.", "manul/structure/001"), true},
		{answered("Манул живёт в Азии."), false},
		{Observed{Reply: "Не знаю.", Cite: &rag.CiteView{Cited: rag.Cited{Status: rag.StatusUnknown}}}, true},
		{Observed{Reply: "Цель — доклад.", Cite: &rag.CiteView{Meta: true, MetaSource: rag.MetaSource}}, true},
		{Observed{Reply: "Карточка."}, false},
		{Observed{Error: "упал"}, false},
	}
	for i, c := range cases {
		if got, why := SourcesShown(c.o); got != c.want {
			t.Errorf("%d: %v (%s), ждали %v", i, got, why, c.want)
		}
	}
}

// must сценариев сверен с корпусом: каждое число и слово есть в тексте
// одного из ожидаемых документов; у B — марка перезапуска.
func TestScenarioMustInCorpus(t *testing.T) {
	restart := false
	for _, name := range []string{"a.json", "b.json"} {
		s, err := Load(filepath.Join("..", "..", "eval", "dialogs", name))
		if err != nil {
			t.Fatal(err)
		}
		for i, tr := range s.Turns {
			restart = restart || tr.Has(MarkRestart)
			if len(tr.Must) == 0 {
				continue
			}
			var text strings.Builder
			for _, d := range tr.Docs {
				for _, alt := range strings.Split(d, "|") {
					data, err := os.ReadFile(filepath.Join("..", "..", "corpus", strings.TrimSpace(alt)+".json"))
					if err != nil {
						t.Fatal(err)
					}
					text.Write(data)
				}
			}
			if m := MustMissing(text.String(), tr.Must); len(m) > 0 {
				t.Errorf("%s·%d: в корпусе нет %v", s.ID, i+1, m)
			}
		}
	}
	if !restart {
		t.Error("ни в одном сценарии нет марки restart")
	}
}
