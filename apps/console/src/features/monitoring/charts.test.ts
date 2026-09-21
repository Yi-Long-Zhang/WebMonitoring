import { describe, expect, it } from "vitest";
import { detectionGaugeOption, severityPieOption, trendOption, type TrendPoint } from "./charts";

function day(date: string, counts: Partial<TrendPoint> = {}): TrendPoint {
  const point = { date, critical: 0, high: 0, medium: 0, low: 0, info: 0, total: 0, ...counts };
  point.total = point.critical + point.high + point.medium + point.low + point.info;
  return point;
}

// The severity key the server sends is the uppercase enum, while the charts key
// on lowercase so one severity has one spelling everywhere. Getting this wrong
// loses every wedge silently -- the pie renders as an empty donut with no error.
describe("severityPieOption", () => {
  it("reads the server's uppercase severity keys", () => {
    const option = severityPieOption({ CRITICAL: 2, HIGH: 3 });
    const data = (option.series as { data: { name: string; value: number }[] }[])[0].data;
    expect(data.map((item) => item.value)).toEqual([2, 3]);
    expect(data.map((item) => item.name)).toEqual(["严重", "高危"]);
  });

  it("drops severities with no incidents", () => {
    const option = severityPieOption({ CRITICAL: 1 });
    const data = (option.series as { data: unknown[] }[])[0].data;
    expect(data).toHaveLength(1);
  });

  it("renders an empty donut rather than throwing on no data", () => {
    const option = severityPieOption({});
    const data = (option.series as { data: unknown[] }[])[0].data;
    expect(data).toEqual([]);
  });
});

describe("trendOption", () => {
  it("keeps a fixed severity order regardless of counts", () => {
    // A quiet week's INFO incidents must not sort above CRITICAL, or the legend
    // would reorder between refreshes and two screenshots could not be compared.
    const option = trendOption([day("2026-09-01", { info: 9, critical: 1 })]);
    const names = (option.series as { name: string }[]).map((series) => series.name);
    expect(names).toEqual(["严重", "提示"]);
  });

  it("hides the legend when there is only one line to tell apart", () => {
    const single = trendOption([day("2026-09-01", { high: 1 })]);
    expect(single.legend).toBeUndefined();
    const multi = trendOption([day("2026-09-01", { high: 1, low: 1 })]);
    expect(multi.legend).toBeDefined();
  });

  it("labels the axis with the month and day, not the full date", () => {
    const option = trendOption([day("2026-09-01"), day("2026-09-02")]);
    expect((option.xAxis as { data: string[] }).data).toEqual(["09-01", "09-02"]);
  });

  it("produces no series for an empty range", () => {
    const option = trendOption([]);
    expect(option.series).toEqual([]);
    expect((option.xAxis as { data: unknown[] }).data).toEqual([]);
  });
});

// The gauge is the one chart whose colour carries the message, so the
// thresholds are the behaviour worth pinning rather than the geometry.
describe("detectionGaugeOption", () => {
  function detailColor(summary: { successfulJobs: number; failedJobs: number; severityCounts: Record<string, number> }) {
    const series = (detectionGaugeOption(summary).series as unknown[])[0] as {
      progress: { itemStyle: { color: string } };
    };
    return series.progress.itemStyle.color;
  }

  it("colours a healthy rate green and a failing one red", () => {
    expect(detailColor({ successfulJobs: 99, failedJobs: 1, severityCounts: {} })).toBe("#27ae60");
    expect(detailColor({ successfulJobs: 70, failedJobs: 30, severityCounts: {} })).toBe("#c0392b");
  });

  it("shows a dash rather than 0% when nothing has run", () => {
    // "0.0%" on a tenant that has never run a detection reads as total failure
    // rather than as nothing measured, which are different things.
    const series = (detectionGaugeOption({ successfulJobs: 0, failedJobs: 0, severityCounts: {} }).series as unknown[])[0] as {
      detail: { formatter: string };
    };
    expect(series.detail.formatter).toBe("—");
  });

  it("counts timeouts as failures through the failedJobs field", () => {
    const series = (detectionGaugeOption({ successfulJobs: 1, failedJobs: 1, severityCounts: {} }).series as unknown[])[0] as {
      data: { value: number }[];
    };
    expect(series.data[0].value).toBe(50);
  });
});
