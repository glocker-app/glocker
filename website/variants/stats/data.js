/* Synthetic but deterministic stand-in for /api/data, shaped like the real
   glockpeek Analytics -> History view. Every skin renders THIS, so the three
   variants can be compared on identical numbers. */

const TODAY = new Date(2026, 8, 16); // Wed 16 Sep 2026
const WINDOW_FROM = '2026-08-20'; // the brushed range: 28 days back

/* mulberry32 — small seeded PRNG, so the long timeline is stable across loads */
function rng(seed) {
  return function () {
    seed |= 0; seed = (seed + 0x6D2B79F5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// Local-calendar key. toISOString() would convert to UTC and shift the date by
// one for anyone east or west of Greenwich.
const iso = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
const addDays = (d, n) => new Date(d.getFullYear(), d.getMonth(), d.getDate() + n);

/* ---- day ledger -------------------------------------------------------
   Aug + Sep 2026 are pinned to match the reference screenshot; everything
   before is generated. v = violations that day, u = unmanaged fraction of
   the day (glocker off), m = managed at all. */
const PINNED = {
  '2026-08-22': { v: 0 }, '2026-08-23': { v: 0 },
  '2026-08-24': { v: 4 }, '2026-08-25': { v: 0 }, '2026-08-26': { v: 0 },
  '2026-08-27': { v: 0 }, '2026-08-28': { v: 0, u: 0.22 },
  '2026-08-29': { v: 0, u: 0.78 }, '2026-08-30': { v: 0 },
  '2026-08-31': { v: 0, u: 0.30 },
  '2026-09-01': { v: 0 }, '2026-09-02': { v: 0 }, '2026-09-03': { v: 0 },
  '2026-09-04': { v: 0 }, '2026-09-05': { v: 0 }, '2026-09-06': { v: 0 },
  '2026-09-07': { v: 0 }, '2026-09-08': { v: 0, u: 0.64 }, '2026-09-09': { v: 0 },
  '2026-09-10': { v: 0 }, '2026-09-11': { v: 0, u: 0.55 }, '2026-09-12': { v: 0, u: 0.18 },
  '2026-09-13': { v: 7 }, '2026-09-14': { v: 0, u: 0.71 }, '2026-09-15': { v: 0, u: 0.48 },
  '2026-09-16': { v: 0 },
};

const days = {};
{
  const r = rng(20260916);
  const start = new Date(2025, 9, 20); // 20 Oct 2025
  for (let d = start; d <= TODAY; d = addDays(d, 1)) {
    const k = iso(d);
    if (PINNED[k]) { days[k] = { v: PINNED[k].v || 0, u: PINNED[k].u || 0, m: true }; continue; }
    const x = r();
    const burst = r() < 0.05;                       // occasional bad stretch
    days[k] = {
      v: burst ? Math.floor(x * 18) + 4 : (x < 0.72 ? 0 : Math.ceil(x * 3)),
      u: r() < 0.12 ? +(0.15 + r() * 0.7).toFixed(2) : 0,
      m: true,
    };
  }
}

/* ---- range strip: violations/day, unblocks/day, unmanaged band --------- */
const timeline = Object.keys(days).sort().map((k) => {
  const d = days[k];
  return { date: k, violations: d.v, unblocks: d.v > 0 ? Math.round(d.v / 3) : (d.u > 0 ? 1 : 0), unmanaged: d.u };
});

/* ---- vulnerability map: 7 weekdays x 24 hours -------------------------- */
const HEAT_HOT = { '0-17': 9, '6-13': 8 };          // the two bright cells
const HEAT_OFF = [                                   // mostly-unmanaged slots
  [0, 15, 23], [1, 0, 12], [2, 0, 5], [4, 15, 23], [5, 0, 7],
];
const heat = [];
{
  const r = rng(7);
  for (let wd = 0; wd < 7; wd++) {
    const row = [];
    const off = HEAT_OFF.find((h) => h[0] === wd);
    for (let h = 0; h < 24; h++) {
      const hot = HEAT_HOT[wd + '-' + h];
      row.push({
        count: hot || (r() < 0.22 ? Math.ceil(r() * 3) : 0),
        unmanaged: !!(off && h >= off[1] && h <= off[2]),
        peak: !!hot,
      });
    }
    heat.push(row);
  }
}

/* ---- unblocks by hour, by reason --------------------------------------- */
const unblockHour = Array.from({ length: 24 }, () => ({ entertainment: 0 }));
[[9, 1], [11, 1], [12, 3], [13, 8], [14, 2], [15, 4], [16, 6], [17, 2], [21, 1], [22, 2], [23, 1]]
  .forEach(([h, n]) => { unblockHour[h].entertainment = n; });

/* ---- exposure over time: hours unguarded, removed vs stopped ----------- */
const exposure = [
  ['2026-08-22', 0, 0], ['2026-08-23', 0, 0], ['2026-08-24', 0, 0], ['2026-08-25', 0, 0],
  ['2026-08-26', 5, 6], ['2026-08-27', 0, 0], ['2026-08-28', 21, 24], ['2026-08-29', 0, 0],
  ['2026-08-30', 1.5, 0.5], ['2026-08-31', 6, 11], ['2026-09-01', 4, 6],
  ['2026-09-02', 0, 0], ['2026-09-03', 0, 0], ['2026-09-04', 0, 0],
  ['2026-09-05', 7, 5.5], ['2026-09-06', 22.5, 25.5], ['2026-09-07', 5, 7.5],
  ['2026-09-08', 4.5, 11], ['2026-09-09', 22, 26.5], ['2026-09-10', 6, 11.5],
  ['2026-09-11', 1, 0.5], ['2026-09-12', 22, 25], ['2026-09-13', 22, 26.5],
  ['2026-09-14', 2.5, 0.5], ['2026-09-15', 0, 0], ['2026-09-16', 0, 0],
].map(([date, removed, stopped]) => ({ date, removed, stopped }));

/* ---- deliberate grants -------------------------------------------------- */
const unblocks = [
  { ts: 'Sep 16, 05:24 PM', target: 'youtube.com', reason: 'entertainment', mins: 20 },
  { ts: 'Sep 16, 05:24 PM', target: 'youtube.com', reason: 'entertainment', mins: 20 },
  { ts: 'Sep 16, 01:51 PM', target: 'youtube.com', reason: 'entertainment', mins: 20 },
  { ts: 'Sep 10, 01:23 PM', target: 'youtube.com', reason: 'entertainment', mins: 20 },
  { ts: 'Sep 3, 10:50 PM',  target: 'youtube.com', reason: 'entertainment', mins: 20 },
  { ts: 'Sep 2, 04:41 PM',  target: 'youtube.com', reason: 'entertainment', mins: 20 },
];

/* ---- false positives the user has hidden ------------------------------- */
const ignored = [
  { ts: 'Sep 7, 04:34 PM',  kw: 'porn',   host: 'www.willspencer.co' },
  { ts: 'Sep 7, 04:33 PM',  kw: 'porn',   host: 'www.willspencer.co' },
  { ts: 'Sep 5, 04:43 PM',  kw: 'goon',   host: 'www.imdb.com' },
  { ts: 'Aug 24, 05:09 PM', kw: 'porn',   host: 'www.modelrepublic.org' },
  { ts: 'Aug 18, 06:22 PM', kw: 'porn',   host: 'tau.dev' },
  { ts: 'Aug 18, 06:20 PM', kw: 'porn',   host: 'tau.dev' },
  { ts: 'Aug 18, 04:26 PM', kw: 'incest', host: 'www.youtube.com' },
  { ts: 'Aug 18, 03:46 PM', kw: 'incest', host: 'www.youtube.com' },
  { ts: 'Aug 18, 11:57 AM', kw: 'pmv',    host: '—' },
  { ts: 'Aug 18, 11:57 AM', kw: 'pmv',    host: '—' },
  { ts: 'Aug 17, 01:56 PM', kw: 'porn',   host: 'scottaaronson.blog' },
];

window.DATA = { TODAY, WINDOW_FROM, days, timeline, heat, unblockHour, exposure, unblocks, ignored, iso, addDays };
