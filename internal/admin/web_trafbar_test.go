package admin

import "testing"

// TestSPAStreamTrafficBar checks that a stream row of the campaign canvas draws its share of the campaign's visits
// as a bar whenever the campaign has traffic, not only while the traffic order is on, and that the bar track is
// a visible token colour rather than the near-white card tone.
func TestSPAStreamTrafficBar(t *testing.T) {
	html := spaSource()
	row := spaBetween(html, "function renderFlow(){", "const rows=flowTrafficOrder(")
	must(t, row, "${p.hits?`<span class=\"trafbar\"><i style=\"width:${Math.min(100,Math.round(sp.hits/p.hits*100))}%\"></i></span>`:''}", "bar of the stream share of visits")
	mustNot(t, row, "visitOrder", "the bar does not wait for the traffic order")
	css := spaBetween(html, ".node .trafbar{", ".canvas.compact{")
	must(t, css, "background:var(--border-strong)", "track is visible on the card in both themes")
	must(t, css, "height:4px", "bar has a readable height")
	must(t, css, "background:var(--accent)", "fill uses the accent")
}
