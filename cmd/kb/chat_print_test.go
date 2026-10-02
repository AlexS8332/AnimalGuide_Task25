package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/dialogs"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/rag"
)

// Ответ, принятый без опоры (Grounded = false), — с пометками «⚠»;
// источник из прошлого хода — с пометкой; ответ с опорой — без «⚠».
func TestPrintTurnGrounded(t *testing.T) {
	v := &rag.CiteView{
		Cited: rag.Cited{Status: rag.StatusAnswered, Answer: "Харза весит до 5 кг.",
			Quotes: []rag.CitedQuote{{ChunkID: "sable/structure/003", Text: "Масса самцов соболя — от 880 до 1800 г."}}},
		Check:   rag.CiteCheck{OK: true, NumbersMissing: []string{"5"}, SpeciesMismatch: []string{"харза"}},
		Sources: []rag.CiteViewSrc{{N: 1, ChunkID: "sable/structure/003", Title: "Соболь", Issued: true, Past: true}},
	}
	var b bytes.Buffer
	printTurn(&b, dialogs.Observed{Cite: v}, nil)
	for _, want := range []string{"⚠ числа без цитаты: 5", "⚠ вид не из источника: харза", "(sable/structure/003) — из прошлого хода"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("нет %q:\n%s", want, b.String())
		}
	}
	v.Check = rag.CiteCheck{OK: true, Grounded: true}
	b.Reset()
	printTurn(&b, dialogs.Observed{Cite: v}, nil)
	if strings.Contains(b.String(), "⚠") {
		t.Fatalf("пометка при опоре:\n%s", b.String())
	}
}
