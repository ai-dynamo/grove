const METRICS_URL = "index/metrics.ndjson";

const state = {
  rows: [],
  testName: "",
  metric: "",
};

const els = {
  status: document.getElementById("status"),
  testSelect: document.getElementById("test-select"),
  metricSelect: document.getElementById("metric-select"),
  chart: document.getElementById("chart"),
  latestBody: document.getElementById("latest-body"),
  tooltip: document.getElementById("tooltip"),
};

init();

async function init() {
  try {
    const text = await fetchText(METRICS_URL);
    state.rows = parseNdjson(text);
    if (state.rows.length === 0) {
      setStatus("No metrics found.");
      drawEmpty("No metrics found");
      return;
    }

    populateSelectors();
    els.testSelect.addEventListener("change", () => {
      state.testName = els.testSelect.value;
      populateMetricSelect();
      render();
    });
    els.metricSelect.addEventListener("change", () => {
      state.metric = els.metricSelect.value;
      render();
    });
    render();
  } catch (err) {
    setStatus(`Failed to load ${METRICS_URL}: ${err.message}`);
    drawEmpty("Failed to load metrics");
  }
}

async function fetchText(url) {
  const res = await fetch(url, { cache: "no-store" });
  if (!res.ok) {
    throw new Error(`${res.status} ${res.statusText}`);
  }
  return res.text();
}

function parseNdjson(text) {
  return text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line, index) => {
      try {
        const row = JSON.parse(line);
        row.valueSeconds = Number(row.valueSeconds);
        row.date = new Date(row.runTimestamp);
        if (!row.testName || !row.metric || !Number.isFinite(row.valueSeconds) || Number.isNaN(row.date.getTime())) {
          throw new Error("missing required metric fields");
        }
        return row;
      } catch (err) {
        throw new Error(`line ${index + 1}: ${err.message}`);
      }
    });
}

function populateSelectors() {
  const tests = unique(state.rows.map((row) => row.testName)).sort();
  state.testName = tests[0] || "";
  setOptions(els.testSelect, tests);
  populateMetricSelect();
}

function populateMetricSelect() {
  const metrics = unique(
    state.rows
      .filter((row) => row.testName === state.testName)
      .map((row) => row.metric),
  ).sort(metricSort);

  state.metric = metrics.includes(state.metric) ? state.metric : metrics[0] || "";
  setOptions(els.metricSelect, metrics);
  els.metricSelect.value = state.metric;
}

function setOptions(select, values) {
  select.replaceChildren(
    ...values.map((value) => {
      const option = document.createElement("option");
      option.value = value;
      option.textContent = value;
      return option;
    }),
  );
  select.value = values[0] || "";
}

function render() {
  const rows = state.rows
    .filter((row) => row.testName === state.testName && row.metric === state.metric)
    .sort((a, b) => a.date - b.date);

  setStatus(`${state.rows.length} metric points loaded`);
  drawChart(rows);
  renderLatestTable();
}

function drawChart(rows) {
  const svg = els.chart;
  const width = Math.max(640, svg.clientWidth || 640);
  const height = Math.max(260, svg.clientHeight || 320);
  const margin = { top: 18, right: 24, bottom: 42, left: 58 };
  const plotW = width - margin.left - margin.right;
  const plotH = height - margin.top - margin.bottom;

  svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
  svg.replaceChildren();

  if (rows.length === 0) {
    drawEmpty("No points for selected metric");
    return;
  }

  const minX = rows[0].date.getTime();
  const maxX = rows[rows.length - 1].date.getTime();
  const maxY = Math.max(...rows.map((row) => row.valueSeconds), 0.001);
  const yTop = maxY * 1.1;

  const xScale = (value) => {
    if (minX === maxX) return margin.left + plotW / 2;
    return margin.left + ((value - minX) / (maxX - minX)) * plotW;
  };
  const yScale = (value) => margin.top + plotH - (value / yTop) * plotH;

  drawGrid(svg, width, height, margin, plotW, plotH, yTop);

  const points = rows.map((row) => [xScale(row.date.getTime()), yScale(row.valueSeconds), row]);
  const path = points
    .map(([x, y], index) => `${index === 0 ? "M" : "L"} ${x.toFixed(2)} ${y.toFixed(2)}`)
    .join(" ");

  addEl(svg, "path", { class: "series", d: path });
  for (const [x, y, row] of points) {
    const marker = addEl(svg, "circle", {
      class: "point",
      cx: x,
      cy: y,
      r: 4,
    });
    const hit = addEl(svg, "circle", {
      class: "point-hit",
      cx: x,
      cy: y,
      r: 12,
    });
    hit.addEventListener("mouseenter", () => {
      marker.classList.add("point-active");
      showTooltip(row, x, y);
    });
    hit.addEventListener("mousemove", () => showTooltip(row, x, y));
    hit.addEventListener("mouseleave", () => {
      marker.classList.remove("point-active");
      hideTooltip();
    });
  }

  const first = rows[0].date;
  const last = rows[rows.length - 1].date;
  addText(svg, margin.left, height - 12, formatDate(first), "tick-label", "start");
  addText(svg, width - margin.right, height - 12, formatDate(last), "tick-label", "end");
}

function drawGrid(svg, width, height, margin, plotW, plotH, yTop) {
  addEl(svg, "line", {
    class: "axis",
    x1: margin.left,
    y1: margin.top,
    x2: margin.left,
    y2: margin.top + plotH,
  });
  addEl(svg, "line", {
    class: "axis",
    x1: margin.left,
    y1: margin.top + plotH,
    x2: width - margin.right,
    y2: margin.top + plotH,
  });

  for (let i = 0; i <= 4; i += 1) {
    const y = margin.top + plotH - (i / 4) * plotH;
    const value = (i / 4) * yTop;
    addEl(svg, "line", {
      class: "grid",
      x1: margin.left,
      y1: y,
      x2: margin.left + plotW,
      y2: y,
    });
    addText(svg, margin.left - 8, y + 4, formatSeconds(value), "tick-label", "end");
  }
}

function drawEmpty(message) {
  const svg = els.chart;
  const width = Math.max(640, svg.clientWidth || 640);
  const height = Math.max(260, svg.clientHeight || 320);
  svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
  svg.replaceChildren();
  addText(svg, width / 2, height / 2, message, "empty-label", "middle");
}

function renderLatestTable() {
  const latestByMetric = new Map();
  for (const row of state.rows.filter((item) => item.testName === state.testName)) {
    const current = latestByMetric.get(row.metric);
    if (!current || row.date > current.date) {
      latestByMetric.set(row.metric, row);
    }
  }

  const rows = [...latestByMetric.values()].sort((a, b) => metricSort(a.metric, b.metric));
  els.latestBody.replaceChildren(
    ...rows.map((row) => {
      const tr = document.createElement("tr");
      tr.append(
        cell(row.metric),
        cell(formatSeconds(row.valueSeconds), "number"),
        cell(row.runID),
      );
      return tr;
    }),
  );
}

function showTooltip(row, x, y) {
  els.tooltip.innerHTML = `
    <dl>
      <dt>Time</dt><dd>${escapeHtml(formatFullDate(row.date))}</dd>
      <dt>Value</dt><dd>${escapeHtml(formatSeconds(row.valueSeconds))} s</dd>
      <dt>Metric</dt><dd>${escapeHtml(row.metric)}</dd>
      <dt>Run</dt><dd>${escapeHtml(row.runID)}</dd>
      <dt>Commit</dt><dd>${escapeHtml(shortCommit(row.commit))}</dd>
      <dt>Result</dt><dd>${escapeHtml(row.resultPath || "")}</dd>
    </dl>
  `;
  els.tooltip.hidden = false;
  const chartBox = els.chart.getBoundingClientRect();
  const wrapBox = els.chart.parentElement.getBoundingClientRect();
  const tooltipBox = els.tooltip.getBoundingClientRect();
  const preferredLeft = chartBox.left - wrapBox.left + x + 12;
  const preferredTop = chartBox.top - wrapBox.top + y;
  const maxLeft = wrapBox.width - tooltipBox.width - 8;
  const minLeft = 8;

  els.tooltip.style.left = `${clamp(preferredLeft, minLeft, Math.max(minLeft, maxLeft))}px`;
  els.tooltip.style.top = `${Math.max(tooltipBox.height + 14, preferredTop)}px`;
}

function hideTooltip() {
  els.tooltip.hidden = true;
}

function cell(text) {
  const td = document.createElement("td");
  td.textContent = text;
  return td;
}

function addEl(parent, name, attrs, text) {
  const el = document.createElementNS("http://www.w3.org/2000/svg", name);
  for (const [key, value] of Object.entries(attrs)) {
    el.setAttribute(key, value);
  }
  if (text) el.textContent = text;
  parent.appendChild(el);
  return el;
}

function addText(parent, x, y, text, className, anchor) {
  return addEl(parent, "text", {
    class: className,
    x,
    y,
    "text-anchor": anchor,
  }, text);
}

function setStatus(text) {
  els.status.textContent = text;
}

function unique(values) {
  return [...new Set(values)];
}

function clamp(value, min, max) {
  return Math.min(Math.max(value, min), max);
}

function metricSort(a, b) {
  const rank = (name) => {
    if (name === "total") return 0;
    if (name.startsWith("phase.")) return 1;
    if (name.startsWith("milestone.")) return 2;
    return 3;
  };
  return rank(a) - rank(b) || a.localeCompare(b);
}

function formatSeconds(value) {
  return value.toFixed(value >= 10 ? 1 : 3);
}

function formatDate(date) {
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

function formatFullDate(date) {
  return new Intl.DateTimeFormat(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    timeZoneName: "short",
  }).format(date);
}

function shortCommit(value) {
  return value ? value.slice(0, 12) : "";
}

function escapeHtml(value) {
  return String(value).replace(/[&<>"']/g, (char) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[char]));
}
