// How analytics values read. A field's catalog type decides: money as money,
// milliseconds as a duration, ratios as percentages. Dimension values are
// text on the wire; the result's labels turn ids into names.

import { fmtCost, fmtDuration, fmtPct } from '$lib/core/format';
import { t } from '$lib/i18n/index.svelte';
import type { AnalyticsFieldType, AnalyticsResult, CatalogField } from '$lib/domain';

/** "950", "12.3k", "4.1M". */
function fmtTokens(v: number): string {
	const a = Math.abs(v);
	if (a < 1000) return String(Math.round(v));
	if (a < 1_000_000) return `${Math.round(v / 100) / 10}k`;
	return `${Math.round(v / 100_000) / 10}M`;
}

/** One measure value, by its field's type. Null (nothing aggregated) is a dash. */
export function fmtValue(type: AnalyticsFieldType, v: number | null | undefined): string {
	if (v == null || Number.isNaN(v)) return '—';
	switch (type) {
		case 'usd':
			return fmtCost(v);
		case 'ms':
			// A command or gate stage is over in well under a second: "230ms",
			// where a whole-second duration would read "0s".
			return v > 0 && v < 1000 ? `${Math.round(v)}ms` : fmtDuration(v);
		case 'ratio':
			return fmtPct(v * 100);
		case 'tokens':
			return fmtTokens(v);
		default:
			// Counts may be averages: at most two decimals, none when whole.
			return String(Math.round(v * 100) / 100);
	}
}

/** One dimension value as a reader sees it: the result's label for it (a
 *  project's name, "builder v3"), a date for a time bucket, yes/no for a
 *  boolean, "(none)" for the empty string. */
export function fmtDim(
	field: CatalogField,
	value: string,
	labels: AnalyticsResult['labels'] = {}
): string {
	const label = labels[field.id]?.[value];
	if (label) return label;
	if (value === '') return t('analytics.none');
	if (field.type === 'bool') {
		if (value === 'true') return t('analytics.yes');
		if (value === 'false') return t('analytics.no');
	}
	if (field.type === 'time') return /^\d{4}-\d{2}-\d{2}/.exec(value)?.[0] ?? value;
	return value;
}

/** A row's dimensions joined for a legend or a heading: "builder v3 · bug". */
export const fmtDims = (result: AnalyticsResult, dims: string[]): string =>
	dims.map((v, i) => fmtDim(result.dims[i], v, result.labels)).join(' · ');
