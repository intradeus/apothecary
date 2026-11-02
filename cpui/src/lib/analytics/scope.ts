// How a dashboard's controls reach its panels: the range picker becomes each
// query's `time.from`, the filter bar `in` filters on the panels whose grain
// has that dimension; and how a clicked row narrows a drill.
import type {
	AnalyticsFilter,
	AnalyticsQuery,
	AnalyticsResult,
	AnalyticsRow,
	Bucket,
	TimeRange
} from '$lib/domain';
import { bucketDate } from './viz/shape';

export type Range = '30d' | '90d' | '1y' | 'all';
export const RANGES: readonly Range[] = ['30d', '90d', '1y', 'all'];
export const DEFAULT_RANGE: Range = '90d';

const DAYS: Record<Exclude<Range, 'all'>, number> = { '30d': 30, '90d': 90, '1y': 365 };

/** The start of the range, from `now` (RFC 3339); none for all time. */
export const rangeFrom = (r: Range, now: Date = new Date()): string | undefined =>
	r === 'all' ? undefined : new Date(+now - DAYS[r] * 86_400_000).toISOString();

/** The dimension values picked in the filter bar, by dimension id. */
export type Picks = Record<string, string[]>;

/** A panel's saved query as the dashboard runs it: from the range's start,
 *  narrowed by every pick its grain can take (`has` says which). */
export function scoped(
	q: AnalyticsQuery,
	from: string | undefined,
	picks: Picks,
	has: (dim: string) => boolean
): AnalyticsQuery {
	const extra: AnalyticsFilter[] = Object.entries(picks)
		.filter(([dim, values]) => values.length && has(dim))
		.map(([field, values]) => ({ field, op: 'in', values }));
	const time: TimeRange = { ...(q.time.bucket ? { bucket: q.time.bucket } : {}) };
	if (from) time.from = from;
	return { ...q, filters: [...q.filters, ...extra], time };
}

/** The end of the bucket starting at `start`. */
function bucketEnd(start: Date, b: Bucket): Date {
	const end = new Date(start);
	if (b === 'day') end.setUTCDate(end.getUTCDate() + 1);
	else if (b === 'week') end.setUTCDate(end.getUTCDate() + 7);
	else end.setUTCMonth(end.getUTCMonth() + 1);
	return end;
}

/** What a drill asks for: the query's filters and range, plus — for a
 *  clicked row — an `eq` filter per dimension, its bucket turned into the
 *  bucket's own time range (the engine takes no filter on a bucket). */
export function drillScope(
	q: AnalyticsQuery,
	result?: AnalyticsResult,
	row?: AnalyticsRow
): { filters: AnalyticsFilter[]; time: TimeRange } {
	const filters = [...q.filters];
	const time: TimeRange = {
		...(q.time.from ? { from: q.time.from } : {}),
		...(q.time.to ? { to: q.time.to } : {})
	};
	if (!result || !row) return { filters, time };
	result.dims.forEach((d, i) => {
		const v = row.dims[i] ?? '';
		if (d.id !== 'bucket') {
			filters.push({ field: d.id, op: 'eq', values: [v] });
			return;
		}
		const start = bucketDate(v);
		const end = bucketEnd(start, q.time.bucket ?? 'day');
		if (!time.from || start > new Date(time.from)) time.from = start.toISOString();
		if (!time.to || end < new Date(time.to)) time.to = end.toISOString();
	});
	return { filters, time };
}
