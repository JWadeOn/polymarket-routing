package simulation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/jwadeon/equinox/internal/config"
	"github.com/jwadeon/equinox/internal/matching"
	"github.com/jwadeon/equinox/internal/models"
	"github.com/jwadeon/equinox/internal/routing"
)

// ── Explorer data types (embedded as JSON in Section 0) ─────────────────────

type explorerMarket struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	YesPrice float64 `json:"yp"`
	Category string  `json:"cat,omitempty"`
}

type explorerCandidate struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Confidence float64 `json:"conf"`
	TitleScore float64 `json:"ts"`
	DateScore  float64 `json:"ds"`
	CatScore   float64 `json:"cs"`
	Inverted   bool    `json:"inv,omitempty"`
	YesPrice   float64 `json:"yp"`
}

type explorerRoutingPair struct {
	PolyID   string `json:"polyID"`
	KalshiID string `json:"kalshiID"`
	Anchor   string `json:"anchor"`
}

type explorerPayload struct {
	Markets      map[string][]explorerMarket               `json:"markets"`
	Candidates   map[string]map[string][]explorerCandidate `json:"candidates"`
	RoutingPairs []explorerRoutingPair                     `json:"routingPairs"`
}

const knownV1Limitations = `Marginal matches near the confidence threshold (0.65–0.72) may produce false positives where semantically unrelated markets share enough surface-level tokens to score above threshold. Example observed in live data: "Fed abolished before 2027?" (Polymarket) matched to KXRATECUT-26DEC31 (Kalshi) at 0.69 confidence due to shared "fed" and date proximity tokens. A price-proximity sanity check (flag matches where venue prices diverge >50 percentage points) is the recommended V2 mitigation.`

// GenerateReport builds a self-contained HTML report from the simulation results.
// decisions[i] corresponds to matches[i] for i < len(decisions).
func GenerateReport(
	decisions []models.RoutingDecision,
	matches []models.MatchResult,
	polyMarkets []models.NormalizedMarket,
	kalshiMarkets []models.NormalizedMarket,
	polyCount int,
	kalshiCount int,
) ([]byte, error) {
	var buf bytes.Buffer
	ts := time.Now().Format("Mon Jan 2, 2006 3:04:05 PM MST")

	// Build decision lookup: MarketA.InternalID → RoutingDecision
	decisionByID := make(map[string]models.RoutingDecision, len(decisions))
	for i, d := range decisions {
		if i < len(matches) {
			decisionByID[matches[i].MarketA.InternalID] = d
		}
	}

	// matches is already deduplicated and sorted by confidence descending by FindMatches/runCore.
	deduped := matches

	// Pre-compute top-8 candidates per market for the Market Explorer (Section 0).
	fmt.Printf("Pre-computing match candidates (%d×%d)...", len(polyMarkets), len(kalshiMarkets))
	t0 := time.Now()

	polyCands := make(map[string][]explorerCandidate, len(polyMarkets))
	for _, pm := range polyMarkets {
		cands := make([]explorerCandidate, 0, len(kalshiMarkets))
		for _, km := range kalshiMarkets {
			ts2, ds, cs, conf, _ := matching.ScorePair(pm, km)
			cands = append(cands, explorerCandidate{
				ID: km.InternalID, Title: km.Title,
				Confidence: conf, TitleScore: ts2, DateScore: ds, CatScore: cs,
				Inverted: matching.DetectInversion(pm, km), YesPrice: km.YesPrice,
			})
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].Confidence > cands[j].Confidence })
		if len(cands) > 8 {
			cands = cands[:8]
		}
		polyCands[pm.InternalID] = cands
	}

	kalshiCands := make(map[string][]explorerCandidate, len(kalshiMarkets))
	for _, km := range kalshiMarkets {
		cands := make([]explorerCandidate, 0, len(polyMarkets))
		for _, pm := range polyMarkets {
			ts2, ds, cs, conf, _ := matching.ScorePair(km, pm)
			cands = append(cands, explorerCandidate{
				ID: pm.InternalID, Title: pm.Title,
				Confidence: conf, TitleScore: ts2, DateScore: ds, CatScore: cs,
				Inverted: matching.DetectInversion(km, pm), YesPrice: pm.YesPrice,
			})
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].Confidence > cands[j].Confidence })
		if len(cands) > 8 {
			cands = cands[:8]
		}
		kalshiCands[km.InternalID] = cands
	}
	fmt.Printf(" done (%v)\n", time.Since(t0))

	// Build market meta lists for the explorer.
	polyMetas := make([]explorerMarket, len(polyMarkets))
	for i, pm := range polyMarkets {
		polyMetas[i] = explorerMarket{ID: pm.InternalID, Title: pm.Title, YesPrice: pm.YesPrice, Category: pm.Category}
	}
	kalshiMetas := make([]explorerMarket, len(kalshiMarkets))
	for i, km := range kalshiMarkets {
		kalshiMetas[i] = explorerMarket{ID: km.InternalID, Title: km.Title, YesPrice: km.YesPrice, Category: km.Category}
	}

	// Compute routing refs: which (polyID, kalshiID) pairs appear in the top-N Section 3 cards.
	// Mirrors the selection logic in writeSection3 so anchor IDs stay in sync.
	var routingPairs []explorerRoutingPair
	{
		shown := 0
		for _, m := range deduped {
			if shown >= topN {
				break
			}
			if _, ok := decisionByID[m.MarketA.InternalID]; !ok {
				continue
			}
			routingPairs = append(routingPairs, explorerRoutingPair{
				PolyID:   m.MarketA.InternalID,
				KalshiID: m.MarketB.InternalID,
				Anchor:   fmt.Sprintf("rd-%d", shown),
			})
			shown++
		}
	}

	exData := explorerPayload{
		Markets: map[string][]explorerMarket{
			"POLYMARKET": polyMetas,
			"KALSHI":     kalshiMetas,
		},
		Candidates: map[string]map[string][]explorerCandidate{
			"POLYMARKET": polyCands,
			"KALSHI":     kalshiCands,
		},
		RoutingPairs: routingPairs,
	}

	writeReportHead(&buf)

	fmt.Fprintf(&buf, "<body>\n<div class=\"wrap\">\n")

	// Page header
	fmt.Fprintf(&buf, `<div class="ph"><div class="ph-left"><h1>PROJECT EQUINOX</h1><div class="ph-sub">Cross-Venue Routing Simulation Report</div></div><div class="ph-right"><div class="ph-ts">%s</div><div class="ph-badge">Simulation Complete</div></div></div>`+"\n", html.EscapeString(ts))

	writeSection0(&buf, exData)
	writeSection1(&buf, polyCount, kalshiCount, len(deduped), ts)
	writeSection2(&buf, deduped, decisionByID)
	writeSection3(&buf, deduped, decisionByID)
	writeSection4(&buf, deduped)
	writeSection5(&buf)

	writeSearchJS(&buf)
	fmt.Fprintf(&buf, "</div>\n<footer><span>PROJECT EQUINOX &mdash; Go %s</span><span>All market data baked in at generation time &mdash; no external requests on load</span></footer>\n</body>\n</html>\n", html.EscapeString("1.21+"))

	return buf.Bytes(), nil
}

// ── Section 0 ──────────────────────────────────────────────────────────────

func writeSection0(buf *bytes.Buffer, exData explorerPayload) {
	polyList := exData.Markets["POLYMARKET"]
	kalshiList := exData.Markets["KALSHI"]

	fmt.Fprintf(buf, "<section>\n<h2>Market Explorer</h2>\n")
	fmt.Fprintf(buf, "<div class=\"expl-cols\">\n")

	// Left column — Polymarket
	fmt.Fprintf(buf, "<div class=\"expl-col\">\n")
	fmt.Fprintf(buf, "<h3>Polymarket markets (%d)</h3>\n", len(polyList))
	fmt.Fprintf(buf, `<input class="expl-search" data-venue="POLYMARKET" type="text" placeholder="Search Polymarket markets..." autocomplete="off">`+"\n")
	fmt.Fprintf(buf, "<div class=\"mkt-list-hdr\"><div>Title</div><div>YES</div><div>Category</div></div>\n")
	fmt.Fprintf(buf, "<div class=\"mkt-list\" id=\"expl-poly-list\">\n")
	for _, m := range polyList {
		writeMarketRow(buf, m, "POLYMARKET")
	}
	fmt.Fprintf(buf, "</div>\n</div>\n")

	// Right column — Kalshi
	fmt.Fprintf(buf, "<div class=\"expl-col\">\n")
	fmt.Fprintf(buf, "<h3>Kalshi markets (%d)</h3>\n", len(kalshiList))
	fmt.Fprintf(buf, `<input class="expl-search" data-venue="KALSHI" type="text" placeholder="Search Kalshi markets..." autocomplete="off">`+"\n")
	fmt.Fprintf(buf, "<div class=\"mkt-list-hdr\"><div>Title</div><div>YES</div><div>Category</div></div>\n")
	fmt.Fprintf(buf, "<div class=\"mkt-list\" id=\"expl-kalshi-list\">\n")
	for _, m := range kalshiList {
		writeMarketRow(buf, m, "KALSHI")
	}
	fmt.Fprintf(buf, "</div>\n</div>\n")

	fmt.Fprintf(buf, "</div>\n") // end expl-cols

	// Results panel (hidden until a market is clicked)
	fmt.Fprintf(buf, "<div class=\"expl-results\" id=\"expl-results\" style=\"display:none\">\n")
	fmt.Fprintf(buf, "<div class=\"expl-results-header\"><span class=\"expl-results-label\">Candidates &rsaquo;</span><span class=\"expl-results-title\" id=\"expl-results-title\"></span></div>\n")
	fmt.Fprintf(buf, "<table class=\"cand-tbl\">\n")
	fmt.Fprintf(buf, "<thead><tr><th>#</th><th>Candidate Title</th><th>Confidence</th><th>Score Breakdown</th><th>Polarity</th><th>Yes Price</th><th>Routing</th></tr></thead>\n")
	fmt.Fprintf(buf, "<tbody id=\"expl-cand-tbody\"></tbody>\n")
	fmt.Fprintf(buf, "</table>\n</div>\n")

	// Embed data as JSON
	jsonBytes, _ := json.Marshal(exData)
	fmt.Fprintf(buf, "<script>\nwindow.EXPLORER_DATA=%s;\n</script>\n", jsonBytes)

	// Explorer JS
	fmt.Fprint(buf, `<script>
(function(){
  var D=window.EXPLORER_DATA;
  if(!D)return;

  // Build routing lookup: "polyID|||kalshiID" → anchor
  var RL={};
  (D.routingPairs||[]).forEach(function(rp){RL[rp.polyID+'|||'+rp.kalshiID]=rp.anchor;});

  // Per-column search
  document.querySelectorAll('.expl-search').forEach(function(inp){
    inp.addEventListener('input',function(){
      var v=this.dataset.venue, q=this.value.toLowerCase();
      document.querySelectorAll('.mrow[data-venue="'+v+'"]').forEach(function(r){
        r.style.display=(!q||(r.dataset.search||'').indexOf(q)!==-1)?'':'none';
      });
    });
    inp.addEventListener('keydown',function(e){
      if(e.key==='Escape'){this.value='';this.dispatchEvent(new Event('input'));}
    });
  });

  // Badge helper
  function confBadge(c){
    var cls=c>=0.80?'conf-high':c>=0.65?'conf-mid':'conf-low';
    return '<div class="conf-bar-wrap '+cls+'"><div class="conf-bar-track"><div class="conf-bar-fill" style="width:'+Math.round(c*100)+'%"></div></div><span class="conf-pct">'+c.toFixed(2)+'</span></div>';
  }

  // Click handler
  document.querySelectorAll('.mrow').forEach(function(row){
    row.addEventListener('click',function(){
      document.querySelectorAll('.mrow.msel').forEach(function(r){r.classList.remove('msel');});
      this.classList.add('msel');
      var venue=this.dataset.venue, id=this.dataset.id, title=this.dataset.title;
      showCandidates(venue,id,title);
    });
  });

  function showCandidates(venue,id,title){
    var otherVenue=venue==='POLYMARKET'?'KALSHI':'POLYMARKET';
    var cands=((D.candidates[venue]||{})[id])||[];
    var panel=document.getElementById('expl-results');
    document.getElementById('expl-results-title').textContent=title;
    var tbody=document.getElementById('expl-cand-tbody');
    tbody.innerHTML='';
    if(cands.length===0){
      var tr=document.createElement('tr');
      tr.innerHTML='<td colspan="7" style="text-align:center;color:var(--muted);padding:14px;font-style:italic">No candidates scored.</td>';
      tbody.appendChild(tr);
    } else {
      cands.forEach(function(c,idx){
        var aKey=venue==='POLYMARKET'?(id+'|||'+c.id):(c.id+'|||'+id);
        var anchor=RL[aKey];
        var tr=document.createElement('tr');
        if(c.conf>=0.65)tr.className='cmatch';
        var truncTitle=c.title.length>55?c.title.slice(0,52)+'\u2026':c.title;
        var btnHTML=anchor
          ?'<button class="btn-route" onclick="(function(){var el=document.getElementById(\''+anchor+'\');if(el){el.scrollIntoView({behavior:\'smooth\',block:\'center\'});el.style.outline=\'2px solid #16a34a\';setTimeout(function(){el.style.outline=\'\';},2000);}})()">View \u2197</button>'
          :'<button class="btn-route" disabled>View \u2197</button>';
        tr.innerHTML='<td style="color:var(--muted);font-weight:600">'+(idx+1)+'</td>'
          +'<td title="'+c.title.replace(/"/g,'&quot;')+'">'+truncTitle+'</td>'
          +'<td>'+confBadge(c.conf)+'</td>'
          +'<td><span class="pill">T:'+c.ts.toFixed(2)+'</span> <span class="pill">D:'+c.ds.toFixed(2)+'</span> <span class="pill">C:'+c.cs.toFixed(2)+'</span></td>'
          +'<td>'+(c.inv?'<span class="badge ba">Inverted</span>':'<span class="badge bx">Aligned</span>')+'</td>'
          +'<td>'+c.yp.toFixed(2)+'</td>'
          +'<td>'+btnHTML+'</td>';
        tbody.appendChild(tr);
      });
    }
    panel.style.display='block';
    requestAnimationFrame(function(){requestAnimationFrame(function(){panel.style.opacity='1';});});
  }
})();
</script>
`)

	fmt.Fprintf(buf, "</section>\n")
}

// writeMarketRow renders one row in an explorer market list.
func writeMarketRow(buf *bytes.Buffer, m explorerMarket, venue string) {
	title := m.Title
	if len(title) > 55 {
		title = title[:52] + "…"
	}
	searchVal := strings.ToLower(m.Title + " " + m.ID)
	cat := m.Category
	fmt.Fprintf(buf,
		"<div class=\"mrow\" data-venue=\"%s\" data-id=\"%s\" data-title=\"%s\" data-search=\"%s\">\n",
		html.EscapeString(venue),
		html.EscapeString(m.ID),
		html.EscapeString(m.Title),
		html.EscapeString(searchVal),
	)
	fmt.Fprintf(buf, "<span class=\"mrow-ttl\">%s</span>\n", html.EscapeString(title))
	fmt.Fprintf(buf, "<span class=\"mrow-px\">%.2f</span>\n", m.YesPrice)
	fmt.Fprintf(buf, "<span class=\"mrow-cat\">%s</span>\n", html.EscapeString(cat))
	fmt.Fprintf(buf, "</div>\n")
}

func writeSearchJS(buf *bytes.Buffer) {
	fmt.Fprint(buf, `<script>
(function(){
  var inp = document.getElementById('market-search');
  if (!inp) return;
  var rows = document.querySelectorAll('details.prow');
  var cards = document.querySelectorAll('.routing-card');
  var noRow = document.getElementById('no-match-row');
  function run() {
    var q = inp.value.toLowerCase();
    var n = 0;
    rows.forEach(function(r) {
      var ok = !q || (r.dataset.search || '').indexOf(q) !== -1;
      r.style.display = ok ? '' : 'none';
      if (ok) n++;
    });
    if (noRow) {
      noRow.style.display = (q && n === 0) ? '' : 'none';
      if (q && n === 0) noRow.textContent = 'No matches for \u201c' + inp.value + '\u201d';
    }
    cards.forEach(function(c) {
      var ok = !q || (c.dataset.search || '').indexOf(q) !== -1;
      c.style.display = ok ? '' : 'none';
    });
  }
  inp.addEventListener('input', run);
  inp.addEventListener('keydown', function(e) {
    if (e.key === 'Escape') { inp.value = ''; run(); }
  });
})();
</script>
`)
}

func writeReportHead(buf *bytes.Buffer) {
	fmt.Fprint(buf, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1.0">
<title>Project Equinox — Routing Report</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800;900&family=JetBrains+Mono:wght@400;500;600;700&display=swap" rel="stylesheet">
<style>
*{box-sizing:border-box;margin:0;padding:0}
:root{
  --bg:#020617;--bg1:#0a0f1a;--bg2:#0f172a;
  --card:#111827;--card2:#1e293b;
  --bdr:rgba(255,255,255,0.07);--bdr2:rgba(255,255,255,0.14);
  --tx:#f1f5f9;--tx2:#94a3b8;--tx3:#475569;
  --p:#0062ff;--pbg:rgba(0,98,255,0.08);--pbdr:rgba(0,98,255,0.28);
  --g:#10b981;--gbg:rgba(16,185,129,0.10);--gbdr:rgba(16,185,129,0.25);
  --a:#f59e0b;--abg:rgba(245,158,11,0.10);--abdr:rgba(245,158,11,0.30);
  --r:#ef4444;--rbg:rgba(239,68,68,0.10);--rbdr:rgba(239,68,68,0.25);
  --mono:#0f172a;
  --R:10px;
  --ff:'Inter',sans-serif;
  --fm:'JetBrains Mono',monospace;
}
html{scroll-behavior:smooth}
body{font-family:var(--ff);background:var(--bg);color:var(--tx);font-size:13px;line-height:1.6;min-height:100vh}
body::before{content:'';position:fixed;inset:0;pointer-events:none;z-index:0;background-image:radial-gradient(circle,rgba(255,255,255,0.035) 1px,transparent 1px);background-size:28px 28px}
body::after{content:'';position:fixed;top:0;left:0;right:0;height:2px;z-index:100;background:linear-gradient(90deg,transparent 0%,var(--p) 35%,var(--g) 70%,transparent 100%);opacity:0.8}
.wrap{max-width:1200px;margin:0 auto;padding:44px 28px;position:relative;z-index:1}

/* ── Page header ── */
.ph{margin-bottom:52px;padding-bottom:28px;border-bottom:1px solid var(--bdr);display:flex;align-items:flex-end;justify-content:space-between;gap:20px}
.ph-left h1{font-size:48px;font-weight:800;line-height:1;letter-spacing:-2px;background:linear-gradient(125deg,#e2e8f0 0%,var(--p) 55%,var(--g) 100%);-webkit-background-clip:text;-webkit-text-fill-color:transparent;background-clip:text}
.ph-sub{font-family:var(--fm);font-size:10px;letter-spacing:2px;text-transform:uppercase;color:var(--tx2);margin-top:10px}
.ph-right{text-align:right;flex-shrink:0}
.ph-ts{font-family:var(--fm);font-size:10px;color:var(--tx3)}
.ph-badge{display:inline-flex;align-items:center;gap:7px;margin-top:8px;padding:5px 14px;background:var(--gbg);border:1px solid var(--gbdr);border-radius:999px;font-family:var(--fm);font-size:9px;font-weight:700;letter-spacing:2px;text-transform:uppercase;color:var(--g)}
.ph-badge::before{content:'';width:6px;height:6px;background:var(--g);border-radius:50%;box-shadow:0 0 8px var(--g);animation:pulse 2.2s ease-in-out infinite}
@keyframes pulse{0%,100%{opacity:1;transform:scale(1)}50%{opacity:0.3;transform:scale(0.6)}}

/* ── Sections ── */
section{margin-bottom:52px}
h2{display:flex;align-items:center;gap:10px;font-family:var(--fm);font-size:9px;font-weight:700;letter-spacing:2.5px;text-transform:uppercase;color:var(--tx3);margin-bottom:20px}
h2::before{content:'';display:inline-block;width:2px;height:13px;background:var(--p);border-radius:1px;flex-shrink:0;box-shadow:0 0 6px var(--p)}
h2::after{content:'';flex:1;height:1px;background:linear-gradient(90deg,var(--bdr),transparent)}
h3{font-family:var(--ff);font-size:13px;font-weight:600;margin-bottom:10px;color:var(--tx)}

/* ── Metric cards ── */
.cards{display:grid;grid-template-columns:repeat(4,1fr);gap:12px}
.mcard{background:var(--card);border:1px solid var(--bdr);border-radius:var(--R);padding:20px;position:relative;overflow:hidden;transition:border-color 0.2s,transform 0.15s,box-shadow 0.2s}
.mcard::after{content:'';position:absolute;bottom:0;left:0;right:0;height:2px;background:linear-gradient(90deg,transparent,var(--p),transparent);opacity:0;transition:opacity 0.2s}
.mcard:hover{border-color:rgba(0,98,255,0.35);transform:translateY(-2px);box-shadow:0 8px 32px rgba(0,0,0,0.5)}
.mcard:hover::after{opacity:0.7}
.mval{font-family:var(--ff);font-size:36px;font-weight:800;color:var(--tx);line-height:1;letter-spacing:-1px}
.mlbl{font-family:var(--fm);font-size:9px;letter-spacing:1.5px;text-transform:uppercase;color:var(--tx3);margin-top:9px}

/* ── General card ── */
.card{background:var(--card);border:1px solid var(--bdr);border-radius:var(--R);padding:20px;margin-bottom:16px}
.card-title{font-size:14px;font-weight:600;margin-bottom:4px}
.card-meta{font-family:var(--fm);font-size:10px;color:var(--tx2);margin-top:3px}

/* ── Badges ── */
.badge{display:inline-flex;align-items:center;padding:3px 9px;border-radius:6px;font-size:10px;font-weight:700;font-family:var(--fm);white-space:nowrap;letter-spacing:0.5px;text-transform:uppercase}
.bg{background:var(--gbg);color:var(--g);border:1px solid var(--gbdr)}
.bp{background:var(--pbg);color:var(--p);border:1px solid var(--pbdr)}
.ba{background:var(--abg);color:var(--a);border:1px solid var(--abdr)}
.br{background:var(--rbg);color:var(--r);border:1px solid var(--rbdr)}
.bx{background:rgba(255,255,255,0.04);color:var(--tx2);border:1px solid var(--bdr)}

/* ── Confidence bar ── */
.conf-bar-wrap{display:flex;align-items:center;gap:8px;min-width:120px}
.conf-bar-track{flex:1;height:5px;background:rgba(255,255,255,0.06);border-radius:999px;overflow:hidden}
.conf-bar-fill{height:100%;border-radius:999px}
.conf-high .conf-bar-fill{background:var(--g)}
.conf-mid  .conf-bar-fill{background:var(--a)}
.conf-low  .conf-bar-fill{background:var(--r)}
.conf-pct{font-family:var(--fm);font-size:11px;font-weight:700;min-width:34px;text-align:right}
.conf-high .conf-pct{color:var(--g)}
.conf-mid  .conf-pct{color:var(--a)}
.conf-low  .conf-pct{color:var(--r)}

/* ── Pairs table ── */
.ptbl{border:1px solid var(--bdr);border-radius:var(--R);overflow:hidden}
.pcols{display:grid;grid-template-columns:minmax(150px,2.5fr) 140px 88px 168px 80px 108px 88px;gap:8px;padding:0 16px;align-items:center;font-size:12px;min-height:42px}
.phdr{background:var(--bg2);font-family:var(--fm);font-weight:700;color:var(--tx3);font-size:9px;letter-spacing:1.5px;text-transform:uppercase;border-bottom:1px solid var(--bdr);min-height:36px}
details.prow{border-bottom:1px solid var(--bdr)}
details.prow:last-child{border-bottom:none}
details.prow>summary{list-style:none;cursor:pointer;display:block}
details.prow>summary::-webkit-details-marker{display:none}
details.prow>summary:hover .pcols{background:rgba(0,98,255,0.04)}
.ptbl>details:nth-child(odd)>summary>.pcols{background:var(--card)}
.ptbl>details:nth-child(even)>summary>.pcols{background:var(--bg2)}
details.prow[open]>summary>.pcols{background:rgba(0,98,255,0.07)!important}
.pexp{padding:20px;background:var(--bg1);border-top:1px solid var(--bdr)}

/* ── Score pills ── */
.pills{display:flex;gap:4px;flex-wrap:wrap}
.pill{padding:2px 7px;border-radius:4px;font-size:10px;font-weight:600;font-family:var(--fm);background:rgba(255,255,255,0.04);color:var(--tx2);border:1px solid var(--bdr)}

/* ── Venue panels (routing cards) ── */
.venue-panels{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin:16px 0}
.venue-panel{background:var(--bg2);border:1px solid var(--bdr);border-radius:var(--R);padding:20px;position:relative;overflow:hidden}
.venue-panel.vsel{border-color:var(--p);background:rgba(0,98,255,0.06);box-shadow:0 0 0 1px var(--p) inset, 0 4px 24px rgba(0,98,255,0.12)}
.venue-panel.vrej{opacity:0.65}
.vp-badge{position:absolute;top:0;right:0;padding:4px 12px;background:var(--p);color:#fff;font-family:var(--fm);font-size:9px;font-weight:700;letter-spacing:1.5px;text-transform:uppercase;border-bottom-left-radius:8px}
.vp-header{display:flex;justify-content:space-between;align-items:flex-start;margin-bottom:16px}
.vp-name{font-family:var(--fm);font-size:9px;font-weight:700;letter-spacing:2px;text-transform:uppercase;color:var(--tx3);margin-bottom:4px}
.vp-name.sel{color:var(--p)}
.vp-price{font-family:var(--ff);font-size:30px;font-weight:800;letter-spacing:-1px;color:var(--tx)}
.vp-price-lbl{font-family:var(--fm);font-size:9px;color:var(--tx3);text-transform:uppercase;letter-spacing:1px;margin-top:2px}
.vp-rows{border-top:1px solid var(--bdr);padding-top:12px;display:flex;flex-direction:column;gap:9px}
.vp-row{display:flex;justify-content:space-between;align-items:center}
.vp-row-lbl{font-family:var(--fm);font-size:10px;color:var(--tx3);text-transform:uppercase;letter-spacing:0.5px}
.vp-row-val{font-family:var(--fm);font-size:11px;font-weight:600;color:var(--tx)}
.vp-row-val.sel{color:var(--p)}

/* ── Reasoning trail ── */
.reasoning-trail{margin-top:16px;padding:16px;background:var(--bg);border:1px solid var(--bdr);border-radius:var(--R)}
.rt-header{font-family:var(--fm);font-size:9px;font-weight:700;letter-spacing:2px;text-transform:uppercase;color:var(--tx3);margin-bottom:12px;display:flex;align-items:center;gap:8px}
.rt-header::after{content:'';flex:1;height:1px;background:var(--bdr)}
.rt-step{display:flex;gap:12px;align-items:flex-start;padding-bottom:10px}
.rt-step:last-child{padding-bottom:0}
.rt-dot{width:8px;height:8px;border-radius:50%;margin-top:4px;flex-shrink:0;background:var(--tx3)}
.rt-dot.ok{background:var(--g);box-shadow:0 0 6px rgba(16,185,129,0.5)}
.rt-dot.warn{background:var(--a)}
.rt-dot.err{background:var(--r)}
.rt-text{font-family:var(--fm);font-size:11px;color:var(--tx2);line-height:1.6}

/* ── Decision block ── */
.dblk{margin-top:16px;padding:14px 16px;background:var(--bg);border-radius:var(--R);border:1px solid var(--bdr)}
.dvenue{font-family:var(--ff);font-size:14px;font-weight:700;letter-spacing:1px;text-transform:uppercase;display:inline-block;padding:6px 16px;border-radius:var(--R);background:var(--gbg);color:var(--g);border:1px solid var(--gbdr);margin-bottom:10px}
.dvenue-rej{background:var(--rbg);color:var(--r);border-color:var(--rbdr)}
.dline{font-family:var(--fm);font-size:11px;color:var(--tx2);margin:3px 0}

/* ── Mono block ── */
.mono{background:var(--mono);border-radius:var(--R);padding:12px 14px;font-family:var(--fm);font-size:11px;overflow-x:auto;white-space:pre-wrap;word-break:break-word;border:1px solid var(--bdr);color:var(--tx2);line-height:1.7}

/* ── Two-column ── */
.twocol{display:grid;grid-template-columns:1fr 1fr;gap:16px}

/* ── Config table ── */
.ctbl{width:100%;border-collapse:collapse;font-size:12px}
.ctbl td{padding:6px 8px;border-bottom:1px solid var(--bdr);font-family:var(--fm);font-size:11px}
.ctbl td:first-child{color:var(--tx2);width:65%}
.ctbl td:last-child{color:var(--p);font-weight:600}
.ctbl tr:last-child td{border-bottom:none}

/* ── Venue comparison table (legacy, used in pexp fallback) ── */
.vtbl{width:100%;border-collapse:collapse;font-size:12px;margin:12px 0}
.vtbl th{text-align:left;padding:8px 10px;background:var(--bg);font-family:var(--fm);font-weight:700;color:var(--tx3);font-size:9px;letter-spacing:1.5px;text-transform:uppercase;border-bottom:1px solid var(--bdr)}
.vtbl td{padding:8px 10px;border-bottom:1px solid var(--bdr);background:var(--card);font-family:var(--fm);font-size:11px}
.vtbl tr:last-child td{border-bottom:none}
.vtbl tr.vsel td{border-left:2px solid var(--p)}
.vtbl tr.vsel td:first-child{padding-left:8px}

/* ── Data dots ── */
.dot{display:inline-block;width:6px;height:6px;border-radius:50%;margin-right:4px;vertical-align:middle}
.dg{background:var(--g)}.da{background:var(--a)}.dr{background:var(--r)}

/* ── Footer ── */
footer{margin-top:64px;padding-top:20px;border-top:1px solid var(--bdr);font-family:var(--fm);font-size:10px;color:var(--tx3);display:flex;justify-content:space-between;align-items:center;letter-spacing:0.5px}

/* ── Routing card header ── */
.rcard-hdr{margin-bottom:14px}
.rcard-hdr strong{font-size:15px;font-weight:700;color:var(--tx)}
.rcard-meta{font-family:var(--fm);font-size:10px;color:var(--tx2);margin-top:4px;letter-spacing:0.3px}
.conf-row{display:flex;align-items:center;gap:8px;margin-top:8px;flex-wrap:wrap}

/* ── Market search ── */
#market-search{width:100%;padding:10px 14px;font-size:13px;font-family:var(--ff);border:1px solid var(--bdr);border-radius:var(--R);background:var(--card);color:var(--tx);margin-bottom:14px;outline:none;display:block;transition:border-color 0.2s,box-shadow 0.2s}
#market-search:focus{border-color:var(--p);box-shadow:0 0 0 3px rgba(0,98,255,0.12)}
#market-search::placeholder{color:var(--tx3)}
.no-match{padding:20px;text-align:center;color:var(--tx3);font-family:var(--fm);font-size:11px;display:none}

/* ── Market Explorer ── */
.expl-cols{display:grid;grid-template-columns:1fr 1fr;gap:16px}
.expl-col h3{font-family:var(--fm);font-size:9px;font-weight:700;letter-spacing:2px;text-transform:uppercase;color:var(--tx3);margin-bottom:10px}
.expl-search{width:100%;padding:8px 12px;font-size:12px;font-family:var(--ff);border:1px solid var(--bdr);border-radius:var(--R);background:var(--card);color:var(--tx);margin-bottom:8px;outline:none;display:block;transition:border-color 0.2s,box-shadow 0.2s}
.expl-search:focus{border-color:var(--p);box-shadow:0 0 0 3px rgba(0,98,255,0.12)}
.expl-search::placeholder{color:var(--tx3)}
.mkt-list{max-height:420px;overflow-y:auto;border:1px solid var(--bdr);border-radius:var(--R);background:var(--card);scrollbar-width:thin;scrollbar-color:var(--bdr2) transparent}
.mkt-list::-webkit-scrollbar{width:3px}
.mkt-list::-webkit-scrollbar-track{background:transparent}
.mkt-list::-webkit-scrollbar-thumb{background:var(--bdr2);border-radius:3px}
.mkt-list-hdr{display:grid;grid-template-columns:1fr 56px 68px;gap:6px;padding:6px 12px;background:var(--bg2);border-bottom:1px solid var(--bdr);font-family:var(--fm);font-size:9px;font-weight:700;letter-spacing:1.5px;text-transform:uppercase;color:var(--tx3)}
.mrow{display:grid;grid-template-columns:1fr 56px 68px;gap:6px;padding:8px 12px;cursor:pointer;font-size:11px;border-left:2px solid transparent;border-bottom:1px solid var(--bdr);transition:background 0.1s,border-left-color 0.15s;align-items:center}
.mrow:last-child{border-bottom:none}
.mrow:hover{background:rgba(0,98,255,0.05);border-left-color:rgba(0,98,255,0.35)}
.mrow.msel{border-left-color:var(--p);background:rgba(0,98,255,0.09)}
.mrow-ttl{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--tx)}
.mrow-px{font-family:var(--fm);font-size:10px;padding:2px 6px;border-radius:4px;background:rgba(255,255,255,0.04);color:var(--p);border:1px solid var(--bdr);text-align:right}
.mrow-cat{font-family:var(--fm);font-size:9px;font-weight:700;letter-spacing:0.5px;text-transform:uppercase;color:var(--tx3);padding:2px 5px;border-radius:4px;border:1px solid var(--bdr);background:var(--bg2);text-align:center;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.expl-results{margin-top:20px;padding-top:20px;border-top:1px solid var(--bdr);opacity:0;transition:opacity 200ms ease}
.expl-results-header{display:flex;align-items:center;gap:10px;margin-bottom:10px}
.expl-results-label{font-family:var(--fm);font-size:9px;letter-spacing:2px;text-transform:uppercase;color:var(--p);flex-shrink:0}
.expl-results-title{font-family:var(--ff);font-size:14px;font-weight:700;color:var(--tx);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.cand-tbl{width:100%;border-collapse:collapse;font-size:12px;margin-top:14px}
.cand-tbl th{text-align:left;padding:8px 10px;background:var(--bg2);font-family:var(--fm);font-weight:700;color:var(--tx3);border-bottom:1px solid var(--bdr);font-size:9px;letter-spacing:1.5px;text-transform:uppercase}
.cand-tbl td{padding:8px 10px;border-bottom:1px solid var(--bdr);background:var(--card);vertical-align:middle}
.cand-tbl tr:last-child td{border-bottom:none}
.cand-tbl tr.cmatch td{border-left:2px solid rgba(0,98,255,0.45)}
.cand-tbl tr:hover td{background:var(--bg2)}
.btn-route{font-family:var(--fm);font-size:9px;padding:3px 10px;border-radius:6px;border:1px solid var(--pbdr);background:var(--pbg);color:var(--p);cursor:pointer;font-weight:700;letter-spacing:1px;text-transform:uppercase;transition:background 0.15s,box-shadow 0.15s}
.btn-route:hover:not(:disabled){background:rgba(0,98,255,0.18);box-shadow:0 0 8px rgba(0,98,255,0.3)}
.btn-route:disabled{background:rgba(255,255,255,0.03);color:var(--tx3);border-color:var(--bdr);cursor:default}
</style>
</head>
`)
}

// ── Section 1 ──────────────────────────────────────────────────────────────

func writeSection1(buf *bytes.Buffer, polyCount, kalshiCount, matchCount int, ts string) {
	fmt.Fprintf(buf, "<section>\n<h2>Run Summary</h2>\n<div class=\"cards\">\n")

	fmt.Fprintf(buf, `<div class="mcard"><div class="mval">%d</div><div class="mlbl">Polymarket markets fetched</div></div>`, polyCount)
	fmt.Fprintf(buf, `<div class="mcard"><div class="mval">%d</div><div class="mlbl">Kalshi markets fetched</div></div>`, kalshiCount)
	fmt.Fprintf(buf, `<div class="mcard"><div class="mval">%d</div><div class="mlbl">Matched pairs found (confidence &ge; %.2f)</div></div>`, matchCount, config.MatchConfidenceThreshold)
	fmt.Fprintf(buf, `<div class="mcard"><div class="mval" style="font-size:14px;line-height:1.4">%s</div><div class="mlbl">Run timestamp</div></div>`, html.EscapeString(ts))

	fmt.Fprintf(buf, "\n</div>\n</section>\n")
}

// ── Section 2 ──────────────────────────────────────────────────────────────

func writeSection2(buf *bytes.Buffer, matches []models.MatchResult, decisionByID map[string]models.RoutingDecision) {
	fmt.Fprintf(buf, "<section>\n<h2>Matched Pairs</h2>\n")

	if len(matches) == 0 {
		fmt.Fprintf(buf, `<p style="color:var(--muted)">No matched pairs found.</p>`)
		fmt.Fprintf(buf, "\n</section>\n")
		return
	}

	fmt.Fprint(buf, `<input id="market-search" type="text" placeholder="Search matched markets..." autocomplete="off">`+"\n")

	fmt.Fprintf(buf, "<div class=\"ptbl\">\n")

	// Header row — 7 columns: Title | Confidence | Resolution | Score Breakdown | Polarity | Venue | Eff. Price
	fmt.Fprintf(buf, `<div class="pcols phdr"><div>Title (Polymarket)</div><div>Confidence</div><div>Resolution</div><div>Score Breakdown</div><div>Polarity</div><div>Venue</div><div>Eff. Price</div></div>`)
	fmt.Fprintf(buf, "\n")

	for _, match := range matches {
		var dec *models.RoutingDecision
		if d, ok := decisionByID[match.MarketA.InternalID]; ok {
			dec = &d
		}

		// Resolution date
		resDt := "—"
		if !match.MarketA.ResolutionTime.IsZero() {
			resDt = match.MarketA.ResolutionTime.Format("Jan 2, 2006")
		}

		// Polarity badge
		polarityBadge := `<span class="badge bx">Aligned</span>`
		if match.IsPolarityInverted {
			polarityBadge = `<span class="badge ba">Inverted</span>`
		}

		// Confidence badge with bar
		conf := match.Confidence
		confClass := "conf-high"
		if conf < 0.80 {
			confClass = "conf-mid"
		}
		if conf < 0.65 {
			confClass = "conf-low"
		}
		confBadge := fmt.Sprintf(
			`<div class="conf-bar-wrap %s"><div class="conf-bar-track"><div class="conf-bar-fill" style="width:%.0f%%"></div></div><span class="conf-pct">%.2f</span></div>`,
			confClass, conf*100, conf)

		// Selected venue and effective price
		venueBadge := `<span class="badge bx">—</span>`
		effPrice := "—"
		if dec != nil {
			if dec.FillStatus == "REJECTED" {
				venueBadge = `<span class="badge br">REJECTED</span>`
			} else if dec.SelectedVenue != "" {
				venueBadge = fmt.Sprintf(`<span class="badge bg">%s</span>`, html.EscapeString(dec.SelectedVenue))
				effPrice = fmt.Sprintf("%.4f", dec.EffectivePrice)
			}
		}

		title := match.MarketA.Title
		if title == "" {
			title = match.MarketB.Title
		}
		if len(title) > 60 {
			title = title[:57] + "…"
		}

		searchVal := strings.ToLower(match.MarketA.Title + " " + match.MarketB.Title + " " +
			match.MarketA.InternalID + " " + match.MarketB.InternalID + " " +
			match.MarketA.VenueID + " " + match.MarketB.VenueID)
		fmt.Fprintf(buf, "<details class=\"prow\" data-search=\"%s\">\n", html.EscapeString(searchVal))
		fmt.Fprintf(buf, "<summary>\n")
		fmt.Fprintf(buf, "<div class=\"pcols\">\n")
		fmt.Fprintf(buf, "<div>%s</div>\n", html.EscapeString(title))
		fmt.Fprintf(buf, "<div>%s</div>\n", confBadge)
		fmt.Fprintf(buf, "<div>%s</div>\n", html.EscapeString(resDt))
		fmt.Fprintf(buf, "<div><div class=\"pills\">")
		fmt.Fprintf(buf, `<span class="pill">T:%.2f</span>`, match.TitleScore)
		fmt.Fprintf(buf, `<span class="pill">D:%.2f</span>`, match.DateScore)
		fmt.Fprintf(buf, `<span class="pill">C:%.2f</span>`, match.CategoryScore)
		fmt.Fprintf(buf, "</div></div>\n")
		fmt.Fprintf(buf, "<div>%s</div>\n", polarityBadge)
		fmt.Fprintf(buf, "<div>%s</div>\n", venueBadge)
		fmt.Fprintf(buf, "<div>%s</div>\n", html.EscapeString(effPrice))
		fmt.Fprintf(buf, "</div>\n</summary>\n")

		// Expanded content
		fmt.Fprintf(buf, "<div class=\"pexp\">\n")
		if dec != nil {
			writeRoutingCard(buf, match, *dec)
		} else {
			fmt.Fprintf(buf, "<p style=\"font-size:12px;color:var(--muted)\">Routing not computed — outside top-%d by confidence (%.2f). Match: Polymarket <code>%s</code> ↔ Kalshi <code>%s</code></p>\n",
				maxRoute,
				match.Confidence,
				html.EscapeString(match.MarketA.InternalID),
				html.EscapeString(match.MarketB.InternalID),
			)
		}
		fmt.Fprintf(buf, "</div>\n</details>\n")
	}

	fmt.Fprint(buf, `<div id="no-match-row" class="no-match"></div>`+"\n")
	fmt.Fprintf(buf, "</div>\n</section>\n")
}

// ── Section 3 ──────────────────────────────────────────────────────────────

func writeSection3(buf *bytes.Buffer, matches []models.MatchResult, decisionByID map[string]models.RoutingDecision) {
	fmt.Fprintf(buf, "<section>\n<h2>Top %d Routing Decision Cards</h2>\n", topN)

	if len(matches) == 0 || len(decisionByID) == 0 {
		fmt.Fprintf(buf, `<p style="color:var(--muted)">No routing decisions to display.</p>`)
		fmt.Fprintf(buf, "\n</section>\n")
		return
	}

	// Show only the top topN by confidence (matches is already sorted desc)
	shown := 0
	for _, match := range matches {
		if shown >= topN {
			break
		}
		dec, ok := decisionByID[match.MarketA.InternalID]
		if !ok {
			continue
		}
		searchVal := strings.ToLower(match.MarketA.Title + " " + match.MarketB.Title + " " +
			match.MarketA.InternalID + " " + match.MarketB.InternalID + " " +
			match.MarketA.VenueID + " " + match.MarketB.VenueID)
		fmt.Fprintf(buf, "<div class=\"card routing-card\" id=\"rd-%d\" data-search=\"%s\">\n", shown, html.EscapeString(searchVal))
		fmt.Fprintf(buf, "<p style=\"font-size:11px;color:var(--muted);margin-bottom:6px\">[%d/%d]</p>\n", shown+1, topN)
		writeRoutingCard(buf, match, dec)
		fmt.Fprintf(buf, "</div>\n")
		shown++
	}

	fmt.Fprintf(buf, "</section>\n")
}

// writeRoutingCard renders a single routing decision card into buf.
func writeRoutingCard(buf *bytes.Buffer, match models.MatchResult, dec models.RoutingDecision) {
	title := match.MarketA.Title
	if title == "" {
		title = match.MarketB.Title
	}

	// ── Card header ──────────────────────────────────────────────────
	fmt.Fprintf(buf, "<div class=\"rcard-hdr\">\n")
	fmt.Fprintf(buf, "<strong>%s</strong>\n", html.EscapeString(title))
	fmt.Fprintf(buf, "<div class=\"rcard-meta\">Standard Lot: $%.0f USD (YES side)</div>\n", config.StandardLotUSD)
	fmt.Fprintf(buf, "<div class=\"rcard-meta\">Polymarket <code>%s</code> &harr; Kalshi <code>%s</code></div>\n",
		html.EscapeString(match.MarketA.InternalID),
		html.EscapeString(match.MarketB.InternalID),
	)

	// Confidence breakdown
	conf := match.Confidence
	confClass := "conf-high"
	if conf < 0.80 {
		confClass = "conf-mid"
	}
	if conf < 0.65 {
		confClass = "conf-low"
	}
	fmt.Fprintf(buf, "<div class=\"conf-row\">\n")
	fmt.Fprintf(buf, `<div class="conf-bar-wrap %s"><div class="conf-bar-track"><div class="conf-bar-fill" style="width:%.0f%%"></div></div><span class="conf-pct">%.2f</span></div>`,
		confClass, conf*100, conf)
	fmt.Fprintf(buf, `<span class="pill">T:%.2f</span>`, match.TitleScore)
	fmt.Fprintf(buf, `<span class="pill">D:%.2f</span>`, match.DateScore)
	fmt.Fprintf(buf, `<span class="pill">C:%.2f</span>`, match.CategoryScore)
	fmt.Fprintf(buf, "\n</div>\n")

	// Polarity
	if match.IsPolarityInverted {
		fmt.Fprintf(buf, `<div style="margin-top:6px"><span class="badge ba">&#9651; Inverted</span> <span style="font-size:11px;color:var(--tx3)">Router uses 1.0 &minus; Market_B.YesPrice</span></div>`+"\n")
	} else {
		fmt.Fprintf(buf, `<div style="margin-top:6px"><span class="badge bx">Aligned</span> <span style="font-size:11px;color:var(--tx3)">No polarity correction needed</span></div>`+"\n")
	}
	fmt.Fprintf(buf, "</div>\n") // end rcard-hdr

	// ── Venue panels ─────────────────────────────────────────────────
	fmt.Fprintf(buf, "<div class=\"venue-panels\">\n")
	for _, m := range []models.NormalizedMarket{match.MarketA, match.MarketB} {
		writeVenuePanel(buf, m, dec, match)
	}
	fmt.Fprintf(buf, "</div>\n")

	// ── Reasoning trail ───────────────────────────────────────────────
	if len(dec.ReasoningLog) > 0 {
		fmt.Fprintf(buf, "<div class=\"reasoning-trail\">\n")
		fmt.Fprintf(buf, "<div class=\"rt-header\">Reasoning Trail</div>\n")
		for _, line := range dec.ReasoningLog {
			dotClass := ""
			l := strings.ToUpper(line)
			if strings.HasPrefix(l, "SAVINGS:") || strings.HasPrefix(l, "ROUTE") || strings.Contains(l, "FULL") {
				dotClass = "ok"
			} else if strings.Contains(l, "EXCLUDED") || strings.Contains(l, "REJECTED") || strings.Contains(l, "STALE") {
				dotClass = "err"
			} else if strings.Contains(l, "PARTIAL") || strings.Contains(l, "WARNING") || strings.Contains(l, "SLIPPAGE") {
				dotClass = "warn"
			}
			fmt.Fprintf(buf, "<div class=\"rt-step\"><div class=\"rt-dot %s\"></div><div class=\"rt-text\">%s</div></div>\n",
				dotClass, html.EscapeString(line))
		}
		fmt.Fprintf(buf, "</div>\n")
	}

	// ── Decision summary ─────────────────────────────────────────────
	fmt.Fprintf(buf, "<div class=\"dblk\">\n")
	if dec.FillStatus == "REJECTED" {
		fmt.Fprintf(buf, `<div class="dvenue dvenue-rej">&#10007; NO ROUTE &mdash; All venues excluded</div>`+"\n")
	} else {
		fmt.Fprintf(buf, `<div class="dvenue">&#10003; ROUTE TO: %s</div>`+"\n", html.EscapeString(dec.SelectedVenue))
	}
	for venue, reason := range dec.ExclusionReasons {
		badgeClass := "ba"
		if reason == "SLIPPAGE_EXCEEDED" {
			badgeClass = "br"
		}
		fmt.Fprintf(buf, `<div class="dline">Excluded <strong>%s</strong>: <span class="badge %s">%s</span></div>`+"\n",
			html.EscapeString(venue), badgeClass, html.EscapeString(reason))
	}
	for _, line := range dec.ReasoningLog {
		if strings.HasPrefix(line, "SAVINGS:") {
			fmt.Fprintf(buf, `<div class="dline" style="margin-top:4px">%s</div>`+"\n", html.EscapeString(line))
		}
	}
	if len(dec.DataAgeSeconds) > 0 {
		fmt.Fprintf(buf, `<div class="dline" style="margin-top:6px">`)
		for venue, age := range dec.DataAgeSeconds {
			dotClass := "dg"
			if age > 60 {
				dotClass = "dr"
			} else if age > 30 {
				dotClass = "da"
			}
			fmt.Fprintf(buf, `<span class="dot %s"></span>%s: %.0fs &nbsp;`, dotClass, html.EscapeString(venue), age)
		}
		fmt.Fprintf(buf, "</div>\n")
	}
	fmt.Fprintf(buf, "</div>\n") // end dblk
}

// writeVenuePanel renders a venue card panel for the side-by-side comparison in routing cards.
func writeVenuePanel(buf *bytes.Buffer, m models.NormalizedMarket, dec models.RoutingDecision, match models.MatchResult) {
	isSelected := m.VenueID == dec.SelectedVenue
	exclusionReason, isExcluded := dec.ExclusionReasons[m.VenueID]

	panelClass := "venue-panel"
	if isSelected {
		panelClass += " vsel"
	}
	if isExcluded {
		panelClass += " vrej"
	}

	fmt.Fprintf(buf, "<div class=\"%s\">\n", panelClass)

	if isSelected {
		fmt.Fprintf(buf, "<div class=\"vp-badge\">Selected Venue</div>\n")
	}

	nameClass := "vp-name"
	if isSelected {
		nameClass = "vp-name sel"
	}
	fmt.Fprintf(buf, "<div class=\"vp-header\">\n<div>\n")
	fmt.Fprintf(buf, "<div class=\"%s\">%s</div>\n", nameClass, html.EscapeString(m.VenueID))

	if isExcluded {
		fmt.Fprintf(buf, "<div class=\"vp-price\" style=\"font-size:18px;color:var(--r)\">EXCLUDED</div>\n")
		fmt.Fprintf(buf, "<div class=\"vp-price-lbl\">%s</div>\n", html.EscapeString(exclusionReason))
	} else if isSelected {
		fmt.Fprintf(buf, "<div class=\"vp-price\">%.4f</div>\n", dec.EffectivePrice)
		fmt.Fprintf(buf, "<div class=\"vp-price-lbl\">Eff. Price</div>\n")
	} else {
		// Recalculate for display
		price := m.YesPrice
		if match.IsPolarityInverted && m.VenueID == match.MarketB.VenueID {
			price = 1.0 - m.YesPrice
		}
		asks := m.Asks
		if len(asks) == 0 && price > 0 {
			asks = []models.OrderbookLevel{{Price: price, SizeUSD: config.StandardLotUSD}}
		}
		wap, filled, _ := routing.CalculateWAP(asks, config.StandardLotUSD)
		if len(m.Asks) == 0 {
			wap = price
			filled = config.StandardLotUSD
		}
		fa := routing.NewFeeAdapter(m.VenueID)
		feeEst := fa.Calculate(wap, filled)
		effP := wap + feeEst.FeePerContract
		fmt.Fprintf(buf, "<div class=\"vp-price\">%.4f</div>\n", effP)
		fmt.Fprintf(buf, "<div class=\"vp-price-lbl\">Eff. Price</div>\n")
	}
	fmt.Fprintf(buf, "</div>\n</div>\n") // end vp-header inner + vp-header

	fmt.Fprintf(buf, "<div class=\"vp-rows\">\n")

	if isExcluded {
		fmt.Fprintf(buf, "<div class=\"vp-row\"><span class=\"vp-row-lbl\">Depth (USD)</span><span class=\"vp-row-val\">$%.0f</span></div>\n", m.TotalDepthUSD)
		fmt.Fprintf(buf, "<div class=\"vp-row\"><span class=\"vp-row-lbl\">Status</span><span class=\"badge br\">Excluded</span></div>\n")
	} else if isSelected {
		fmt.Fprintf(buf, "<div class=\"vp-row\"><span class=\"vp-row-lbl\">WAP</span><span class=\"vp-row-val sel\">%.4f</span></div>\n", dec.WAP)
		fmt.Fprintf(buf, "<div class=\"vp-row\"><span class=\"vp-row-lbl\">Fee / Contract</span><span class=\"vp-row-val sel\">%.4f</span></div>\n", dec.FeePerContract)
		fmt.Fprintf(buf, "<div class=\"vp-row\"><span class=\"vp-row-lbl\">Depth (USD)</span><span class=\"vp-row-val\">$%.0f</span></div>\n", m.TotalDepthUSD)
		statusClass := "bg"
		if dec.FillStatus == "PARTIAL" {
			statusClass = "ba"
		} else if dec.FillStatus == "REJECTED" {
			statusClass = "br"
		}
		fmt.Fprintf(buf, "<div class=\"vp-row\"><span class=\"vp-row-lbl\">Status</span><span class=\"badge %s\">%s</span></div>\n", statusClass, html.EscapeString(dec.FillStatus))
	} else {
		price := m.YesPrice
		if match.IsPolarityInverted && m.VenueID == match.MarketB.VenueID {
			price = 1.0 - m.YesPrice
		}
		asks := m.Asks
		if len(asks) == 0 && price > 0 {
			asks = []models.OrderbookLevel{{Price: price, SizeUSD: config.StandardLotUSD}}
		}
		wap, filled, status := routing.CalculateWAP(asks, config.StandardLotUSD)
		if len(m.Asks) == 0 {
			wap = price
			filled = config.StandardLotUSD
			status = "FULL"
		}
		fa := routing.NewFeeAdapter(m.VenueID)
		feeEst := fa.Calculate(wap, filled)
		fmt.Fprintf(buf, "<div class=\"vp-row\"><span class=\"vp-row-lbl\">WAP</span><span class=\"vp-row-val\">%.4f</span></div>\n", wap)
		fmt.Fprintf(buf, "<div class=\"vp-row\"><span class=\"vp-row-lbl\">Fee / Contract</span><span class=\"vp-row-val\">%.4f</span></div>\n", feeEst.FeePerContract)
		fmt.Fprintf(buf, "<div class=\"vp-row\"><span class=\"vp-row-lbl\">Depth (USD)</span><span class=\"vp-row-val\">$%.0f</span></div>\n", m.TotalDepthUSD)
		statusClass := "bx"
		if status == "PARTIAL" {
			statusClass = "ba"
		} else if status == "REJECTED" {
			statusClass = "br"
		}
		fmt.Fprintf(buf, "<div class=\"vp-row\"><span class=\"vp-row-lbl\">Status</span><span class=\"badge %s\">%s</span></div>\n", statusClass, html.EscapeString(status))
		_ = filled
	}

	fmt.Fprintf(buf, "</div>\n") // end vp-rows
	fmt.Fprintf(buf, "</div>\n") // end venue-panel
}

// ── Section 4 ──────────────────────────────────────────────────────────────

func writeSection4(buf *bytes.Buffer, matches []models.MatchResult) {
	fmt.Fprintf(buf, "<section>\n<h2>Assumptions &amp; Audit Trail</h2>\n<div class=\"twocol\">\n")

	// Left column — active config values
	fmt.Fprintf(buf, "<div class=\"card\">\n<h3>Active Configuration</h3>\n")
	fmt.Fprintf(buf, "<table class=\"ctbl\">\n")

	rows := [][2]string{
		{"StandardLotUSD", fmt.Sprintf("$%.0f", config.StandardLotUSD)},
		{"StalenessThreshold", fmt.Sprintf("%.0fs", config.StalenessThreshold.Seconds())},
		{"ResolutionWindowHours", fmt.Sprintf("%.0fh", config.ResolutionWindowHours)},
		{"SlippageCeiling", fmt.Sprintf("%.0f%%", config.SlippageCeiling*100)},
		{"MatchConfidenceThreshold", fmt.Sprintf("%.2f", config.MatchConfidenceThreshold)},
		{"KalshiFeeMultiplier", fmt.Sprintf("%.2f", config.KalshiFeeMultiplier)},
		{"PolymarketPeakTakerFee", fmt.Sprintf("%.2f%%", config.PolymarketPeakTakerFee*100)},
		{"PolymarketFeeFloor", fmt.Sprintf("%.1f%%", config.PolymarketFeeFloor*100)},
		{"TitleWeight", fmt.Sprintf("%.2f", config.TitleWeight)},
		{"DateWeight", fmt.Sprintf("%.2f", config.DateWeight)},
		{"CategoryWeight", fmt.Sprintf("%.2f", config.CategoryWeight)},
	}
	for _, r := range rows {
		fmt.Fprintf(buf, "<tr><td>%s</td><td>%s</td></tr>\n",
			html.EscapeString(r[0]), html.EscapeString(r[1]))
	}

	fmt.Fprintf(buf, "</table>\n</div>\n")

	// Right column — matching reasoning audit trail
	fmt.Fprintf(buf, "<div class=\"card\">\n<h3>Matching Reasoning (Top %d)</h3>\n", topN)

	if len(matches) == 0 {
		fmt.Fprintf(buf, `<p style="font-size:12px;color:var(--muted)">No matches to audit.</p>`)
	}

	for i, match := range matches {
		if i >= topN {
			break
		}
		title := match.MarketA.Title
		if title == "" {
			title = match.MarketB.Title
		}
		if len(title) > 50 {
			title = title[:47] + "…"
		}
		fmt.Fprintf(buf, "<p style=\"font-size:11px;font-weight:600;color:var(--muted);margin:8px 0 3px\">[%d] %s</p>\n",
			i+1, html.EscapeString(title))
		if len(match.Reasoning) == 0 {
			fmt.Fprintf(buf, "<div class=\"mono\" style=\"font-size:11px\">(no reasoning entries)</div>\n")
		} else {
			fmt.Fprintf(buf, "<div class=\"mono\" style=\"font-size:11px\">")
			for _, line := range match.Reasoning {
				fmt.Fprintf(buf, "%s\n", html.EscapeString(line))
			}
			fmt.Fprintf(buf, "</div>\n")
		}
	}

	fmt.Fprintf(buf, "</div>\n") // end right column
	fmt.Fprintf(buf, "</div>\n") // end twocol
	fmt.Fprintf(buf, "</section>\n")
}

// ── Section 5 ──────────────────────────────────────────────────────────────

func writeSection5(buf *bytes.Buffer) {
	fmt.Fprintf(buf, "<section>\n<h2>Known Limitations</h2>\n")
	fmt.Fprintf(buf, "<div class=\"card\">\n")
	fmt.Fprintf(buf, "<h3>Known V1 Matcher Limitations</h3>\n")
	fmt.Fprintf(buf, "<div class=\"mono\" style=\"margin-top:8px\">%s</div>\n",
		html.EscapeString(knownV1Limitations))
	fmt.Fprintf(buf, "</div>\n</section>\n")
}
