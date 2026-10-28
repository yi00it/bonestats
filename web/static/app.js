// Dashboard behaviour: the LED chart, sparklines, map, motion and small form
// helpers. Everything except the chart, sparklines and map also works
// without JavaScript.
var css = getComputedStyle(document.documentElement);
var cssVar = function (n) { return css.getPropertyValue(n).trim(); };
var calm = window.matchMedia && matchMedia("(prefers-reduced-motion: reduce)").matches;

function bindForms(root) {
  root.querySelectorAll("[data-autosubmit]").forEach(function (el) {
    el.addEventListener("change", function () {
      var custom = document.querySelector(".custom");
      if (el.name === "range" && el.value === "custom" && custom) {
        custom.removeAttribute("data-custom-hidden"); // pick dates, then Apply
        return;
      }
      el.form.submit();
    });
  });
  root.querySelectorAll("form[data-confirm]").forEach(function (f) {
    f.addEventListener("submit", function (e) { if (!confirm(f.dataset.confirm)) e.preventDefault(); });
  });
}
bindForms(document);

// Show country names instead of ISO codes.
var regionNames = window.Intl && Intl.DisplayNames && new Intl.DisplayNames(["en"], { type: "region" });
function countryName(code) {
  try { return (regionNames && regionNames.of(code)) || code; } catch (e) { return code; }
}

// Adds "dim is value" to the current URL, replacing filters on the same dimension.
function filterURL(dim, value) {
  var p = new URLSearchParams(location.search), keep = p.getAll("f").filter(function (f) { return f.split("|")[0] !== dim; });
  p.delete("f");
  keep.concat(dim + "|is|" + value).forEach(function (f) { p.append("f", f); });
  return "/?" + p.toString();
}

function hex(c) {
  var m = c.replace("#", "").match(/../g) || ["88", "88", "88"];
  return m.map(function (x) { return parseInt(x, 16); });
}
// mix returns the color a fraction t of the way from a to b.
function mix(a, b, t) {
  var x = hex(a), y = hex(b);
  return "rgb(" + x.map(function (c, i) { return Math.round(c + (y[i] - c) * t); }).join(",") + ")";
}

function formatValue(kind, v) {
  if (v == null) return "";
  switch (kind) {
    case "percent": return Math.round(v) + "%";
    case "decimal": return v.toFixed(1);
    case "duration":
      v = Math.round(v);
      return v < 60 ? v + "s" : Math.floor(v / 60) + "m " + String(v % 60).padStart(2, "0") + "s";
    default: return v >= 1e4 ? (v / 1e3).toFixed(1) + "k" : String(Math.round(v));
  }
}

// niceMax rounds a maximum up to a round number on a fine scale.
function niceMax(v) {
  if (v <= 0) return 1;
  var p = Math.pow(10, Math.floor(Math.log10(v))), f = v / p;
  return ([1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10].find(function (s) { return f <= s; })) * p;
}

// The main chart: one column of LEDs per time bucket on a dot-matrix
// panel. The comparison period shows as a dash at its level.
function chart() {
  var el = document.getElementById("chart"), raw = document.getElementById("chart-data");
  if (!el || !raw) return;
  var cfg = JSON.parse(raw.textContent);
  var fmtTime = {
    hour: function (d) { return d.toLocaleDateString([], { weekday: "short", day: "numeric", month: "short" }) + " · " + d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }); },
    day: function (d) { return d.toLocaleDateString([], { weekday: "short", month: "short", day: "numeric", year: "numeric" }); },
    week: function (d) { return "Week of " + d.toLocaleDateString([], { month: "short", day: "numeric" }); },
    month: function (d) { return d.toLocaleDateString([], { month: "long", year: "numeric" }); }
  }[cfg.interval];
  var fmtTick = {
    hour: function (d) { return d.getHours() === 0 ? d.toLocaleDateString([], { month: "short", day: "numeric" }) : String(d.getHours()).padStart(2, "0") + ":00"; },
    day: function (d) { return d.toLocaleDateString([], { month: "short", day: "numeric" }); },
    week: function (d) { return d.toLocaleDateString([], { month: "short", day: "numeric" }); },
    month: function (d) { return d.toLocaleDateString([], { month: "short" }); }
  }[cfg.interval];
  var col = { off: cssVar("--off-2"), led: cssVar("--led"), lit: cssVar("--lit"), ring: cssVar("--ink-2"), muted: cssVar("--muted") };
  var font = "500 10px " + cssVar("--mono");
  var cur = cfg.cur || [], prev = cfg.prev, n = cur.length;
  var all = cur.concat(prev || []);
  var max = cfg.kind === "percent" ? 100 : niceMax(Math.max.apply(null, all.concat(cfg.kind === "decimal" ? 1 : 4)) * 1.08);

  var canvas = document.createElement("canvas"), ctx = canvas.getContext("2d");
  var tip = document.createElement("div");
  tip.className = "tip";
  el.replaceChildren(canvas, tip);

  var L = 44, R = 4, T = 6, B = 26, geo, hover = -1, start = null, raf = 0;
  function layout() {
    var w = el.clientWidth, h = el.clientHeight, dpr = window.devicePixelRatio || 1;
    canvas.width = Math.round(w * dpr); canvas.height = Math.round(h * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    var pw = w - L - R, ph = h - T - B, cw = pw / Math.max(n, 1);
    // Dot pitch follows the column width, within limits; wide columns hold
    // several dots side by side.
    var pitch = Math.max(4, Math.min(11, cw)), across = Math.max(1, Math.floor(cw / pitch - 0.25));
    var rows = Math.max(4, Math.floor(ph / pitch));
    geo = { w: w, h: h, pw: pw, ph: ph, cw: cw, pitch: pitch, across: across, rows: rows, r: pitch * 0.34, y0: T + ph - (ph - rows * pitch) / 2 - pitch / 2 };
  }
  function level(v) { return v == null ? 0 : Math.round(Math.min(1, v / max) * geo.rows); }
  function dot(x, y, r, fill) { ctx.beginPath(); ctx.arc(x, y, r, 0, 6.2832); ctx.fillStyle = fill; ctx.fill(); }

  function draw(now) {
    var g = geo, t = start == null ? 1 : (now - start) / 1100;
    ctx.clearRect(0, 0, g.w, g.h);
    for (var i = 0; i < n; i++) {
      var lit = level(cur[i]), pl = prev ? level(prev[i]) : -1;
      // Columns rise one after another on first draw.
      var p = Math.max(0, Math.min(1, t * 1.7 - (i / Math.max(n - 1, 1)) * 0.7));
      var show = Math.round(lit * (1 - Math.pow(1 - p, 3)));
      var cx = L + g.cw * i + g.cw / 2, active = i === hover;
      for (var a = 0; a < g.across; a++) {
        var x = cx + (a - (g.across - 1) / 2) * g.pitch;
        for (var row = 0; row < g.rows; row++) {
          var y = g.y0 - row * g.pitch, on = row < show;
          var fill = on ? (active ? col.lit : col.led) : (active ? mix(col.off, col.muted, 0.35) : col.off);
          if (on && row === show - 1 && !active) { ctx.shadowColor = col.led; ctx.shadowBlur = 8; }
          dot(x, y, g.r, fill);
          ctx.shadowBlur = 0;
        }
      }
      if (pl > 0 && p >= 1) {
        var py = Math.round(g.y0 - (pl - 0.5) * g.pitch) + 0.5, half = g.cw / 2 - Math.max(1, g.cw * 0.12);
        ctx.beginPath(); ctx.moveTo(cx - half, py); ctx.lineTo(cx + half, py);
        ctx.lineWidth = 2; ctx.lineCap = "round"; ctx.strokeStyle = col.ring; ctx.stroke();
      }
    }
    // Axis labels: three levels on the left, a handful of dates below.
    ctx.font = font; ctx.fillStyle = col.muted; ctx.textAlign = "right"; ctx.textBaseline = "middle";
    [0, 0.5, 1].forEach(function (f) {
      var row = Math.max(0, Math.round(f * g.rows) - 1);
      ctx.fillText(formatValue(cfg.kind, f * max), L - 12, g.y0 - row * g.pitch);
    });
    ctx.textAlign = "center"; ctx.textBaseline = "top";
    var every = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(g.pw / 90))));
    for (var j = 0; j < n; j += every) {
      var lx = L + g.cw * j + g.cw / 2;
      ctx.fillStyle = j === hover ? col.lit : col.muted;
      ctx.fillText(fmtTick(new Date(cfg.t[j] * 1000)).toUpperCase(), Math.min(Math.max(lx, L + 20), g.w - 24), g.h - B + 10);
    }
    if (t < 1) raf = requestAnimationFrame(draw); else start = null;
  }

  function showTip(i) {
    if (i < 0) { tip.classList.remove("on"); return; }
    var c = cur[i], pv = prev && prev[i];
    var html = '<div class="tip-date"></div><div class="tip-row"><span></span><b>' + formatValue(cfg.kind, c) + "</b></div>";
    if (prev) html += '<div class="tip-row"><span></span><b>' + formatValue(cfg.kind, pv) + "</b></div>";
    if (prev && pv) {
      var d = Math.round((c - pv) / pv * 100);
      html += '<div class="tip-change">' + (d > 0 ? "▲ " : d < 0 ? "▼ " : "") + Math.abs(d) + "% vs " + cfg.prevLabel.toLowerCase() + "</div>";
    }
    tip.innerHTML = html;
    // Labels go in as text: they come from the server but never as markup.
    tip.querySelector(".tip-date").textContent = fmtTime(new Date(cfg.t[i] * 1000));
    var spans = tip.querySelectorAll(".tip-row span");
    spans[0].textContent = cfg.label;
    if (spans[1]) spans[1].textContent = cfg.prevLabel;
    var x = L + geo.cw * i + geo.cw / 2, w = tip.offsetWidth;
    var top = Math.max(0, geo.y0 - level(c) * geo.pitch - tip.offsetHeight - 14);
    var left = x + 16 + w > geo.w ? x - 16 - w : x + 16;
    tip.style.transform = "translate(" + Math.max(0, left) + "px," + top + "px)";
    tip.classList.add("on");
  }

  canvas.addEventListener("mousemove", function (e) {
    var x = e.clientX - canvas.getBoundingClientRect().left;
    var i = x < L ? -1 : Math.min(n - 1, Math.floor((x - L) / geo.cw));
    if (i === hover) return;
    hover = i;
    showTip(i);
    if (start == null) draw(performance.now());
  });
  canvas.addEventListener("mouseleave", function () { hover = -1; showTip(-1); if (start == null) draw(performance.now()); });

  layout();
  if (!calm && !el.closest(".swapped")) start = performance.now();
  draw(performance.now());
  var ro = new ResizeObserver(function () { layout(); if (start == null) draw(performance.now()); });
  ro.observe(el);
  return function () { ro.disconnect(); cancelAnimationFrame(raf); el.replaceChildren(); };
}

// Sparklines under each readout: small LED columns, at most 28 of them.
function sparks(root) {
  var raw = root.querySelector("#spark-data");
  if (!raw) return;
  var data = JSON.parse(raw.textContent), ns = "http://www.w3.org/2000/svg", rows = 5, p = 4.4;
  root.querySelectorAll("[data-spark]").forEach(function (el) {
    var v = data[el.dataset.spark] || [];
    if (v.length < 2) return;
    var k = Math.min(28, v.length), cols = [];
    for (var c = 0; c < k; c++) {
      var a = Math.floor(c * v.length / k), b = Math.max(a + 1, Math.floor((c + 1) * v.length / k)), s = 0;
      for (var j = a; j < b; j++) s += v[j];
      cols.push(s / (b - a));
    }
    var max = Math.max.apply(null, cols.concat(1e-9));
    var svg = document.createElementNS(ns, "svg");
    svg.setAttribute("viewBox", "0 0 " + (k * p) + " " + (rows * p));
    svg.setAttribute("width", k * p * 22 / (rows * p));
    svg.setAttribute("aria-hidden", "true");
    cols.forEach(function (val, ci) {
      var lit = val > 0 ? Math.max(1, Math.round(val / max * rows)) : 0;
      for (var r = 0; r < rows; r++) {
        var d = document.createElementNS(ns, "circle");
        d.setAttribute("cx", ci * p + p / 2);
        d.setAttribute("cy", (rows - r) * p - p / 2);
        d.setAttribute("r", 1.35);
        if (r < lit) d.setAttribute("class", "on");
        svg.appendChild(d);
      }
    });
    el.replaceChildren(svg);
  });
}

// Departure-board numbers: digits flicker through random values and lock
// in from left to right.
function scramble(el, to) {
  if (calm || !/\d/.test(to)) { el.textContent = to; return; }
  var start = performance.now(), chars = to.split("");
  (function frame(now) {
    var t = now - start, done = true, digit = 0;
    el.textContent = chars.map(function (ch) {
      if (!/\d/.test(ch)) return ch;
      if (t >= 260 + digit++ * 90) return ch;
      done = false;
      return String(Math.floor(Math.random() * 10));
    }).join("");
    if (!done && el.isConnected) requestAnimationFrame(frame);
  })(start);
}

// The map: countries filled with dot patterns, from unlit to fully lit.
function map() {
  var el = document.getElementById("map"), raw = document.getElementById("map-data");
  if (!el || !raw || !window.jsVectorMap) return;
  var data = JSON.parse(raw.textContent);
  var max = Math.max.apply(null, Object.values(data).concat(1)), scale = {}, buckets = {};
  var off = cssVar("--off-2"), led = cssVar("--led"), steps = [0.22, 0.37, 0.53, 0.69, 0.84, 1];
  steps.forEach(function (s, i) { scale["b" + i] = "url(#bs-dots-" + i + ")"; });
  Object.keys(data).forEach(function (code) { buckets[code] = "b" + Math.min(5, Math.floor(Math.sqrt(data[code] / max) * 6)); });
  var m = new jsVectorMap({
    selector: "#map",
    map: "world",
    zoomButtons: false,
    zoomOnScroll: false,
    showTooltip: true,
    regionStyle: {
      initial: { fill: "url(#bs-dots-off)", stroke: "none", strokeWidth: 0 },
      hover: { fillOpacity: 0.65, cursor: "pointer" }
    },
    series: { regions: [{ attribute: "fill", scale: scale, values: buckets }] },
    onRegionTooltipShow: function (event, tooltip, code) {
      var n = data[code] || 0;
      tooltip.text(countryName(code) + " · " + n.toLocaleString() + " visitor" + (n === 1 ? "" : "s"));
    },
    onRegionClick: function (event, code) {
      if (data[code]) { remember(); location.href = filterURL("country", code); }
    }
  });
  var svg = el.querySelector("svg");
  if (svg) {
    var ns = "http://www.w3.org/2000/svg", defs = document.createElementNS(ns, "defs");
    var pattern = function (id, color) {
      var pt = document.createElementNS(ns, "pattern"), c = document.createElementNS(ns, "circle");
      pt.setAttribute("id", id); pt.setAttribute("width", 5); pt.setAttribute("height", 5);
      pt.setAttribute("patternUnits", "userSpaceOnUse");
      c.setAttribute("cx", 2.5); c.setAttribute("cy", 2.5); c.setAttribute("r", 1.75); c.setAttribute("fill", color);
      pt.appendChild(c);
      defs.appendChild(pt);
    };
    pattern("bs-dots-off", off);
    steps.forEach(function (s, i) { pattern("bs-dots-" + i, mix(off, led, s)); });
    svg.insertBefore(defs, svg.firstChild);
  }
  return function () { m.destroy(); };
}

function countryNames(root) {
  root.querySelectorAll("[data-country]").forEach(function (el) {
    el.textContent = countryName(el.dataset.country);
  });
}

// Keep the scroll position when a click only changes the dashboard's
// filters or metric, so the page doesn't jump back to the top.
function remember() {
  try { sessionStorage.setItem("bs-scroll", String(scrollY)); } catch (e) {}
}
(function restore() {
  try {
    var y = sessionStorage.getItem("bs-scroll");
    sessionStorage.removeItem("bs-scroll");
    if (y && document.querySelector("main[data-live]")) {
      history.scrollRestoration = "manual";
      scrollTo(0, +y);
    }
  } catch (e) {}
})();
document.addEventListener("click", function (e) {
  if (e.defaultPrevented || e.button || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  // A click anywhere on a breakdown row follows its filter link.
  var row = e.target.closest(".bars tbody tr");
  if (row && !e.target.closest("a")) {
    var link = row.querySelector("td.label a");
    if (link) { remember(); location.href = link.href; }
    return;
  }
  var a = e.target.closest("main[data-live] a[href^='/?']");
  if (a && !a.closest(".hero")) remember();
});

// Fade the panel in when switching tabs; its meters light up again.
document.addEventListener("change", function (e) {
  var r = e.target;
  if (!r.matches || !r.matches(".tabs > input[type=radio]")) return;
  var tabs = r.parentNode, i = Array.prototype.indexOf.call(tabs.querySelectorAll(":scope > input"), r);
  var panel = tabs.querySelectorAll(":scope > .panel")[i];
  if (!panel) return;
  panel.classList.remove("enter");
  void panel.offsetWidth;
  panel.classList.add("enter");
});

countryNames(document);
sparks(document);
document.querySelectorAll("[data-tween]").forEach(function (el) { scramble(el, el.textContent); });
var stopMap = map(), stopChart;
// The chart draws text on a canvas: wait for the fonts.
(document.fonts && document.fonts.ready ? document.fonts.ready : Promise.resolve()).then(function () { stopChart = chart(); });
// The canvas chart and the map's dot patterns hold colors: redraw them when
// the theme switch changes the palette.
document.addEventListener("bs-theme", function () {
  if (stopChart) stopChart();
  if (stopMap) stopMap();
  stopChart = chart();
  stopMap = map();
});

// Live updates: refetch the dashboard every 15 seconds while it is visible
// and swap in the new <main>. Open tabs are kept, numbers that changed
// flicker to their new value, and the chart and map are only redrawn when
// their data changed. Skipped while the user is busy with the toolbar or
// the filter form.
(function live() {
  var main = document.querySelector("main[data-live]");
  if (!main || !window.fetch || !window.DOMParser) return;
  var every = 15000, last = Date.now(), busy = false;

  function interacting() {
    var a = document.activeElement;
    return main.querySelector("details[open]") || (a && main.contains(a) && a.matches("select, textarea, input:not([type=radio])"));
  }

  function refresh() {
    if (busy || document.hidden || interacting()) return;
    busy = true;
    fetch(location.href, { cache: "no-store", credentials: "same-origin" }).then(function (res) {
      // A redirect means the login expired or the site is gone: reload properly.
      if (res.redirected || !res.ok) {
        if (res.redirected) location.reload();
        return;
      }
      return res.text().then(function (html) {
        var next = new DOMParser().parseFromString(html, "text/html").querySelector("main");
        if (!next || interacting()) return;
        var checked = Array.prototype.map.call(main.querySelectorAll("input[type=radio]:checked"), function (r) { return r.id; });
        var before = {};
        main.querySelectorAll("[data-tween]").forEach(function (el) { before[el.dataset.tween] = el.textContent; });
        var keep = function (id) {
          var a = document.getElementById(id + "-data"), b = next.querySelector("#" + id + "-data");
          if (!a || !b || a.textContent !== b.textContent) return false;
          next.querySelector("#" + id).replaceWith(document.getElementById(id));
          return true;
        };
        var keepChart = keep("chart"), keepMap = keep("map");
        if (!keepChart && stopChart) stopChart();
        if (!keepMap && stopMap) stopMap();
        main.classList.add("swapped"); // no entrance animations from here on
        main.replaceChildren.apply(main, Array.prototype.slice.call(next.childNodes));
        checked.forEach(function (id) { var r = document.getElementById(id); if (r) r.checked = true; });
        countryNames(main);
        bindForms(main);
        sparks(main);
        main.querySelectorAll("[data-tween]").forEach(function (el) {
          var old = before[el.dataset.tween];
          if (old == null || old === el.textContent) return;
          scramble(el, el.textContent);
          var kpi = el.closest(".kpi");
          if (kpi) kpi.classList.add("bump");
        });
        if (!keepChart) stopChart = chart();
        if (!keepMap) stopMap = map();
      });
    }).catch(function () {}).finally(function () { busy = false; last = Date.now(); });
  }

  setInterval(function () { if (Date.now() - last >= every) refresh(); }, 1000);
  document.addEventListener("visibilitychange", function () {
    if (!document.hidden && Date.now() - last >= every) refresh();
  });
})();
