package retrieve

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/corpus"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/task"
)

// Таксоны выше вида в переписывании кодом (v25).
//
// Вопросы о систематике называют не вид, а семейство или род по-русски:
// «сколько видов и родов в семействе куньих по MDD?», «а сколько видов в
// роде куниц?». MDD в корпусе записан латынью («Семейство Куньи
// (Mustelidae)», «Род Martes — 8 видов»), и без латыни dense уводит такие
// вопросы в статью Википедии о семействе, а BM25 латынь не находит вовсе.
// Пары «русское ↔ латынь» берутся из самих документов базы при
// LoadAliases, а не списком в коде:
//
//   - семейства — заголовки разделов и начала строк вида «Куньи
//     (Mustelidae)» (MDD: раздел на семейство и сводка «Семейства хищных»);
//   - роды — «рода <слово>» во фразе вступления с латынью вида («Соболь (лат.
//     Martes zibellina) — млекопитающее рода куниц») — род вида; «род <слово>
//     (Genus)» в любом тексте («род волков (Canis)»); «род <слово>» в статье
//     о виде, чьё название это слово и есть («род выдр» в статье о речной
//     выдре) — тоже род самого вида. «Рода лисиц» в статье о песце родом
//     песца не считается: песец — не лисица по названию.
//
// Пара, у которой два разных значения, выбрасывается — как синоним двух
// видов.

// mddName — полное название MDD для запроса: во фрагментах MDD оно стоит
// словами, а «по MDD» в вопросе dense не связывает с ними.
const mddName = "Mammal Diversity Database"

// mddRe — вопрос «по MDD».
var mddRe = regexp.MustCompile(`(?i)(^|[^\p{L}])mdd([^\p{L}]|$)|mammal diversity`)

// familyRe — «Куньи (Mustelidae)» в начале заголовка или строки.
var familyRe = regexp.MustCompile(`^(?:Семейство\s+)?(\p{Lu}\p{Ll}+)\s*\(([A-Z][a-z]+idae)\)`)

// genusLatinRe — «род волков (Canis)».
var genusLatinRe = regexp.MustCompile(`(?:^|[^\p{L}])род(?:а|е|у|ом)?\s+(\p{Cyrillic}+)\s*\(([A-Z][a-z]+)\)`)

// genusRe — «рода куниц», «род выдр».
var genusRe = regexp.MustCompile(`(?:^|[^\p{L}])род(?:а|е|у|ом)?\s+(\p{Cyrillic}+)`)

// minTaxonWord — короче — не название («рода под …» не бывает, но «род
// их» — бывает).
const minTaxonWord = 4

// taxa — пары таксонов выше вида из документов (см. начало файла).
func taxa(docs []corpus.Doc) (families, genera map[string]string) {
	families, genera = map[string]string{}, map[string]string{}
	badF, badG := map[string]bool{}, map[string]bool{}
	put := func(m map[string]string, bad map[string]bool, word, latin string) {
		if utf8.RuneCountInString(word) < minTaxonWord || latin == "" {
			return
		}
		k := stem(word)
		if bad[k] {
			return
		}
		if prev, ok := m[k]; ok && prev != latin {
			delete(m, k)
			bad[k] = true
			return
		}
		m[k] = latin
	}
	for _, d := range docs {
		texts := []string{d.Intro}
		for _, s := range d.Sections {
			if m := familyRe.FindStringSubmatch(strings.TrimSpace(s.Title)); m != nil {
				put(families, badF, m[1], m[2])
			}
			texts = append(texts, s.Text)
		}
		for _, t := range texts {
			for _, line := range strings.Split(t, "\n") {
				if m := familyRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
					put(families, badF, m[1], m[2])
				}
			}
			for _, m := range genusLatinRe.FindAllStringSubmatch(t, -1) {
				put(genera, badG, m[1], m[2])
			}
		}
		sp := d.Species
		if sp == nil || sp.Latin == "" {
			continue
		}
		genus := strings.Fields(sp.Latin)[0]
		// Род вида из фразы вступления с его латынью.
		if i := strings.Index(d.Intro, sp.Latin); i >= 0 {
			rest := d.Intro[i+len(sp.Latin):]
			if j := strings.Index(rest, "."); j >= 0 {
				rest = rest[:j]
			}
			for _, m := range genusRe.FindAllStringSubmatch(rest, -1) {
				put(genera, badG, m[1], genus)
			}
		}
		// «Род выдр» в статье о речной выдре.
		own := ownWords(d)
		for _, t := range texts {
			for _, m := range genusRe.FindAllStringSubmatch(t, -1) {
				if own[stem(m[1])] {
					put(genera, badG, m[1], genus)
				}
			}
		}
	}
	return families, genera
}

// ownWords — основы последних слов названий вида статьи (канон и синонимы):
// «выдр» у речной выдры, «волк» у волка.
func ownWords(d corpus.Doc) map[string]bool {
	out := map[string]bool{}
	names := append([]string{d.Species.Ru, d.Title}, d.Species.Aliases...)
	for _, n := range names {
		if ws := strings.Fields(strings.ToLower(n)); len(ws) > 0 {
			if w := ws[len(ws)-1]; utf8.RuneCountInString(w) >= minTaxonWord && cyrillicWords([]string{w}) {
				out[stem(w)] = true
			}
		}
	}
	return out
}

// taxonQuery — добавки запроса для таксонов выше вида:
//
//   - «по MDD» — полное название MDD и латынь семейства, названного
//     по-русски («семейство куньих» → Mustelidae), в оба запроса;
//   - «в роде куниц» — «род Martes» в dense и Martes в BM25.
//
// Латынь, которая уже есть в реплике, не повторяется.
func taxonQuery(al *Aliases, text string) (dense, bm25, expanded []string) {
	if al == nil {
		return nil, nil, nil
	}
	toks := tokenize(text)
	if mddRe.MatchString(text) {
		if !containsFold(text, mddName) {
			dense, bm25 = append(dense, mddName), append(bm25, mddName)
			expanded = append(expanded, "MDD → "+mddName)
		}
		seen := map[string]bool{}
		for _, t := range toks {
			lat := taxonOf(al.Families, t.stem)
			if lat == "" || seen[lat] || containsFold(text, lat) {
				continue
			}
			seen[lat] = true
			dense, bm25 = append(dense, lat), append(bm25, lat)
			expanded = append(expanded, text[t.from:t.to]+" → "+lat)
		}
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].stem != "род" {
			continue
		}
		lat := taxonOf(al.Genera, toks[i+1].stem)
		if lat == "" || seen[lat] || containsFold(text, lat) {
			continue
		}
		seen[lat] = true
		dense, bm25 = append(dense, "род "+lat), append(bm25, lat)
		expanded = append(expanded, text[toks[i].from:toks[i+1].to]+" → род "+lat)
	}
	return dense, bm25, expanded
}

// taxonOf — латынь таксона по основе слова: точное совпадение основы, иначе
// sameStem (ключи по порядку — ответ не зависит от обхода карты).
func taxonOf(m map[string]string, st string) string {
	if v, ok := m[st]; ok {
		return v
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if sameStem(k, st) {
			return m[k]
		}
	}
	return ""
}

// genericNames — названия, которые словарь не считает видом (стоп-лист
// данными): «дикая кошка» — синоним лесного кота во вступлении его статьи,
// но в речи это «дикие кошки» вообще («доклад о диких кошках Азии»), и вид
// из такой цели уводил поиск в статью о лесном коте.
var genericNames = map[string]bool{"дикая кошка": true}

// pluralEnds — окончания множественного числа существительного в косвенных
// падежах («о манулах», «манулами», «манулов»): название вида в таком виде
// говорит о группе, а не об одном виде.
var pluralEnds = []string{"ах", "ях", "ами", "ями", "ам", "ям", "ов", "ев"}

// singular — слово не в косвенной форме множественного числа (pluralEnds).
// Именительный множественного («манулы») от родительного единственного
// («манула нет») по окончанию не отличить — его ловит только правило «ровно
// один вид» у goalSpecies.
func singular(w string) bool {
	w = strings.ToLower(w)
	for _, e := range pluralEnds {
		if strings.HasSuffix(w, e) && utf8.RuneCountInString(w)-utf8.RuneCountInString(e) >= 3 {
			return false
		}
	}
	return true
}

// goalSpecies — вид из цели задачи: ровно один вид, названный однозначно и
// в единственном числе («доклад о мануле», «про харзу»). Цель о группе
// («о диких кошках Азии», «о манулах и ирбисах»), с неоднозначным
// названием («рысь») или с несколькими видами — не даёт вида: вопрос-
// продолжение без вида ушёл бы в статью об одном из них наугад.
func goalSpecies(al *Aliases, goal string) []string {
	if al == nil || strings.TrimSpace(goal) == "" || len(al.Ambiguous(goal)) > 0 {
		return nil
	}
	ms := al.matches(goal)
	if len(ms) != 1 {
		return nil
	}
	toks := tokenize(ms[0].surface)
	if len(toks) == 0 || !singular(toks[len(toks)-1].word) {
		return nil
	}
	return []string{ms[0].canon}
}

// compareWords — маркеры сравнения (основы и слова): «он или манул»,
// «крупнее», «чем».
var compareWords = []string{"или", "чем", "крупнее", "мельче", "больше", "меньше", "тяжелее", "легче", "сравн", "отлича"}

// comparePronouns — местоимения второго участника сравнения.
var comparePronouns = map[string]bool{"он": true, "она": true, "они": true, "его": true, "ее": true, "их": true,
	"него": true, "нее": true, "них": true}

// compareNear — сколько слов между местоимением и маркером сравнения.
const compareNear = 3

// comparedSpecies — вид из контекста для сравнения «он или манул» (A-14):
// в реплике назван вид И местоимение стоит рядом с маркером сравнения —
// второй участник сравнения назван местоимением, и это вид ближайшей
// реплики контекста, которого в самой реплике нет. Без маркера сравнения
// («Где он живёт, манул?») — пусто: местоимение там и есть названный вид.
func comparedSpecies(al *Aliases, q Query, named []string) []string {
	if al == nil || len(named) == 0 || len(q.Context) == 0 {
		return nil
	}
	toks := tokenize(q.Text)
	near := false
	for i, t := range toks {
		if !comparePronouns[t.word] {
			continue
		}
		for j := max(0, i-compareNear); j <= min(len(toks)-1, i+compareNear) && !near; j++ {
			for _, w := range compareWords {
				if strings.HasPrefix(toks[j].word, w) {
					near = true
					break
				}
			}
		}
	}
	if !near {
		return nil
	}
	in := map[string]bool{}
	for _, s := range named {
		in[s] = true
	}
	terms := task.State{Terms: q.Terms}
	for i := len(q.Context) - 1; i >= 0; i-- {
		prev, _ := terms.Expand(strings.TrimSpace(q.Context[i]))
		var out []string
		for _, s := range al.Species(prev) {
			if !in[s] {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}
