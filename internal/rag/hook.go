package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/agent"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/agents"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/features"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/kb"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/retrieve"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/runs"
)

// HookName — имя хука в журнале хода.
const HookName = "rag"

// leadRule — абзац системного промпта ведущего при механизме rag. Без него
// ведущий видит выдачу kb_search как свой первый шаг, но считает
// источниками только Википедию и GBIF и идёт туда по привычке: выдача
// оплачена токенами, а ответ строится мимо неё. Блоком правило быть не
// может: у механизма нет места в запросе (KindTool). Про вызов кодом
// сказано условно: правило получает и составитель подборки, а вызов кодом
// — только ведущий, и ход кнопкой его не делает. Правило — в каждом
// запросе ведущего (≈140 токенов), поэтому без повторов и без примеров
// сверх одного.
const leadRule = `База знаний (kb_search) — снимок статей справочника и MDD v2.5. Если сразу после реплики человека стоит выдача kb_search, это вызов кодом до твоего первого шага: опирайся на неё и называй фрагменты по chunk_id в квадратных скобках, например [manul/structure/004]. Если выдача не отвечает на вопрос — скажи, что в базе знаний этого нет, и при необходимости вызови kb_search сам другими словами или иди в Википедию или GBIF.`

// citeLeadRule — правило ведущего при механизме rag.cite (v24) вместо
// leadRule. Отличие от leadRule — запасного пути в Википедию и GBIF нет:
// ответ с источниками обещает человеку, что каждый факт стоит в цитате из
// базы, а статью Википедии kb_answer процитировать не даст (chunk_id
// только из выдачи kb_search); карточки и источники на таком ходе ведущему
// и не выдаются (agents.lead). Условие «если среди твоих инструментов есть
// kb_answer» — потому что правило хода получает и составитель подборки, у
// которого завершающего инструмента нет. Правило постоянное (одно и то же
// каждый ход): решение кода «только не знаю» приходит пометкой в выдаче
// kb_search и отказом kb_answer, а не правкой системного промпта — иначе
// ход с «не знаю» обнулял бы кэш префикса окна. Решается оно по всем
// вызовам kb_search хода (Hook.cite) — отсюда «другой поиск этого хода».
// Фрагменты, выданные kb_search в прошлых ходах ветки, kb_answer тоже
// принимает (v25, Hook.past) — с той же дословной сверкой цитат. Фраза о
// MDD (v25, B-3): на вопрос «по MDD» ответ опирался на статью Википедии о
// семействе, а не на фрагменты MDD.
const citeLeadRule = `База знаний (kb_search) — снимок статей справочника и MDD v2.5. Если сразу после реплики человека стоит выдача kb_search, это вызов кодом до твоего первого шага: опирайся на неё. Если среди твоих инструментов есть kb_answer — ход заканчивай только им, не текстом, и отвечай только по выдаче kb_search этого хода или прошлых ходов разговора (других источников на этом ходе нет: карточки, Википедию и GBIF не процитировать). Вопрос «по MDD» — опора на фрагменты mdd-carnivora; статьи Википедии о систематике могут отставать от релиза. Поля kb_answer:
- answer — кратко, без ссылок [chunk_id]; sources — chunk_id фрагментов из выдачи; quotes — куски текста фрагментов, скопированные ДОСЛОВНО (не короче 15 символов, пропуск — «…»); числа ответа — только из цитат;
- ответа в выдаче нет (можно сначала вызвать kb_search другими словами) — status unknown: в answer — чего нет в базе знаний, в clarify — уточняющий вопрос человеку, в sources — 1–3 ближайших найденных фрагмента;
- kb_search пометил, что релевантных фрагментов нет, — ответ по существу kb_answer примет, только если другой kb_search этого хода их найдёт;
- kb_answer вернул ошибку — исправь названное и вызови снова.`

// metaRule — правило ведущего на реплике о самом разговоре при rag.cite
// (v25, Meta): отвечать по памяти задачи и истории, а не по базе.
const metaRule = `Реплика человека — о самом разговоре (цель, договорённости, что уже выяснили), а не о животных: ответь кратко по блоку задачи разговора и истории — цель, принятые ограничения и термины, что уже разобрали. Базу знаний для этого не ищи. Если цели нет ни в блоке, ни в истории — так и скажи.`

// metaRe — признаки реплики о самом разговоре: «что мы решили», «какие
// ограничения мы приняли», «подведи итог». \b в RE2 знает только латиницу,
// поэтому границы слова — классом букв.
var metaRe = regexp.MustCompile(`(?i)(мы (уже )?(решили|договорились|выяснили|обсудили|приняли|условились)|о ч[её]м мы (говорили|договорились)|подведи итог|на ч[её]м мы остановились|что я (просил|хотел|спрашивал) в начале|какая (была|у нас) задача)`)

// goalRe — слово «цель» в любой форме. Само по себе о разговоре не
// говорит: «с какой целью ирбис метит территорию», «цель охоты волка» —
// вопросы о животных. Засчитывается только вместе с признаком разговора
// (convRe).
var goalRe = regexp.MustCompile(`(?i)(^|[^\p{L}])цел(ь|и|ью|ей|ям)([^\p{L}]|$)`)

// convRe — признак того, что речь о самом разговоре: «напомни», «наш», «у
// нас», «мы», «разговор», «задача», «договорились».
var convRe = regexp.MustCompile(`(?i)напомни|(^|[^\p{L}])(наш\p{L}*|у нас|мы)([^\p{L}]|$)|разговор|задач|договорил`)

// animalAskRe — вопрос о животном в той же реплике: «напомни цель и
// скажи, сколько весит манул» — смешанная реплика, по памяти задачи на неё
// не ответить. Вид корпуса в реплике проверяет MetaWith по словарю; здесь —
// вопросы без названия («и сколько он весит?»).
var animalAskRe = regexp.MustCompile(`(?i)сколько (он|она|оно|они)?\s*(весит|весят|живет|живёт|живут)|где (он|она|они)?\s*(живет|живёт|живут|обитает|обитают|водится|водятся)|чем (он|она|они)?\s*(питается|питаются)|какой статус|на кого (он|она|они)?\s*охот`)

// Meta — реплика о самом разговоре, а не о животных. При rag.cite такой
// ход идёт без kb_answer: в базе знаний цели разговора нет, и Gate по
// выдаче kb_search («релевантных фрагментов нет») оставил бы ведущему
// только «не знаю» — контрольная реплика «напомни цель» теряла бы цель.
// Источник такого ответа — память задачи и история (CiteView.Meta).
//
// «Цель» засчитывается только с признаком разговора (convRe); реплика с
// вопросом о животном (animalAskRe) — не о разговоре, даже если в ней
// «напомни цель». Вид, названный в реплике, проверяет MetaWith: Meta его
// не знает (словаря нет).
func Meta(text string) bool { return MetaWith(text, nil) }

// MetaWith — Meta со словарём названий: реплика, где назван вид корпуса
// («что мы уже выяснили о питании манула?»), — вопрос о животном, а не о
// разговоре. names == nil — без этой проверки.
func MetaWith(text string, names *retrieve.Aliases) bool {
	l := strings.ToLower(text)
	meta := metaRe.MatchString(l) || (goalRe.MatchString(l) && convRe.MatchString(l))
	if !meta || animalAskRe.MatchString(l) {
		return false
	}
	return names == nil || len(names.Species(text)) == 0
}

// hintNoKB — что сделать, если базы нет.
const hintNoKB = "соберите базу: go run ./cmd/kb index -strategy all (путь к другой базе — флаг -kb или KB_DB)"

// Hook — механизм rag (features.RAG): ведущий получает kb_search, а код
// вызывает его до первого запроса с репликой человека (agents.Request.
// Preload), плюс абзац правил (Request.Rules): опирайся на найденные
// фрагменты и называй их [chunk_id]; если фрагменты не отвечают на
// вопрос — так и скажи и при необходимости иди в Википедию/GBIF.
//
// Базы нет (Searcher == nil) — механизм откатывается: ход идёт без
// kb_search, в журнале причина и подсказка, а в наборе механизмов хода rag
// выключен (как у trivia). Эмбеддер не отвечает — не откат механизма:
// поиск сам уходит в BM25 и называет причину в ответе инструмента.
//
// Голые названия («манул») и «сравни» без rag.cite идут мимо ведущего
// (agents.Classify) — база там не участвует. С rag.cite (v25) маршрут
// agents.Route отдаёт ведущему любую реплику: ответ справочной — всегда с
// источниками из базы.
//
// Механизмы v23 rag.filter и rag.rewrite (требуют rag) переводят и вызов
// кодом, и сам инструмент kb_search на конвейер retrieve (PipelineTool):
// фильтр — ModeConfig(RAGFilter), переписывание — ModeConfig(RAGRewrite),
// оба — ModeConfig(RAGBoth). Контекст переписывания — прошлые реплики
// человека из окна хода (Request.Window).
//
// Механизм v24 rag.cite (требует rag): ведущий заканчивает ход только
// завершающим kb_answer (Request.Finish), выдача kb_search этого хода
// (вызов кодом и вызовы моделью) копится для проверки chunk_id и цитат, а
// «только не знаю» решает Gate по всем вызовам kb_search хода (Hook.cite).
type Hook struct {
	Searcher *kb.Searcher
	Index    string // пусто — DefaultIndex
	K        int    // 0 — DefaultK
	Why      string // почему базы нет (для журнала)
	// Pipeline — конвейер механизмов rag.filter и rag.rewrite; nil — свой
	// поверх Searcher при первом ходе (словарь названий грузится один раз).
	Pipeline *retrieve.Pipeline

	mu sync.Mutex
}

// contextTurns — сколько прошлых реплик человека получает rewrite.
const contextTurns = 3

// pipeline — конвейер хука (ленивый).
func (h *Hook) pipeline() *retrieve.Pipeline {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.Pipeline == nil {
		h.Pipeline = &retrieve.Pipeline{Searcher: h.Searcher}
	}
	return h.Pipeline
}

// mechanisms — механизмы v23 хода и настройки конвейера по ним; пусто —
// прямой поиск.
func mechanisms(t *runs.Turn) ([]string, retrieve.Config, bool) {
	filter, rewrite := t.Features.On(features.RAGFilter), t.Features.On(features.RAGRewrite)
	var names []string
	var c retrieve.Config
	switch {
	case filter && rewrite:
		names, c = []string{string(features.RAGFilter), string(features.RAGRewrite)}, ModeConfig(RAGBoth)
	case filter:
		names, c = []string{string(features.RAGFilter)}, ModeConfig(RAGFilter)
	case rewrite:
		names, c = []string{string(features.RAGRewrite)}, ModeConfig(RAGRewrite)
	default:
		return nil, c, false
	}
	return names, c, true
}

// humanTurns — прошлые реплики человека из окна хода (последние
// contextTurns), без текущей реплики.
func humanTurns(t *runs.Turn) []string {
	var out []string
	cur := strings.TrimSpace(t.Request.Text)
	for _, m := range t.Request.Window {
		if m.Role != llm.RoleUser {
			continue
		}
		if s := strings.TrimSpace(m.Content); s != "" {
			out = append(out, s)
		}
	}
	if n := len(out); n > 0 && out[n-1] == cur {
		out = out[:n-1]
	}
	if len(out) > contextTurns {
		out = out[len(out)-contextTurns:]
	}
	return out
}

func (h *Hook) Name() string { return HookName }

// Before — kb_search в инструменты хода, его вызов кодом с репликой и
// правило ведущему. Ход без реплики (кнопки карточки: раздел, узел дерева)
// получает инструмент и правило, но не вызов: искать нечем, а пустой
// запрос — это ошибка инструмента в журнале на каждом нажатии.
func (h *Hook) Before(ctx context.Context, t *runs.Turn) error {
	if !t.Features.On(features.RAG) {
		return nil
	}
	if h.Searcher == nil {
		h.off(t)
		return nil
	}
	if t.Features.On(features.RAGCite) && Meta(t.Request.Text) {
		// Вид в реплике — вопрос о животном, а не о разговоре: словарь
		// грузится только для реплик, похожих на реплику о разговоре.
		names, err := h.pipeline().Names(ctx)
		if err != nil {
			names = nil
		}
		if MetaWith(t.Request.Text, names) {
			h.meta(t)
			return nil
		}
	}
	index, k := orIndex(h.Index), orK(h.K)
	names, cfg, piped := mechanisms(t)
	// rag.cite: выдача хода копится (kb_answer проверяет по ней chunk_id и
	// цитаты), а правило ведущего — без запасного пути в Википедию.
	cite := t.Features.On(features.RAGCite)
	var obs *issued
	rule := leadRule
	if cite {
		obs, rule = &issued{}, citeLeadRule
	}
	via, detail := "", ""
	if piped {
		cfg.Index = index
		base := retrieve.Query{Context: humanTurns(t)}
		// Память задачи (v25, механизм task): термины и цель ветки — в
		// переписывание запроса. Задача хода уже с правкой извлекателя
		// (persona идёт раньше rag): «барс = ирбис», сказанное в этой же
		// реплике, работает в этом же поиске. Выключен механизм — поиск
		// прежний.
		if t.Features.On(features.Task) {
			base.Terms, base.Goal = t.Task.Terms, t.Task.Goal
		}
		t.Request.Tools = append(t.Request.Tools, pipelineTool(h.pipeline(), cfg, base, k, obs))
		via = "; " + strings.Join(names, ", ")
		detail = "\nВторой этап поиска (" + strings.Join(names, ", ") + "): " + cfg.Describe() +
			". Переписанный запрос идёт только в поиск, ведущий видит исходную реплику; в ответе kb_search — переписанный запрос и сколько фрагментов отсёк фильтр."
		if cfg.Rewrite == retrieve.RewriteCode && (len(base.Terms) > 0 || base.Goal != "") {
			detail += fmt.Sprintf("\nПамять задачи в переписывании: терминов %d, цель «%s» — вид из неё получит вопрос-продолжение, если вида нет в реплике и прошлых репликах.",
				len(base.Terms), base.Goal)
		}
	} else {
		t.Request.Tools = append(t.Request.Tools, searchTool(h.Searcher, index, k, obs))
	}
	t.Request.Rules = join(t.Request.Rules, rule)
	text := strings.TrimSpace(t.Request.Text)
	if text == "" {
		t.Em.Log(agent.Event{Agent: HookName, Kind: agent.EventMechanism, Mechanism: string(features.RAG),
			Title:  fmt.Sprintf("база знаний: kb_search выдан ведущему без вызова кодом — у хода нет реплики (индекс %s, k %d%s)", index, k, via),
			Detail: "Ход начат кнопкой, а не репликой: искать по базе нечем. Инструмент и правило у ведущего есть — вызвать kb_search он может сам." + detail})
		return nil
	}
	args, _ := json.Marshal(struct {
		Query string `json:"query"`
		K     int    `json:"k"`
	}{text, k})
	t.Request.Preload = append(t.Request.Preload, agent.Preload{Tool: ToolName, Args: string(args)})
	t.Em.Log(agent.Event{Agent: HookName, Kind: agent.EventMechanism, Mechanism: string(features.RAG), Tool: ToolName,
		Title: fmt.Sprintf("база знаний: заказан вызов kb_search кодом до первого запроса ведущего (индекс %s, k %d%s)", index, k, via),
		Detail: "Код поищет по базе знаний с репликой человека до первого запроса ведущего: выдача встанет после реплики, " +
			"а не блоком перед историей, и кэш префикса окна не сбрасывается. Ведущему выдано правило: опираться на выдачу " +
			"и называть [chunk_id]; нет ответа в выдаче — сказать об этом и при необходимости идти в Википедию или GBIF.\n" +
			"Оговорка: без rag.cite голое название животного («манул») и «сравни …» идут мимо ведущего (карточка, сравнение) — " +
			"запроса ведущего тогда нет, и вызова kb_search не будет. Сам вызов — отдельное событие журнала инструментов." + detail})
	if cite {
		h.cite(ctx, t, obs, piped, index, text)
	}
	return nil
}

// cite — механизм rag.cite: ведущий заканчивает ход только kb_answer
// (Request.Finish), проверка — по выдаче kb_search этого хода (obs).
//
// «Только не знаю» решается в момент вызова kb_answer по ВСЕМ вызовам
// kb_search хода (issued.Gate): каждый вызов несёт свой Gate — у конвейера
// (rag.filter, rag.rewrite) по его трассе, без конвейера — по трассе,
// собранной из выдачи (plainGate: косинус лучшего фрагмента против порога
// индекса, якорь — словарём названий). Все вызовы отсечены — только
// unknown; хоть один дал релевантные фрагменты — ответ по существу
// разрешён, а chunk_id и цитаты сверяются с объединённой выдачей. Поэтому
// схема kb_answer в чате без ограничения enum: решение не известно до хода
// (модель может поискать сама), запрет — отказом в Handle с подсказкой.
// Поиска до хода ради Gate нет: вызов кодом (Preload) сам несёт свой Gate,
// а ход, ушедший мимо ведущего (карточка, сравнение), ничего не тратит.
//
// Принятый ответ — в итогах хода (Extras, ключ rag.cite) для окна. Ход,
// оборвавшийся без kb_answer (ошибка протокола, предел шагов), отвечает
// «не знаю» с пометкой «не проверено» (Request.FinishFail).
func (h *Hook) cite(ctx context.Context, t *runs.Turn, obs *issued, piped bool, index, text string) {
	names, err := h.pipeline().Names(ctx)
	if err != nil {
		names = nil // без словаря — без метрики SpeciesMismatch и без порога у вызова без конвейера
	}
	gateLine := "«только не знаю» — если фильтр отсечёт всё во всех вызовах kb_search хода"
	if !piped {
		obs.plain = h.plainGate(index, names)
		gateLine = "«только не знаю» — если во всех вызовах kb_search хода вид не назван и лучший косинус ниже порога индекса (фильтра нет)"
		if names == nil {
			gateLine = "«только не знаю» — по пустой выдаче (словаря названий нет: без якоря порог не применить)"
		}
	}
	past := h.past(t)
	fin := FinisherOf(FinishOptions{Hits: obs.Hits, Gate: obs.Gate, Question: text, Names: names, Past: past.hit})
	inner := fin.Handle
	fin.Handle = func(ctx context.Context, callID string, args json.RawMessage) (any, error) {
		res, err := inner(ctx, callID, args)
		if r, ok := res.(*CitedResult); ok && err == nil {
			t.Extra(string(features.RAGCite), ViewOf(r))
			t.Extra(IssuedKey, obs.IDs())
		}
		return res, err
	}
	t.Request.Finish = append(t.Request.Finish, fin)
	t.Request.FinishFail = func(err error) agents.Texter {
		_, _, rel := obs.Gate()
		r := UnverifiedResult(err, obs.Hits(), rel)
		t.Extra(string(features.RAGCite), ViewOf(r))
		t.Extra(IssuedKey, obs.IDs())
		t.Em.Log(agent.Event{Agent: HookName, Kind: agent.EventMechanism, Mechanism: string(features.RAGCite), Tool: FinishName,
			Title:  "ответ с источниками: ход ведущего оборвался без kb_answer — человеку «не знаю» с пометкой «не проверено»",
			Detail: "Причина: " + err.Error() + ".\nПроверенного ответа нет: вместо ошибки хода — «Не знаю: " + unverifiedAnswer + "», уточняющий вопрос и ближайшее найденное в базе."})
		return r
	}
	t.Em.Log(agent.Event{Agent: HookName, Kind: agent.EventMechanism, Mechanism: string(features.RAGCite), Tool: FinishName,
		Title: "ответ с источниками: ведущий заканчивает ход только kb_answer; " + gateLine,
		Detail: "Код проверит ответ до человека: chunk_id — из выдачи kb_search этого хода" + past.line() + ", у ответа есть источник и цитата, " +
			"каждая цитата — дословный кусок своего фрагмента, числа ответа стоят в цитатах. Не прошло — отказ с подсказкой и ещё " +
			fmt.Sprintf("один запрос ведущего; после %d отказов ответ принимается с пометкой «не проверено». ", MaxRejects) +
			"Карточки, Википедия и GBIF на этом ходе ведущему не выданы: ответ — только по базе знаний."})
}

// IssuedKey — ключ итогов хода (Extras): chunk_id, которые выдал kb_search
// этого хода при rag.cite. По ним kb_answer следующих ходов ветки принимает
// источник из прошлого хода (Hook.past).
const IssuedKey = "rag.issued"

// pastIssued — фрагменты, выданные kb_search в прошлых ходах ветки.
type pastIssued struct {
	ids   map[string]bool
	store *kb.Store
}

// past — chunk_id прошлых ходов ветки: из итогов хода IssuedKey, а у ходов,
// записанных до v25, — источники ответа rag.cite, которые выдавались в своём
// ходе (CiteViewSrc.Issued). Текст фрагмента — из базы (kb.Store.Chunk):
// цитата сверяется с ним так же дословно, как с выдачей хода.
func (h *Hook) past(t *runs.Turn) *pastIssued {
	p := &pastIssued{ids: map[string]bool{}}
	if h.Searcher != nil {
		p.store = h.Searcher.Store
	}
	if t.Conv == nil {
		return p
	}
	for _, turn := range t.Conv.PathTurns(t.Branch) {
		var ids []string
		if turn.Extra(IssuedKey, &ids) {
			for _, id := range ids {
				p.ids[id] = true
			}
			continue
		}
		var v CiteView
		if turn.Extra(string(features.RAGCite), &v) {
			for _, s := range v.Sources {
				if s.Issued {
					p.ids[s.ChunkID] = true
				}
			}
		}
	}
	return p
}

// hit — фрагмент прошлого хода по chunk_id (FinishOptions.Past).
func (p *pastIssued) hit(ctx context.Context, id string) (kb.Hit, bool) {
	if p == nil || p.store == nil || !p.ids[id] {
		return kb.Hit{}, false
	}
	c, err := p.store.Chunk(ctx, id)
	if err != nil {
		return kb.Hit{}, false
	}
	return kb.Hit{Chunk: c}, true
}

// line — для журнала: сколько фрагментов прошлых ходов примет kb_answer.
func (p *pastIssued) line() string {
	if p == nil || len(p.ids) == 0 {
		return ""
	}
	return fmt.Sprintf(" или прошлых ходов ветки (%d фрагментов; цитата сверяется с текстом фрагмента из базы)", len(p.ids))
}

// plainGate — Gate вызова kb_search без конвейера: трасса из выдачи —
// лучший косинус (выдача dense), порог индекса (retrieve.MinScoreOf) и
// якорь — виды, названные в запросе (словарь названий). Без словаря —
// только пустая выдача: без якоря порог отсекал бы вопросы о названных
// видах.
func (h *Hook) plainGate(index string, names *retrieve.Aliases) func(ctx context.Context, query string, hits []kb.Hit, info kb.SearchInfo) (bool, string) {
	return func(ctx context.Context, query string, hits []kb.Hit, info kb.SearchInfo) (bool, string) {
		if names == nil || info.Mode != kb.Dense || h.Searcher == nil || h.Searcher.Store == nil {
			return Gate(nil, hits)
		}
		tr := retrieve.Trace{Original: query, Hits: hits, Info: info, Anchored: names.Species(query)}
		for _, x := range hits {
			tr.TopDense = math.Max(tr.TopDense, x.Score)
		}
		if idx, err := h.Searcher.Store.Index(ctx, orIndex(index)); err == nil {
			tr.MinScore, tr.MinScoreFrom = retrieve.MinScoreOf(retrieve.Config{}, idx)
		}
		return Gate(&tr, hits)
	}
}

// CiteView — ответ kb_answer в итогах хода (Extras, ключ rag.cite): для
// окна — чипы источников «[1] Манул › Питание», цитаты, «не знаю» и
// уточнение, итог проверки кодом.
type CiteView struct {
	Text       string        `json:"text"`
	Cited      Cited         `json:"cited"`
	Check      CiteCheck     `json:"check"`
	Gated      bool          `json:"gated,omitempty"`
	GateReason string        `json:"gate_reason,omitempty"`
	Sources    []CiteViewSrc `json:"sources,omitempty"`
	// Meta — реплика о самом разговоре (v25, Meta): ответ без kb_answer, по
	// памяти задачи и истории; MetaSource — как назвать этот источник.
	Meta       bool   `json:"meta,omitempty"`
	MetaSource string `json:"meta_source,omitempty"`
}

// CiteViewSrc — источник ответа с номером и заголовком из выдачи.
type CiteViewSrc struct {
	N       int    `json:"n"`
	ChunkID string `json:"chunk_id"`
	Title   string `json:"title,omitempty"`
	Path    string `json:"path,omitempty"`
	Issued  bool   `json:"issued"` // выдавался в этом ходе или в прошлом (Past)
	// Past — выдавался kb_search в прошлом ходе ветки, а не в этом (v25).
	Past bool `json:"past,omitempty"`
}

// ViewOf — CiteView принятого ответа. У «не знаю» источники — ближайшее
// найденное (если выдача была).
func ViewOf(r *CitedResult) CiteView {
	v := CiteView{Text: r.Text(), Cited: r.Cited, Check: r.Check, Gated: r.Check.Gated, GateReason: r.Check.GateReason}
	byID := map[string]kb.Hit{}
	for _, h := range r.Hits {
		byID[h.ID] = h
	}
	seen := map[string]bool{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		s := CiteViewSrc{N: len(v.Sources) + 1, ChunkID: id}
		if h, ok := byID[id]; ok {
			s.Title, s.Path, s.Issued, s.Past = h.Title, pathOf(h.Chunk), true, r.Past[id]
		}
		v.Sources = append(v.Sources, s)
	}
	for _, s := range r.Cited.Sources {
		add(s.ChunkID)
	}
	if !r.Cited.Unknown() {
		for _, q := range r.Cited.Quotes {
			add(q.ChunkID)
		}
	}
	return v
}

// off — базы нет: ход идёт без kb_search, в журнале причина и подсказка, в
// итоговом наборе хода механизм выключен.
func (h *Hook) off(t *runs.Turn) {
	t.Request.Features = t.Request.Features.With(features.RAG, false)
	if t.Request.Features.On(features.RAGCite) {
		// rag.cite требует rag: без базы цитировать нечего, ведущий отвечает
		// текстом.
		t.Request.Features = t.Request.Features.With(features.RAGCite, false)
	}
	why := strings.TrimSpace(h.Why)
	if why == "" {
		why = "базы знаний нет"
	}
	t.Em.Log(agent.Event{Agent: HookName, Kind: agent.EventMechanism, Mechanism: string(features.RAG),
		Title:  "база знаний: " + why + " — ход идёт без kb_search",
		Detail: "Ход идёт без kb_search: ведущий отвечает по источникам хода (Википедия, GBIF).\nЧто сделать: " + hintNoKB + "."})
}

// MetaSource — источник ответа на реплику о самом разговоре (CiteView.Meta).
const MetaSource = "память задачи и история разговора"

// meta — реплика о самом разговоре при rag.cite: без вызова kb_search
// кодом и без kb_answer, правило — ответить по памяти задачи и истории.
// В итогах хода — CiteView с Meta и источником MetaSource (текст ответа
// допишет After): окно и kb chat показывают, откуда ответ.
func (h *Hook) meta(t *runs.Turn) {
	t.Request.Rules = join(t.Request.Rules, metaRule)
	// Ответ — по памяти задачи и истории: Википедия, GBIF и карточки на
	// этом ходе ведущему не выдаются (как на ходе с kb_answer) — иначе
	// «напомни цель» уходило в источники по виду из истории.
	t.Request.NoSources = true
	t.Extra(string(features.RAGCite), CiteView{Meta: true, MetaSource: MetaSource})
	t.Em.Log(agent.Event{Agent: HookName, Kind: agent.EventMechanism, Mechanism: string(features.RAGCite),
		Title: "ответ с источниками: реплика о самом разговоре — ответ по памяти задачи и истории, без kb_answer",
		Detail: "Цели разговора и договорённостей в базе знаний нет: поиск по такой реплике не найдёт релевантных фрагментов, " +
			"и kb_answer принял бы только «не знаю». Ход идёт без вызова kb_search кодом и без завершающего kb_answer; " +
			"источник ответа — " + MetaSource + "."})
}

// After — ответ rag.cite проверяет kb_answer во время хода, а принятый
// результат пишет в Extras сам Finisher (Hook.cite). Здесь — только текст
// ответа на реплику о самом разговоре (Hook.meta).
func (h *Hook) After(ctx context.Context, t *runs.Turn) error {
	if v, ok := t.Extras[string(features.RAGCite)].(CiteView); ok && v.Meta && t.Result != nil {
		v.Text = t.Result.Text
		t.Extra(string(features.RAGCite), v)
	}
	return nil
}

func join(a, b string) string {
	switch {
	case strings.TrimSpace(a) == "":
		return b
	case strings.TrimSpace(b) == "":
		return a
	}
	return a + "\n\n" + b
}

var _ runs.Hook = (*Hook)(nil)
