package agents

import (
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/card"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/features"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm/llmtest"
)

// v25: при rag.cite голое название и «сравни» идут ведущему, без rag.cite —
// как раньше.
func TestRouteWithCite(t *testing.T) {
	reg := features.Catalog()
	base := reg.Defaults()
	cite := base.With(features.RAG, true).With(features.RAGCite, true)
	for _, text := range []string{"манул", "сравни харзу и соболя", "Рысь."} {
		if kind, _, _ := Route(text, card.State{}, cite); kind != KindMessage {
			t.Errorf("rag.cite, %q: маршрут %s, ждали ведущего", text, kind)
		}
	}
	if kind, a, _ := Route("манул", card.State{}, base); kind != KindOpen || a != "манул" {
		t.Errorf("без rag.cite голое название — карточка: %s %q", kind, a)
	}
	if kind, a, b := Route("сравни харзу и соболя", card.State{}, base); kind != KindCompare || a != "харзу" || b != "соболя" {
		t.Errorf("без rag.cite «сравни» — сравнение: %s %q %q", kind, a, b)
	}
	// rag.cite без rag (база откатилась хуком) — Validate такого не пустит,
	// но маршрут смотрит только на rag.cite: откат хука снимает оба.
	off := cite.With(features.RAG, false).With(features.RAGCite, false)
	if kind, _, _ := Route("манул", card.State{}, off); kind != KindOpen {
		t.Errorf("rag.cite откатился — карточка: %s", kind)
	}
}

// Голое название при rag.cite — ход ведущего, а не карточка: привратник и
// специалисты не зовутся.
func TestBareNameGoesToLeadWithCite(t *testing.T) {
	e := newEnv(t)
	var user string
	e.b.LeadScript = func(req llm.Request, step int) llm.Response {
		user = req.Messages[len(req.Messages)-1].Content
		return llmtest.Text("Манул — дикая кошка Азии.")
	}
	fs := e.fs.With(features.RAG, true).With(features.RAGCite, true)
	res := e.run(t, Request{Kind: KindMessage, Text: "манул", Features: fs})
	if res.Route != RouteLead || user != "манул" {
		t.Fatalf("маршрут %s, реплика ведущему %q", res.Route, user)
	}
	if e.b.Calls("gatekeeper") != 0 || len(res.Deltas) != 0 {
		t.Fatalf("карточка открывалась: привратник %d, правок %d", e.b.Calls("gatekeeper"), len(res.Deltas))
	}
	res = e.run(t, Request{Kind: KindMessage, Text: "сравни харзу и соболя по массе", Features: fs})
	if res.Route != RouteLead {
		t.Fatalf("«сравни» при rag.cite: маршрут %s", res.Route)
	}
}
