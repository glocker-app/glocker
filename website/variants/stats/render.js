/* Shared renderer for the three Analytics skins. Geometry and data-shaping live
   here once; each skin supplies the CSS. Only the range strip is SVG — the
   calendar, heatmap, bars and lists are plain elements so a skin can fill them
   with a CSS pattern (the journal one needs that) instead of a flat colour. */
(function () {
  const D = window.DATA;
  const SKIN = document.documentElement.dataset.skin || 'dashboard';
  const el = (tag, cls, txt) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (txt != null) n.textContent = txt;
    return n;
  };
  const svgEl = (tag, attrs) => {
    const n = document.createElementNS('http://www.w3.org/2000/svg', tag);
    for (const k in attrs) n.setAttribute(k, attrs[k]);
    return n;
  };
  const MON = ['Jan','Feb','Mar','Apr','May','Jun','Jul','Aug','Sep','Oct','Nov','Dec'];
  const MONTH_FULL = ['January','February','March','April','May','June','July','August','September','October','November','December'];
  const DOW = ['Sunday','Monday','Tuesday','Wednesday','Thursday','Friday','Saturday'];

  /* violation count -> severity bucket, matching the legend (none/1-2/3-5/6-10/10+) */
  const bucket = (v) => (v === 0 ? 0 : v <= 2 ? 1 : v <= 5 ? 2 : v <= 10 ? 3 : 4);

  /* ------------------------------------------------------------------ strip */
  function renderStrip(host) {
    const W = 1000, H = 92, PAD_B = 16, PAD_T = 6;
    const t = D.timeline, n = t.length;
    const maxV = Math.max(...t.map((d) => Math.max(d.violations, d.unblocks)), 10);
    const x = (i) => (i / (n - 1)) * W;
    const y = (v) => PAD_T + (1 - v / maxV) * (H - PAD_T - PAD_B);

    const svg = svgEl('svg', { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: 'none', class: 'strip-svg' });
    svg.appendChild(hatchDefs());

    // unmanaged bands behind the lines
    t.forEach((d, i) => {
      if (!d.unmanaged) return;
      svg.appendChild(svgEl('rect', {
        class: 'band', x: x(i) - W / n / 2, y: PAD_T, width: Math.max(W / n, 1.4), height: H - PAD_T - PAD_B,
      }));
    });

    // horizontal guides at 0 / 20 / 40
    [0, 20, 40].forEach((v) => {
      if (v > maxV) return;
      svg.appendChild(svgEl('line', { class: 'guide', 'vector-effect': 'non-scaling-stroke', x1: 0, x2: W, y1: y(v), y2: y(v) }));
    });

    const path = (key, cls) =>
      svgEl('polyline', { class: cls, 'vector-effect': 'non-scaling-stroke', points: t.map((d, i) => `${x(i).toFixed(1)},${y(d[key]).toFixed(1)}`).join(' ') });
    svg.appendChild(path('unblocks', 'line-unblock'));
    svg.appendChild(path('violations', 'line-viol'));

    // the brushed window: the last ~24 days
    const selFrom = x(n - 25), selW = W - selFrom;
    svg.appendChild(svgEl('rect', { class: 'sel', 'vector-effect': 'non-scaling-stroke', x: selFrom, y: 0, width: selW, height: H - PAD_B }));

    host.innerHTML = '';
    const axisY = el('div', 'strip-y');
    [40, 20, 0].forEach((v) => axisY.appendChild(el('span', null, String(v))));
    const plot = el('div', 'strip-plot');
    plot.appendChild(svg);
    const axisX = el('div', 'strip-x');
    let lastMon = -1;
    t.forEach((d, i) => {
      const m = +d.date.slice(5, 7) - 1;
      if (m === lastMon) return;
      lastMon = m;
      const s = el('span', null, MON[m]);
      s.style.left = ((i / (n - 1)) * 100).toFixed(2) + '%';
      axisX.appendChild(s);
    });
    plot.appendChild(axisX);
    host.appendChild(axisY);
    host.appendChild(plot);
  }

  function hatchDefs() {
    const defs = svgEl('defs', {});
    const p = svgEl('pattern', { id: 'hatch-band', width: 5, height: 5, patternUnits: 'userSpaceOnUse', patternTransform: 'rotate(45)' });
    p.appendChild(svgEl('line', { class: 'hatch-stroke', x1: 0, y1: 0, x2: 0, y2: 5 }));
    defs.appendChild(p);
    return defs;
  }

  /* --------------------------------------------------------------- calendar */
  function monthGrid(year, month) {
    const wrap = el('section', 'cal-month');
    const head = el('h4', 'cal-title');
    head.appendChild(el('span', 'cal-name', `${MONTH_FULL[month]} ${year}`));

    const first = new Date(year, month, 1);
    const lead = (first.getDay() + 6) % 7;           // grid starts Monday
    const len = new Date(year, month + 1, 0).getDate();
    let hits = 0;

    const grid = el('div', 'cal-grid');
    ['M', 'T', 'W', 'T', 'F', 'S', 'S'].forEach((d) => grid.appendChild(el('span', 'cal-dow', d)));
    for (let i = 0; i < lead; i++) grid.appendChild(el('span', 'cal-pad'));

    for (let d = 1; d <= len; d++) {
      const date = new Date(year, month, d);
      const k = D.iso(date);
      const rec = k >= D.WINDOW_FROM && k <= D.iso(D.TODAY) ? D.days[k] : null;
      const cell = el('span', 'cal-cell', String(d));
      if (!rec) {
        cell.classList.add('future');
      } else {
        if (rec.v > 0) { cell.classList.add('viol', 'b' + bucket(rec.v)); hits++; }
        else cell.classList.add('clean');
        if (rec.u > 0) {
          cell.classList.add('unmanaged');
          cell.style.setProperty('--u', (rec.u * 100).toFixed(0) + '%');
        }
        cell.title = `${d} ${MON[month]} — ${rec.v} violation${rec.v === 1 ? '' : 's'}` +
          (rec.u ? `, ${Math.round(rec.u * 24)}h unmanaged` : '');
      }
      if (k === D.iso(D.TODAY)) cell.classList.add('today');
      grid.appendChild(cell);
    }
    head.appendChild(el('span', 'cal-hits', hits ? `${hits} hit${hits === 1 ? '' : 's'}` : 'no hits'));
    wrap.appendChild(head);
    wrap.appendChild(grid);
    return wrap;
  }

  function renderCalendar(host) {
    host.innerHTML = '';
    const months = el('div', 'cal-months');
    months.appendChild(monthGrid(2026, 7));
    months.appendChild(monthGrid(2026, 8));
    host.appendChild(months);

    const leg = el('div', 'cal-legend');
    const ramp = el('div', 'legend-row');
    ramp.appendChild(el('span', 'legend-label', 'none'));
    [1, 2, 3, 4].forEach((b) => ramp.appendChild(el('i', 'sw viol b' + b)));
    ['1–2', '3–5', '6–10', '10+'].forEach((t, i) => {
      ramp.insertBefore(el('span', 'legend-label', t), ramp.children[2 + i * 2]);
    });
    leg.appendChild(ramp);
    leg.appendChild(legendRow('sw clean', 'clean & managed'));
    leg.appendChild(legendRow('sw unmanaged', 'unmanaged (proportion of day, >1h)'));
    leg.appendChild(legendRow('sw today', 'today'));
    host.appendChild(leg);
  }
  function legendRow(swCls, text) {
    const r = el('div', 'legend-row');
    r.appendChild(el('i', swCls));
    r.appendChild(el('span', 'legend-label', text));
    return r;
  }

  /* -------------------------------------------------------------- day detail */
  function renderDayDetail(host) {
    const date = D.TODAY;
    const rec = D.days[D.iso(date)];
    host.innerHTML = '';
    host.appendChild(el('h4', 'day-date', `${DOW[date.getDay()]}, ${MONTH_FULL[date.getMonth()]} ${date.getDate()}, ${date.getFullYear()}`));

    const strip = el('div', 'day-hours');
    for (let h = 0; h < 24; h++) {
      const c = el('i', 'day-hour');
      if (rec && rec.u && h / 24 < rec.u) c.classList.add('unmanaged');
      c.title = String(h).padStart(2, '0') + ':00';
      strip.appendChild(c);
    }
    host.appendChild(strip);

    const ticks = el('div', 'day-ticks');
    ['00', '06', '12', '18'].forEach((t) => ticks.appendChild(el('span', null, t)));
    host.appendChild(ticks);

    const v = rec ? rec.v : 0;
    host.appendChild(el('p', v ? 'day-verdict bad' : 'day-verdict good',
      v ? `${v} violation${v === 1 ? '' : 's'}` : 'No violations — clean day ✓'));
  }

  /* ----------------------------------------------------------------- heatmap */
  function renderHeatmap(host, legendHost) {
    host.innerHTML = '';
    const ticks = el('div', 'heat-ticks');
    for (let h = 0; h < 24; h += 3) {
      const s = el('span', null, String(h).padStart(2, '0'));
      s.style.gridColumn = String(h + 2);
      ticks.appendChild(s);
    }
    host.appendChild(ticks);

    ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'].forEach((name, wd) => {
      const row = el('div', 'heat-row');
      row.appendChild(el('span', 'heat-day', name));
      D.heat[wd].forEach((c, h) => {
        const cell = el('i', 'heat-cell');
        if (c.count) cell.classList.add('hit', 'b' + bucket(c.count));
        if (c.unmanaged) cell.classList.add('unmanaged');
        if (c.peak) cell.classList.add('peak');
        cell.title = `${name} ${String(h).padStart(2, '0')}:00 — ${c.count} violation${c.count === 1 ? '' : 's'}`;
        row.appendChild(cell);
      });
      host.appendChild(row);
    });

    legendHost.innerHTML = '';
    const r = el('div', 'legend-row');
    r.appendChild(el('span', 'legend-label', 'less'));
    [1, 2, 3, 4].forEach((b) => r.appendChild(el('i', 'sw viol b' + b)));
    r.appendChild(el('span', 'legend-label', 'more'));
    legendHost.appendChild(r);
    legendHost.appendChild(legendRow('sw peak', 'peak slot'));
    legendHost.appendChild(legendRow('sw unmanaged', 'mostly unmanaged (glocker off)'));
  }

  /* --------------------------------------------------------- bars: by hour */
  function renderHourBars(host) {
    const max = Math.max(...D.unblockHour.map((h) => h.entertainment), 1);
    host.innerHTML = '';
    const yax = el('div', 'bar-y');
    for (let v = max; v >= 0; v--) yax.appendChild(el('span', null, String(v)));
    const plot = el('div', 'bar-plot');
    plot.style.setProperty('--rows', String(max));
    D.unblockHour.forEach((h, i) => {
      const slot = el('div', 'bar-slot');
      const b = el('i', 'bar reason-entertainment');
      b.style.height = ((h.entertainment / max) * 100).toFixed(1) + '%';
      if (!h.entertainment) b.classList.add('empty');
      b.title = `${String(i).padStart(2, '0')}:00 — ${h.entertainment} unblock${h.entertainment === 1 ? '' : 's'}`;
      slot.appendChild(b);
      plot.appendChild(slot);
    });
    const xax = el('div', 'bar-x');
    for (let h = 0; h < 24; h += 3) {
      const s = el('span', null, String(h).padStart(2, '0'));
      s.style.left = ((h / 24) * 100 + 100 / 48).toFixed(2) + '%';
      xax.appendChild(s);
    }
    host.appendChild(yax);
    const col = el('div', 'bar-col');
    col.appendChild(plot);
    col.appendChild(xax);
    host.appendChild(col);
  }

  /* ------------------------------------------------------ bars: exposure */
  function renderExposure(host) {
    const max = Math.ceil(Math.max(...D.exposure.map((d) => d.removed + d.stopped)) / 5) * 5;
    host.innerHTML = '';
    const yax = el('div', 'bar-y');
    for (let v = max; v >= 0; v -= 5) yax.appendChild(el('span', null, v + 'h'));
    const plot = el('div', 'bar-plot stacked');
    plot.style.setProperty('--rows', String(max / 5));
    D.exposure.forEach((d) => {
      const slot = el('div', 'bar-slot');
      const stack = el('div', 'bar-stack');
      stack.style.height = (((d.removed + d.stopped) / max) * 100).toFixed(1) + '%';
      if (d.removed + d.stopped === 0) stack.classList.add('empty');
      const tot = d.removed + d.stopped || 1;
      const s2 = el('i', 'bar cause-stopped');
      s2.style.height = ((d.stopped / tot) * 100).toFixed(1) + '%';
      const s1 = el('i', 'bar cause-removed');
      s1.style.height = ((d.removed / tot) * 100).toFixed(1) + '%';
      stack.appendChild(s2);
      stack.appendChild(s1);
      stack.title = `${d.date} — ${d.removed}h removed, ${d.stopped}h stopped`;
      slot.appendChild(stack);
      plot.appendChild(slot);
    });
    const xax = el('div', 'bar-x');
    D.exposure.forEach((d, i) => {
      if (i % 3) return;
      const m = +d.date.slice(5, 7) - 1;
      const s = el('span', null, `${MON[m]} ${+d.date.slice(8)}`);
      s.style.left = ((i / D.exposure.length) * 100 + 100 / (D.exposure.length * 2)).toFixed(2) + '%';
      xax.appendChild(s);
    });
    host.appendChild(yax);
    const col = el('div', 'bar-col');
    col.appendChild(plot);
    col.appendChild(xax);
    host.appendChild(col);
  }

  /* -------------------------------------------------------------- the lists */
  function renderUnblocks(host) {
    host.innerHTML = '';
    D.unblocks.forEach((u) => {
      const row = el('div', 'grant-row');
      row.appendChild(el('span', 'grant-ts', u.ts));
      row.appendChild(el('span', 'grant-what', `${u.target} — ${u.reason}`));
      row.appendChild(el('span', 'grant-len', u.mins + 'm'));
      host.appendChild(row);
    });
  }

  function renderIgnored(host) {
    host.innerHTML = '';
    D.ignored.forEach((g) => {
      const row = el('div', 'ign-row');
      row.appendChild(el('span', 'ign-ts', g.ts));
      row.appendChild(el('span', 'ign-kw', g.kw));
      row.appendChild(el('span', 'ign-host', g.host));
      const b = el('button', 'ign-restore', '↺');
      b.type = 'button';
      b.title = 'Restore this hit';
      row.appendChild(b);
      host.appendChild(row);
    });
  }

  /* ------------------------------------------------------------------ boot */
  function boot() {
    renderStrip(document.getElementById('strip'));
    renderCalendar(document.getElementById('calendar'));
    renderDayDetail(document.getElementById('day-detail'));
    renderHeatmap(document.getElementById('heatmap'), document.getElementById('heatmap-legend'));
    renderHourBars(document.getElementById('hour-bars'));
    renderExposure(document.getElementById('exposure'));
    renderUnblocks(document.getElementById('unblock-list'));
    renderIgnored(document.getElementById('ignored-list'));

    // calendar day selection feeds the day-detail panel
    document.getElementById('calendar').addEventListener('click', (e) => {
      const cell = e.target.closest('.cal-cell');
      if (!cell || cell.classList.contains('future')) return;
      document.querySelectorAll('.cal-cell.selected').forEach((c) => c.classList.remove('selected'));
      cell.classList.add('selected');
    });

    // sub-tabs are inert here — the variants only mock the History tab
    document.querySelectorAll('.subtabs button').forEach((b) => {
      b.addEventListener('click', () => {
        document.querySelectorAll('.subtabs button').forEach((x) => x.classList.remove('active'));
        b.classList.add('active');
      });
    });
  }
  document.readyState === 'loading' ? document.addEventListener('DOMContentLoaded', boot) : boot();
})();
