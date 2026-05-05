/*
 * Copyright 2026 The Grove Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

const RUNS_URL = "index/runs.ndjson";

const STACK_COLORS = [
  "#16736f",
  "#c85a32",
  "#4f6db8",
  "#9b6a1c",
  "#6f5499",
  "#2f7d43",
  "#a34d73",
  "#53616f",
];

const state = {
  runs: [],
  testName: "",
};

const els = {
  status: document.getElementById("status"),
  testSelect: document.getElementById("test-select"),
  totalChart: document.getElementById("total-chart"),
  phaseChart: document.getElementById("phase-chart"),
  totalSummary: document.getElementById("total-summary"),
  phaseLegend: document.getElementById("phase-legend"),
  milestoneCharts: document.getElementById("milestone-charts"),
  latestBody: document.getElementById("latest-body"),
  tooltip: document.getElementById("tooltip"),
};

init();

async function init() {
  try {
    const text = await fetchText(RUNS_URL);
    state.runs = parseNdjson(text);
    if (state.runs.length === 0) {
      setStatus("No runs found.");
      drawEmpty(els.totalChart, "No runs found");
      return;
    }

    populateSelectors();
    els.testSelect.addEventListener("change", () => {
      state.testName = els.testSelect.value;
      render();
    });
    window.addEventListener("resize", render);
    render();
  } catch (err) {
    setStatus(`Failed to load ${RUNS_URL}: ${err.message}`);
    drawEmpty(els.totalChart, "Failed to load runs");
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
        return normalizeRun(JSON.parse(line));
      } catch (err) {
        throw new Error(`line ${index + 1}: ${err.message}`);
      }
    });
}

function normalizeRun(run) {
  run.totalSeconds = Number(run.totalSeconds);
  run.date = new Date(run.runTimestamp);
  run.phases = Array.isArray(run.phases) ? run.phases.map(normalizePhase) : [];

  if (!run.testName || !run.runID || !Number.isFinite(run.totalSeconds) || Number.isNaN(run.date.getTime())) {
    throw new Error("missing required run fields");
  }

  run.commit = run.commit || "";
  run.runURL = run.runURL || "";
  run.resultPath = run.resultPath || "";
  return run;
}

function normalizePhase(phase) {
  phase.valueSeconds = Number(phase.valueSeconds);
  phase.milestones = Array.isArray(phase.milestones) ? phase.milestones.map(normalizeMilestone) : [];
  if (!phase.name || !Number.isFinite(phase.valueSeconds)) {
    throw new Error("missing required phase fields");
  }
  return phase;
}

function normalizeMilestone(milestone) {
  milestone.valueSeconds = Number(milestone.valueSeconds);
  if (!milestone.name || !Number.isFinite(milestone.valueSeconds)) {
    throw new Error("missing required milestone fields");
  }
  return milestone;
}

function populateSelectors() {
  const tests = unique(state.runs.map((run) => run.testName)).sort();
  state.testName = tests[0] || "";
  setOptions(els.testSelect, tests);
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
  const runs = selectedRuns();
  setStatus(`${runs.length} runs loaded`);

  drawTotalChart(runs);
  drawPhaseChart(runs);
  renderMilestoneCharts(runs);
  renderLatestTable(runs);
}

function selectedRuns() {
  return state.runs
    .filter((run) => run.testName === state.testName)
    .sort((a, b) => a.date - b.date);
}

function drawTotalChart(runs) {
  const rows = runs
    .map((run) => ({ run, value: run.totalSeconds }))
    .filter((item) => Number.isFinite(item.value));

  const svg = els.totalChart;
  const dims = prepareChart(svg, { top: 20, right: 28, bottom: 42, left: 58 });
  if (rows.length === 0) {
    drawEmpty(svg, "No total runtime points");
    els.totalSummary.replaceChildren();
    return;
  }

  const values = rows.map((row) => row.value);
  const average = values.reduce((sum, value) => sum + value, 0) / values.length;
  const latest = rows[rows.length - 1].value;
  const delta = average > 0 ? ((latest - average) / average) * 100 : 0;
  renderSummary(els.totalSummary, [
    ["Latest", `${formatSeconds(latest)}s`],
    ["Average", `${formatSeconds(average)}s`],
    ["Delta", `${formatSignedPercent(delta)}`],
  ]);

  const minX = rows[0].run.date.getTime();
  const maxX = rows[rows.length - 1].run.date.getTime();
  const maxY = Math.max(...values, average, 0.001);
  const yTop = maxY * 1.15;

  const xScale = (value) => {
    if (minX === maxX) return dims.margin.left + dims.plotW / 2;
    return dims.margin.left + ((value - minX) / (maxX - minX)) * dims.plotW;
  };
  const yScale = (value) => dims.margin.top + dims.plotH - (value / yTop) * dims.plotH;

  drawGrid(svg, dims, yTop);

  const avgY = yScale(average);
  addEl(svg, "line", {
    class: "average-line",
    x1: dims.margin.left,
    y1: avgY,
    x2: dims.width - dims.margin.right,
    y2: avgY,
  });
  addText(svg, dims.width - dims.margin.right, avgY - 6, `avg ${formatSeconds(average)}s`, "average-label", "end");

  const points = rows.map((row) => [xScale(row.run.date.getTime()), yScale(row.value), row]);
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
      r: 13,
    });
    hit.addEventListener("mouseenter", (event) => {
      marker.classList.add("point-active");
      showTooltip(event, tooltipRows(row.run, [
        ["Total", `${formatSeconds(row.value)}s`],
        ["Average", `${formatSeconds(average)}s`],
      ]));
    });
    hit.addEventListener("mousemove", (event) => {
      showTooltip(event, tooltipRows(row.run, [
        ["Total", `${formatSeconds(row.value)}s`],
        ["Average", `${formatSeconds(average)}s`],
      ]));
    });
    hit.addEventListener("mouseleave", () => {
      marker.classList.remove("point-active");
      hideTooltip();
    });
  }

  drawDateLabels(svg, dims, rows.map((row) => row.run));
}

function drawPhaseChart(runs) {
  const phaseNames = orderedPhaseNames(runs);
  const colors = colorMap(phaseNames);
  const stacks = runs.map((run) => ({
    run,
    segments: run.phases.map((phase) => ({
      name: phaseLabel(phase.name),
      value: phase.valueSeconds,
      color: colors.get(phase.name),
    })).filter((segment) => Number.isFinite(segment.value)),
  })).filter((stack) => stack.segments.length > 0);

  renderLegend(els.phaseLegend, phaseNames.map((name) => ({ name: phaseLabel(name), color: colors.get(name) })));
  drawStackedBars(els.phaseChart, stacks);
}

function renderMilestoneCharts(runs) {
  els.milestoneCharts.replaceChildren();
  const phaseNames = orderedPhaseNames(runs)
    .filter((phaseName) => runs.some((run) => (phaseByName(run, phaseName)?.milestones || []).length > 0));

  for (const phaseName of phaseNames) {
    const article = document.createElement("article");
    article.className = "chart-panel";

    const header = document.createElement("header");
    header.className = "chart-panel-header";

    const title = document.createElement("h2");
    title.textContent = `${phaseLabel(phaseName)} Milestones`;

    const legend = document.createElement("div");
    legend.className = "legend";

    const frame = document.createElement("div");
    frame.className = "chart-frame";

    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.classList.add("chart");
    svg.setAttribute("role", "img");
    svg.setAttribute("aria-label", `${phaseLabel(phaseName)} milestone runtime by run`);

    header.append(title, legend);
    frame.append(svg);
    article.append(header, frame);
    els.milestoneCharts.append(article);

    drawMilestoneChart(runs, phaseName, svg, legend);
  }
}

function drawMilestoneChart(runs, phaseName, svg, legend) {
  const milestoneNames = orderedMilestoneNames(runs, phaseName);
  const colors = colorMap([...milestoneNames, "phase tail"]);
  const stacks = runs.map((run) => {
    const phase = phaseByName(run, phaseName);
    if (!phase) return { run, segments: [] };

    let previous = 0;
    const segments = [];

    for (const milestone of phase.milestones) {
      if (!Number.isFinite(milestone.valueSeconds)) continue;
      const value = Math.max(0, milestone.valueSeconds - previous);
      segments.push({
        name: milestone.name,
        value,
        color: colors.get(milestone.name),
      });
      previous = milestone.valueSeconds;
    }

    if (phase.valueSeconds > previous + 0.001) {
      segments.push({
        name: "phase tail",
        value: phase.valueSeconds - previous,
        color: colors.get("phase tail"),
      });
    }

    return { run, segments: segments.filter((segment) => segment.value > 0) };
  }).filter((stack) => stack.segments.length > 0);

  const legendItems = unique(stacks.flatMap((stack) => stack.segments.map((segment) => segment.name)))
    .map((name) => ({ name, color: colors.get(name) }));
  renderLegend(legend, legendItems);
  drawStackedBars(svg, stacks);
}

function drawStackedBars(svg, stacks) {
  const dims = prepareChart(svg, { top: 18, right: 24, bottom: 42, left: 58 });
  if (stacks.length === 0) {
    drawEmpty(svg, "No points for this chart");
    return;
  }

  const totals = stacks.map((stack) => stack.segments.reduce((sum, segment) => sum + segment.value, 0));
  const maxY = Math.max(...totals, 0.001);
  const yTop = maxY * 1.15;
  const yScale = (value) => dims.margin.top + dims.plotH - (value / yTop) * dims.plotH;

  drawGrid(svg, dims, yTop);

  const band = dims.plotW / stacks.length;
  const barW = Math.max(14, Math.min(48, band * 0.58));

  stacks.forEach((stack, index) => {
    const x = dims.margin.left + band * index + band / 2 - barW / 2;
    const total = totals[index];
    let accumulated = 0;

    for (const segment of stack.segments) {
      const y = yScale(accumulated + segment.value);
      const h = Math.max(1, yScale(accumulated) - y);
      const rect = addEl(svg, "rect", {
        class: "bar-segment",
        x,
        y,
        width: barW,
        height: h,
        fill: segment.color,
      });
      rect.addEventListener("mouseenter", (event) => {
        rect.classList.add("bar-active");
        showTooltip(event, tooltipRows(stack.run, [
          [segment.name, `${formatSeconds(segment.value)}s`],
          ["Stack total", `${formatSeconds(total)}s`],
        ]));
      });
      rect.addEventListener("mousemove", (event) => {
        showTooltip(event, tooltipRows(stack.run, [
          [segment.name, `${formatSeconds(segment.value)}s`],
          ["Stack total", `${formatSeconds(total)}s`],
        ]));
      });
      rect.addEventListener("mouseleave", () => {
        rect.classList.remove("bar-active");
        hideTooltip();
      });
      accumulated += segment.value;
    }
  });

  drawDateLabels(svg, dims, stacks.map((stack) => stack.run));
}

function orderedPhaseNames(runs) {
  const names = [];
  const latest = runs[runs.length - 1];
  if (latest) {
    addPhaseNames(names, latest);
  }
  for (const run of runs) {
    addPhaseNames(names, run);
  }
  return names;
}

function addPhaseNames(names, run) {
  for (const phase of run.phases) {
    if (!names.includes(phase.name)) {
      names.push(phase.name);
    }
  }
}

function orderedMilestoneNames(runs, phaseName) {
  const names = [];
  const latestPhase = [...runs].reverse()
    .map((run) => phaseByName(run, phaseName))
    .find((phase) => phase && phase.milestones.length > 0);
  if (latestPhase) {
    addMilestoneNames(names, latestPhase);
  }
  for (const run of runs) {
    const phase = phaseByName(run, phaseName);
    if (phase) {
      addMilestoneNames(names, phase);
    }
  }
  return names;
}

function addMilestoneNames(names, phase) {
  for (const milestone of phase.milestones) {
    if (!names.includes(milestone.name)) {
      names.push(milestone.name);
    }
  }
}

function phaseByName(run, name) {
  return run.phases.find((phase) => phase.name === name);
}

function prepareChart(svg, margin) {
  const width = Math.max(640, svg.clientWidth || 640);
  const height = Math.max(260, svg.clientHeight || 320);
  const plotW = width - margin.left - margin.right;
  const plotH = height - margin.top - margin.bottom;
  svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
  svg.replaceChildren();
  return { svg, width, height, margin, plotW, plotH };
}

function drawGrid(svg, dims, yTop) {
  addEl(svg, "line", {
    class: "axis",
    x1: dims.margin.left,
    y1: dims.margin.top,
    x2: dims.margin.left,
    y2: dims.margin.top + dims.plotH,
  });
  addEl(svg, "line", {
    class: "axis",
    x1: dims.margin.left,
    y1: dims.margin.top + dims.plotH,
    x2: dims.width - dims.margin.right,
    y2: dims.margin.top + dims.plotH,
  });

  for (let i = 0; i <= 4; i += 1) {
    const y = dims.margin.top + dims.plotH - (i / 4) * dims.plotH;
    const value = (i / 4) * yTop;
    addEl(svg, "line", {
      class: "grid",
      x1: dims.margin.left,
      y1: y,
      x2: dims.margin.left + dims.plotW,
      y2: y,
    });
    addText(svg, dims.margin.left - 8, y + 4, `${formatSeconds(value)}s`, "tick-label", "end");
  }
}

function drawDateLabels(svg, dims, runs) {
  if (runs.length === 0) return;
  addText(svg, dims.margin.left, dims.height - 12, formatDate(runs[0].date), "tick-label", "start");
  addText(svg, dims.width - dims.margin.right, dims.height - 12, formatDate(runs[runs.length - 1].date), "tick-label", "end");
}

function drawEmpty(svg, message) {
  const dims = prepareChart(svg, { top: 18, right: 24, bottom: 42, left: 58 });
  addText(svg, dims.width / 2, dims.height / 2, message, "empty-label", "middle");
}

function renderLatestTable(runs) {
  const latest = runs[runs.length - 1];
  if (!latest) {
    els.latestBody.replaceChildren();
    return;
  }

  const rows = [{ metric: "total", valueSeconds: latest.totalSeconds, runID: latest.runID }];
  for (const phase of latest.phases) {
    rows.push({
      metric: `phase.${phase.name}`,
      valueSeconds: phase.valueSeconds,
      runID: latest.runID,
    });
    for (const milestone of phase.milestones) {
      rows.push({
        metric: `milestone.${phase.name}.${milestone.name}`,
        valueSeconds: milestone.valueSeconds,
        runID: latest.runID,
      });
    }
  }

  els.latestBody.replaceChildren(
    ...rows.map((row) => {
      const tr = document.createElement("tr");
      tr.append(
        cell(row.metric),
        cell(`${formatSeconds(row.valueSeconds)}s`),
        cell(row.runID),
      );
      return tr;
    }),
  );
}

function renderSummary(target, items) {
  target.replaceChildren(
    ...items.map(([label, value]) => {
      const item = document.createElement("span");
      const name = document.createElement("strong");
      name.textContent = label;
      item.append(name, document.createTextNode(` ${value}`));
      return item;
    }),
  );
}

function renderLegend(target, items) {
  target.replaceChildren(
    ...items.map((item) => {
      const entry = document.createElement("span");
      const swatch = document.createElement("i");
      swatch.style.background = item.color;
      entry.append(swatch, document.createTextNode(item.name));
      return entry;
    }),
  );
}

function showTooltip(event, rows) {
  els.tooltip.innerHTML = `
    <dl>
      ${rows.map(([key, value]) => `<dt>${escapeHtml(key)}</dt><dd>${escapeHtml(value)}</dd>`).join("")}
    </dl>
  `;
  els.tooltip.hidden = false;

  const box = els.tooltip.getBoundingClientRect();
  const left = clamp(event.clientX + 14, 8, window.innerWidth - box.width - 8);
  const top = clamp(event.clientY - box.height - 14, 8, window.innerHeight - box.height - 8);
  els.tooltip.style.left = `${left}px`;
  els.tooltip.style.top = `${top}px`;
}

function hideTooltip() {
  els.tooltip.hidden = true;
}

function tooltipRows(run, rows) {
  return [
    ["Time", formatFullDate(run.date)],
    ...rows,
    ["Run", run.runID],
    ["Commit", shortCommit(run.commit)],
    ["Result", run.resultPath || ""],
  ];
}

function colorMap(names) {
  const map = new Map();
  unique(names).forEach((name, index) => {
    map.set(name, STACK_COLORS[index % STACK_COLORS.length]);
  });
  return map;
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

function phaseLabel(name) {
  return name
    .split(/[-_.\s]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function formatSeconds(value) {
  return value.toFixed(value >= 10 ? 1 : 3);
}

function formatSignedPercent(value) {
  const sign = value > 0 ? "+" : "";
  return `${sign}${value.toFixed(1)}%`;
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
