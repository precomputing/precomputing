package drain

import (
	"strings"
	"testing"
)

func add(p *Parser, msg string) *Cluster {
	c, _ := p.Add(strings.Fields(msg))
	return c
}

func TestGroups(t *testing.T) {
	p := New(Options{Depth: 4, Similarity: 0.5})
	a := add(p, "charge ok provider=<*> amount=<*> ms=<*>")
	b := add(p, "charge declined provider=<*> code=<*> amount=<*>")
	if a == b {
		t.Fatal("two different messages share a group")
	}
	if c := add(p, "charge ok provider=<*> amount=<*> ms=<*>"); c != a || c.N != 2 {
		t.Fatalf("the same message should join its group: %+v", c)
	}
	// A differing token becomes a wildcard in the template.
	g := add(p, "user alice logged in from web")
	add(p, "user bob logged in from web")
	if got := strings.Join(g.Template, " "); got != "user <*> logged in from web" {
		t.Fatalf("template %q", got)
	}
	// Different lengths never share a group.
	if c := add(p, "user carol logged in from the web"); c == g {
		t.Fatal("messages of different lengths share a group")
	}
	// Tokens with digits go down one wildcard branch of the tree.
	x := add(p, "retry 1 of 3 for order A1")
	if y := add(p, "retry 2 of 3 for order B7"); y != x || strings.Join(x.Template, " ") != "retry <*> of 3 for order <*>" {
		t.Fatalf("got %q", strings.Join(x.Template, " "))
	}
}

// Restoring the groups in creation order rebuilds the same tree, so later messages go where they
// would have gone had the parser never stopped.
func TestRestore(t *testing.T) {
	msgs := []string{
		"GET /a status=<*> ms=<*>", "GET /b status=<*> ms=<*>", "POST /cart status=<*> ms=<*>",
		"order placed order=<*> items=<*> total=<*>", "cache warm keys=<*>", "cache warm keys=<*> shard 2",
		"worker 3 started", "worker 4 started", "GET /c status=<*> ms=<*>", "ok",
	}
	later := []string{"GET /d status=<*> ms=<*>", "worker 9 started", "POST /pay status=<*> ms=<*>", "cache cold keys=<*>", "ok"}
	whole := New(Options{Depth: 4, Similarity: 0.5})
	for _, m := range msgs {
		add(whole, m)
	}
	restored := New(Options{Depth: 4, Similarity: 0.5})
	for _, c := range whole.Clusters() {
		restored.Restore(c.Initial, c.Template, c.N)
	}
	for _, m := range later {
		a, b := add(whole, m), add(restored, m)
		if a.ID != b.ID || strings.Join(a.Template, " ") != strings.Join(b.Template, " ") {
			t.Fatalf("%q: group %d %q against %d %q", m, a.ID, a.Template, b.ID, b.Template)
		}
	}
}
