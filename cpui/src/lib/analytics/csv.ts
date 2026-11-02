// A panel's result as CSV (RFC 4180): dimension columns labelled the way the
// panel shows them, measures as raw numbers so a spreadsheet can compute on
// them, then the row count and, when ranked, the score.

import { t } from '$lib/i18n/index.svelte';
import type { AnalyticsResult } from '$lib/domain';
import { fmtDim } from './format';

/** Quote a cell when it holds a quote, a comma or a line break. */
const cell = (v: string): string => (/[",\r\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v);

export function toCsv(result: AnalyticsResult): string {
	const scored = result.rows.some((r) => r.score != null);
	const head = [
		...result.dims.map((d) => d.label),
		...result.measures.map((m) => m.label),
		t('analytics.col.n'),
		...(scored ? [t('analytics.col.score')] : [])
	];
	const rows = result.rows.map((r) => [
		...r.dims.map((v, i) => fmtDim(result.dims[i], v, result.labels)),
		...result.measures.map((_, i) => String(r.values[i] ?? '')),
		String(r.n),
		...(scored ? [String(r.score ?? '')] : [])
	]);
	return [head, ...rows].map((line) => line.map(cell).join(',')).join('\r\n') + '\r\n';
}

/** "Compare flows" → "compare-flows.csv". */
export const csvName = (title: string): string =>
	`${
		title
			.toLowerCase()
			.replace(/[^a-z0-9]+/g, '-')
			.replace(/^-|-$/g, '') || 'panel'
	}.csv`;

/** Save `text` as a file named after `title`, through a throwaway link. */
export function downloadCsv(title: string, text: string): void {
	const url = URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' }));
	const a = document.createElement('a');
	a.href = url;
	a.download = csvName(title);
	document.body.append(a);
	a.click();
	a.remove();
	URL.revokeObjectURL(url);
}
