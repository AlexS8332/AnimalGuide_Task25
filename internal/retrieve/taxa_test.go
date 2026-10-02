package retrieve

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/task"
)

// Пары семейств и родов из документов корпуса: заголовки MDD, фраза
// вступления с латынью, «род волков (Canis)», «род выдр» в статье о выдре.
// «Рода лисиц» и «рода волков» в статье о песце родом песца не стали.
func TestTaxaFromCorpus(t *testing.T) {
	a := realAliases(t)
	for word, want := range map[string]string{"куньих": "Mustelidae", "Куньи": "Mustelidae", "пандовых": "Ailuridae",
		"кошачьих": "Felidae", "псовых": "Canidae"} {
		if got := taxonOf(a.Families, stem(word)); got != want {
			t.Errorf("семейство %q: %q, ждали %q", word, got, want)
		}
	}
	for word, want := range map[string]string{"куниц": "Martes", "лисиц": "Vulpes", "барсуков": "Meles", "рысей": "Lynx",
		"ласок": "Mustela", "волков": "Canis", "выдр": "Lutra", "росомахи": "Gulo"} {
		if got := taxonOf(a.Genera, stem(word)); got != want {
			t.Errorf("род %q: %q, ждали %q", word, got, want)
		}
	}
	if got := taxonOf(a.Genera, stem("песцов")); got != "" {
		t.Errorf("род песцов: %q", got)
	}
}

// Переписывание кодом: «по MDD» — полное название и латынь семейства, «в
// роде куниц» — «род Martes» в dense и Martes в BM25; без «MDD» семейство
// не раскрывается.
func TestRewriteCodeTaxa(t *testing.T) {
	a := realAliases(t)
	dense, bm25, list, _ := rewriteCode(a, Query{Text: "Сколько видов и родов в семействе куньих по MDD?"})
	for _, s := range []string{dense, bm25} {
		if !strings.Contains(s, "Mammal Diversity Database") || !strings.Contains(s, "Mustelidae") {
			t.Fatalf("MDD: %q", s)
		}
	}
	if j := strings.Join(list, "|"); !strings.Contains(j, "куньих → Mustelidae") || !strings.Contains(j, "MDD → Mammal Diversity Database") {
		t.Fatalf("трасса MDD: %v", list)
	}
	if dense, _, _, _ := rewriteCode(a, Query{Text: "Сколько видов в семействе куньих?"}); strings.Contains(dense, "Mustelidae") {
		t.Fatalf("семейство без MDD: %q", dense)
	}
	dense, bm25, list, _ = rewriteCode(a, Query{Text: "А сколько видов в роде куниц?"})
	if !strings.Contains(dense, "род Martes") || !strings.Contains(bm25, "Martes") || strings.Contains(bm25, "род Martes") {
		t.Fatalf("род: %q / %q", dense, bm25)
	}
	if !strings.Contains(strings.Join(list, "|"), "роде куниц → род Martes") {
		t.Fatalf("трасса рода: %v", list)
	}
	// «Сколько родов» — не «род X».
	if dense, _, _, _ := rewriteCode(a, Query{Text: "Сколько родов в семействе куньих?"}); strings.Contains(dense, "род ") {
		t.Fatalf("«родов»: %q", dense)
	}
	// Латынь уже в реплике — не повторяется.
	if dense, _, _, _ := rewriteCode(a, Query{Text: "Сколько видов в роде Martes по MDD (Mammal Diversity Database)?"}); strings.Count(dense, "Mammal Diversity Database") != 1 {
		t.Fatalf("повтор: %q", dense)
	}
}

// Сравнение «он или манул» (A-14): вид из ближайшей реплики контекста — в
// запрос и в якорь; без маркера сравнения («Где он живёт, манул?») — нет.
func TestRewriteCodeComparePronoun(t *testing.T) {
	a := realAliases(t)
	ctx := []string{"Ладно, вернёмся к кошкам. Почему каракала так назвали?", "Где в Азии живёт камышовый кот?", "А сколько он весит?"}
	q := Query{Text: "Кто из них крупнее — он или манул?", Context: ctx}
	dense, bm25, list, _ := rewriteCode(a, q)
	if !strings.Contains(dense, "камышовый кот") || !strings.Contains(bm25, "Felis chaus") || strings.Contains(dense, "каракал") {
		t.Fatalf("сравнение: %q / %q", dense, bm25)
	}
	if !strings.Contains(strings.Join(list, "|"), "вид из контекста для сравнения → камышовый кот") {
		t.Fatalf("трасса: %v", list)
	}
	if got := comparedSpecies(a, q, a.Species(q.Text)); strings.Join(got, ",") != "камышовый кот" {
		t.Fatalf("якорь сравнения: %v", got)
	}
	for _, text := range []string{"Где он живёт, манул?", "Манул, он крупный?", "Чем питается манул?"} {
		if dense, _, _, _ := rewriteCode(a, Query{Text: text, Context: ctx}); strings.Contains(dense, "камышовый") {
			t.Errorf("%q: лишний вид: %q", text, dense)
		}
	}
	// Вид из контекста, который уже назван в реплике, не в счёт — берётся
	// ближайший другой.
	q = Query{Text: "Он тяжелее, чем манул?", Context: []string{"Где живёт харза?", "А манул?"}}
	if got := comparedSpecies(a, q, a.Species(q.Text)); strings.Join(got, ",") != "харза" {
		t.Fatalf("ближайший другой: %v", got)
	}
	// Термины задачи — и в контексте сравнения.
	q = Query{Text: "Кто крупнее — он или манул?", Context: []string{"Где живёт барс?"}, Terms: []task.Term{{Term: "барс", Meaning: "ирбис"}}}
	if got := comparedSpecies(a, q, a.Species(q.Text)); strings.Join(got, ",") != "ирбис" && strings.Join(got, ",") != "снежный барс" {
		t.Fatalf("термины: %v", got)
	}
}

// Сравнение «она или манул» в конвейере: вид из контекста — в якоре, и
// рамка якоря (Scope) не отсекает его статью.
func TestSearchCompareAnchorsContextSpecies(t *testing.T) {
	p := topicPipeline(t)
	c := Config{Rewrite: RewriteCode, Filter: true, Scope: true}
	q := Query{Text: "Кто тяжелее — она или манул?", Context: []string{"Где водится харза?"}}
	tr, err := p.Search(context.Background(), q, c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tr.Anchored, ",") != "манул,харза" || len(tr.Scope) != 2 {
		t.Fatalf("якорь %v, рамка %v", tr.Anchored, tr.Scope)
	}
	docs := map[string]bool{}
	for _, h := range tr.Hits {
		docs[h.DocID] = true
	}
	if !docs["harza"] || !docs["manul"] {
		t.Fatalf("выдача: %+v", tr.Hits)
	}
	// Без маркера сравнения — только названный вид.
	tr, err = p.Search(context.Background(), Query{Text: "Сколько она весит, манул?", Context: q.Context}, c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tr.Anchored, ",") != "манул" {
		t.Fatalf("без сравнения: якорь %v", tr.Anchored)
	}
}
