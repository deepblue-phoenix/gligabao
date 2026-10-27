// Formatting helpers shared by the admin UI's modules.

// "1.50 KiB" / "12.3 MiB" for a number of bytes.
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let v = n;
  let i = -1;
  do { v /= 1024; i++; } while (v >= 1024 && i < units.length - 1);
  return `${v.toFixed(v < 10 ? 2 : 1)} ${units[i]}`;
}

// "5 mins ago" / "3 hours ago" / "12 days ago" / "2 months ago" for an ISO timestamp ("just now" under a minute;
// a month is 30 days). "" if it cannot be parsed.
export function ageText(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const mins = Math.max(0, Math.floor((Date.now() - t) / 60000));
  const plural = (n: number, unit: string) => `${n} ${unit}${n === 1 ? "" : "s"} ago`;
  if (mins < 1) return "just now";
  if (mins < 60) return plural(mins, "min");
  if (mins < 1440) return plural(Math.floor(mins / 60), "hour");
  if (mins < 43200) return plural(Math.floor(mins / 1440), "day");
  return plural(Math.floor(mins / 43200), "month");
}
