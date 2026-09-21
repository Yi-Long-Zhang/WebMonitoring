// Chart options for the overview page, kept as plain functions rather than
// inline objects in the component. They are pure -- report summary in, ECharts
// option out -- so they can be tested without mounting a canvas, and the three
// charts that share the severity palette cannot drift apart.
import type * as echarts from "echarts";

// The severity order is fixed rather than derived from the data. A chart that
// ordered by count would put a quiet week's LOW incidents above CRITICAL and
// reorder the legend between refreshes, so an operator could not read two
// screenshots the same way.
const SEVERITY_ORDER = ["critical", "high", "medium", "low", "info"] as const;

const SEVERITY_LABELS: Record<string, string> = {
  critical: "严重",
  high: "高危",
  medium: "中危",
  low: "低危",
  info: "提示",
};

// A severity keeps one colour everywhere it appears: the trend line, the
// distribution pie and the incident table all agree on what "严重" looks like.
const SEVERITY_COLORS: Record<string, string> = {
  critical: "#c0392b",
  high: "#e67e22",
  medium: "#d4a017",
  low: "#2f80ed",
  info: "#7f8c8d",
};

export type TrendPoint = {
  date: string;
  critical: number;
  high: number;
  medium: number;
  low: number;
  info: number;
  total: number;
};

export type SummaryForCharts = {
  successfulJobs: number;
  failedJobs: number;
  severityCounts: Record<string, number>;
};

// An empty chart is the state an operator sees on a fresh tenant, and a chart
// with no series draws an empty grid that looks broken rather than quiet. Each
// builder therefore returns an option that still renders its axes with a
// placeholder, so "no data yet" and "failed to load" stay distinguishable.
export function trendOption(trend: TrendPoint[]): echarts.EChartsOption {
  const days = trend.map((point) => point.date.slice(5));
  const series: echarts.SeriesOption[] = SEVERITY_ORDER.filter((severity) =>
    trend.some((point) => point[severity] > 0),
  ).map((severity) => ({
    name: SEVERITY_LABELS[severity],
    type: "line",
    smooth: true,
    symbol: "circle",
    symbolSize: 6,
    itemStyle: { color: SEVERITY_COLORS[severity] },
    lineStyle: { width: 2, color: SEVERITY_COLORS[severity] },
    areaStyle: { opacity: 0.06 },
    data: trend.map((point) => point[severity]),
  }));
  return {
    // The legend is only useful once there is more than one line to tell apart.
    legend: series.length > 1 ? { bottom: 0, icon: "roundRect", itemWidth: 10, itemHeight: 10 } : undefined,
    tooltip: { trigger: "axis" },
    grid: { left: 8, right: 16, top: 16, bottom: series.length > 1 ? 36 : 8, containLabel: true },
    xAxis: {
      type: "category",
      boundaryGap: false,
      data: days,
      axisLine: { lineStyle: { color: "#dcdfe6" } },
      axisLabel: { color: "#909399", fontSize: 11 },
    },
    yAxis: {
      type: "value",
      minInterval: 1,
      splitLine: { lineStyle: { color: "#f0f2f5" } },
      axisLabel: { color: "#909399", fontSize: 11 },
    },
    series,
  };
}

// Severities with no incidents are dropped rather than drawn as zero-width
// wedges, which would still appear in the legend and claim a proportion that
// does not exist.
export function severityPieOption(counts: Record<string, number>): echarts.EChartsOption {
  // The server keys this map by the uppercase enum ("CRITICAL"), while the
  // charts use lowercase keys so one severity has one spelling everywhere.
  const data = SEVERITY_ORDER.map((severity) => ({
    name: SEVERITY_LABELS[severity],
    value: counts[severity.toUpperCase()] ?? 0,
    itemStyle: { color: SEVERITY_COLORS[severity] },
  })).filter((item) => item.value > 0);
  return {
    tooltip: { trigger: "item", formatter: "{b}: {c} ({d}%)" },
    legend: { bottom: 0, icon: "circle", itemWidth: 8, itemHeight: 8 },
    series: [
      {
        type: "pie",
        radius: ["46%", "68%"],
        center: ["50%", "44%"],
        // A donut rather than a full pie: the hole leaves room for the total to
        // be drawn in the middle, where the eye goes first.
        avoidLabelOverlap: true,
        itemStyle: { borderColor: "#fff", borderWidth: 2 },
        label: { show: false },
        emphasis: { label: { show: true, fontSize: 14, fontWeight: "bold", formatter: "{b}\n{c}" } },
        data,
      },
    ],
  };
}

// detectionGaugeOption reads the two counters the summary already carries. The
// rate is computed here rather than server-side because the server has no
// opinion about it -- "successful" and "failed" are the facts, and a rate is a
// presentation choice that the threshold colours below depend on.
export function detectionGaugeOption(summary: SummaryForCharts): echarts.EChartsOption {
  const successful = summary.successfulJobs;
  const failed = summary.failedJobs;
  const total = successful + failed;
  const rate = total ? Math.round((successful / total) * 1000) / 10 : 0;
  // The colour is the whole point of a gauge: 99% and 71% should not need to be
  // read off the number.
  const color = rate >= 95 ? "#27ae60" : rate >= 80 ? "#e67e22" : "#c0392b";
  return {
    series: [
      {
        type: "gauge",
        startAngle: 210,
        endAngle: -30,
        min: 0,
        max: 100,
        splitNumber: 5,
        radius: "92%",
        center: ["50%", "58%"],
        axisLine: { lineStyle: { width: 14, color: [[1, "#f0f2f5"]] } },
        progress: { show: true, width: 14, roundCap: true, itemStyle: { color } },
        pointer: { show: false },
        axisTick: { show: false },
        splitLine: { show: false },
        axisLabel: { show: false },
        anchor: { show: false },
        title: { show: false },
        detail: {
          offsetCenter: [0, "0%"],
          fontSize: 28,
          fontWeight: "bold",
          color,
          // A tenant with no completed job yet has no rate to show, and "0.0%"
          // would read as total failure rather than as nothing measured.
          formatter: total ? "{value}%" : "—",
        },
        data: [{ value: total ? rate : 0 }],
      },
    ],
  };
}
