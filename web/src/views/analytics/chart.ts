// Shared helpers for the analytics charts: a readable axis scale, compact
// numbers, and date formatting. Kept out of the components so every chart on the
// page rounds and labels the same way.

const NICE_STEPS = [1, 1.25, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10];

// niceMax rounds a peak up to a number a person would choose for an axis, so the
// top gridline reads 250 rather than 237.
export function niceMax(values: number[]): number {
  const max = Math.max(0, ...values);
  if (max === 0) return 0;
  const magnitude = Math.pow(10, Math.floor(Math.log10(max)));
  const ratio = max / magnitude;
  const step = NICE_STEPS.find((n) => n >= ratio) ?? 10;
  return Math.round(step * magnitude);
}

// heightPct keeps a non-zero value visible: a single send in a busy month would
// otherwise round to an invisible sliver.
export function heightPct(value: number, peak: number): string {
  if (peak <= 0) return "0%";
  const pct = (value / peak) * 100;
  return `${value > 0 ? Math.max(pct, 1.5) : 0}%`;
}

export function formatNumber(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "M";
  if (n >= 10_000) return (n / 1_000).toFixed(1) + "K";
  return n.toLocaleString();
}

export function formatPercent(n: number, digits = 1): string {
  return `${n.toFixed(digits)}%`;
}

export function formatLatency(seconds: number): string {
  if (seconds <= 0) return "—";
  if (seconds < 1) return `${Math.round(seconds * 1000)}ms`;
  if (seconds < 60) return `${seconds.toFixed(1)}s`;
  if (seconds < 3600) return `${(seconds / 60).toFixed(1)}m`;
  return `${(seconds / 3600).toFixed(1)}h`;
}

// Dates arrive as YYYY-MM-DD. Parsing them bare would be read as UTC midnight and
// shift a day backwards for anyone west of Greenwich, so anchor them locally.
function localDate(dateStr: string): Date {
  return new Date(dateStr.length === 10 ? `${dateStr}T00:00:00` : dateStr);
}

export function shortDate(dateStr: string): string {
  return localDate(dateStr).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export function longDate(dateStr: string): string {
  return localDate(dateStr).toLocaleDateString(undefined, {
    weekday: "short",
    month: "short",
    day: "numeric",
  });
}

// Thin the axis so labels never collide: always keep the ends, then take an even
// stride through the middle.
export function axisLabels(count: number, target = 7): (index: number) => boolean {
  const stride = Math.max(1, Math.ceil(count / target));
  return (index: number) => index === 0 || index === count - 1 || index % stride === 0;
}

export function isoDay(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

// Delta between a period and the one before it. Returns null when there is no
// prior figure to compare against, so the UI can say nothing rather than "+100%".
export function delta(current: number, previous: number): number | null {
  if (previous === 0) return current === 0 ? 0 : null;
  return ((current - previous) / previous) * 100;
}

// dayCount is inclusive of both ends, matching how the ranges are presented:
// "7 days" covers today and the six before it.
export function dayCount(from: string, to: string): number {
  const start = new Date(`${from}T00:00:00`);
  const end = new Date(`${to}T00:00:00`);
  return Math.max(1, Math.round((end.getTime() - start.getTime()) / 86_400_000) + 1);
}

// precedingWindow is the equally long window ending the day before this one, so
// a period-over-period comparison is like for like and the two never overlap.
export function precedingWindow(from: string, to: string): { from: string; to: string } {
  const days = dayCount(from, to);
  const priorEnd = new Date(`${from}T00:00:00`);
  priorEnd.setDate(priorEnd.getDate() - 1);
  const priorStart = new Date(priorEnd);
  priorStart.setDate(priorStart.getDate() - (days - 1));
  return { from: isoDay(priorStart), to: isoDay(priorEnd) };
}
