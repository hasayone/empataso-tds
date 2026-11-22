//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestOfferLineUEPCAndBots runs the line under an offer of a stream under node. The revenue of the offer divided by
// its unique clicks of the same report is uEPC; without unique clicks there is no uEPC at all (never a zero and never
// a division by zero); a stream total has none. Bots show only above zero, as an icon and a number and never as the
// word, on the offer line and on the collapsed total alike. The folded report rows give the unique clicks the way
// the report totals do. It skips when node is absent.
func TestOfferLineUEPCAndBots(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the offer line checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString(`const esc=s=>String(s==null?'':s);
const tr=k=>({'bots':'боты','Bots':'Боты','col.uepc':'uEPC','col.revenue':'Доход','col.cost':'Расход','conv':'конв.','sent on':'отправлено'}[k]||k);
const fmt=n=>String(n==null?0:n);
const icon=n=>'<svg class="ic ic-'+n+'"></svg>';
const moneyText=(n,cur,d)=>n.toFixed(d)+' $';
const normBy=b=>b||{};
const moneyMap=b=>'0.00 $';
`)
	for _, name := range []string{"streamHint", "botsBit", "offerUEPC", "branchMoney", "foldStreamOffers", "flowOfferLine"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const ok = (c, w) => { if (!c) { failed++; console.log('FAIL ' + w); } };
const eq = (a, b, w) => ok(a === b, w + ': got ' + JSON.stringify(a) + ', want ' + JSON.stringify(b));
const text = h => h.replace(/<[^>]*>/g, '');
const line = st => streamHint(flowOfferLine(st), false);

eq(offerUEPC({uniq: 100, profit: 300}), 3, 'revenue 300 over 100 unique clicks is 3');
eq(offerUEPC({uniq: 0, profit: 300}), null, 'no unique clicks, no uEPC');
eq(offerUEPC({uniq: 100, profit: 0}), 0, 'a real zero revenue is a real zero uEPC');
eq(offerUEPC({profit: 300}), null, 'a stream total has no uEPC');
eq(offerUEPC(null), null, 'no line, no uEPC');

let t1 = text(line({bots: 0, conv: 67, profit: 300, uniq: 100}));
eq(t1, 'uEPC 3.00 $ · 67 конв. · Доход 300.00 $', 'the offer line: uEPC takes the place of the bots');
ok(!/бот/i.test(t1) && !/<svg/.test(line({bots: 0, conv: 67, profit: 300, uniq: 100})), 'with no bots there is neither the word nor an icon');
let t2 = text(line({bots: 0, conv: 67, profit: 300, uniq: 0}));
eq(t2, '67 конв. · Доход 300.00 $', 'without unique clicks the line shows no uEPC and no bots');
let h3 = line({bots: 12, conv: 5, profit: 20, uniq: 10});
eq(text(h3), '12 · uEPC 2.00 $ · 5 конв. · Доход 20.00 $', 'with bots: a number, no word');
ok(h3.includes('<svg class="ic ic-bot"></svg>12'), 'the number follows a bot icon');
ok(!/боты/i.test(text(h3)) && !/боты/i.test(h3.replace(/(title|aria-label)="[^"]*"/g, '')), 'the word bots is not written, only named in the title');
let h4 = line({bots: 1, conv: 0, profit: 0, uniq: 0});
eq(text(h4), '1 · 0 конв. · Доход 0.00 $', 'one bot is a number too');

const total = streamHint({hits: 10, bots: 0, conv: 3, profit: 9}, false);
eq(text(total), '3 конв. · Доход 9.00 $', 'the collapsed total has no zero bots and no uEPC');
const totalBots = streamHint({hits: 10, bots: 4, conv: 3, profit: 9}, false);
eq(text(totalBots), '4 · 3 конв. · Доход 9.00 $', 'the collapsed total shows bots as an icon and a number');
ok(totalBots.includes('<svg') && !/uEPC/.test(totalBots), 'the total keeps the icon and gets no uEPC');
eq(streamHint({hits: 7}, true), '7 отправлено', 'a hand-off to another campaign is unchanged');

const fold = foldStreamOffers([
  {offer: 'o1', unique: 40, uniq_first: 30, bots: 1, conv: 2, profit: 6},
  {offer: 'o1', unique: 50, bots: 0, conv: 1, profit: 4},
  {offer: '', unique: 99, bots: 9, conv: 9, profit: 9},
]);
eq(fold.size, 1, 'a row without an offer is not an offer');
eq(fold.get('o1').uniq, 80, 'unique clicks are taken as the report totals take them: uniq_first when the row has it');
eq(fold.get('o1').profit, 10, 'revenue is the sum of the offer rows');
eq(offerUEPC(flowOfferLine(fold.get('o1'))), 10 / 80, 'uEPC of the folded offer');
eq(flowOfferLine(undefined).uniq, 0, 'a missing row has no unique clicks');
if (failed) process.exit(1);
`)
	file := filepath.Join(t.TempDir(), "offerline.js")
	if err := os.WriteFile(file, []byte(src.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
}

// TestOfferLineMarkup checks the pieces around the line: the offer line still draws from the stored report only,
// the loading bar stays until it arrives, the bot icon is small, and the label of uEPC is the translated column name.
func TestOfferLineMarkup(t *testing.T) {
	html := spaSource()
	stats := spaFunctionLine(html, "flowDestStatsHTML")
	must(t, stats, "if(!folded)", "the loading bar stays until the report arrives")
	must(t, stats, "streamHint(st,false)", "the offer line is drawn by the same hint")
	must(t, spaFunctionLine(html, "streamHint"), "tr('col.uepc')+' '+moneyText(uepc,'USD',2)", "the label is translated and the money is written like the revenue")
	must(t, spaFunctionLine(html, "offerUEPC"), "u>0?(+sp.profit||0)/u:null", "the formula is revenue over unique clicks, none without them")
	must(t, html, ".hint .botbit svg.ic{width:12px;height:12px}", "the bot icon is small")
	re := regexp.MustCompile(`bot:'<rect`)
	if !re.MatchString(html) {
		t.Error("the bot icon is in the icon set")
	}
}
