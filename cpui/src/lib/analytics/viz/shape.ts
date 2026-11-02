// What every chart reads off a result: the value columns, a row's label, the
// series colours. A query with no measures is a count: `n` is its one value.
import { HUES } from '$lib/core/urls';
import type { AnalyticsResult, AnalyticsRow, CatalogField } from '$lib/domain';
import { t } from '$lib/i18n/index.svelte';
import { fmtDim, fmtDims } from '../format';

/** Series colours cycle the role-token hues (a theme decides what they are). */
export const seriesColor = (i: number): string => HUES[i % HUES.length];

/** The value columns: the measures, or the count when there are none. */
export const valueFields = (r: AnalyticsResult): CatalogField[] =>
	r.measures.length
		? r.measures
		: [{ id: 'n', label: t('analytics.viz.count'), type: 'count', aggs: [] }];

/** A row's values in `valueFields` order. */
export const valuesOf = (r: AnalyticsResult, row: AnalyticsRow): (number | null)[] =>
	r.measures.length ? row.values : [row.n];

/** Dimension `i` of a row as read ("builder v3", a bucket's date); one the
 *  query lacks (a heatmap grouped once) reads as none. */
export const dimText = (r: AnalyticsResult, row: AnalyticsRow, i: number): string =>
	r.dims[i] ? fmtDim(r.dims[i], row.dims[i] ?? '', r.labels) : t('analytics.none');

/** A row's dimensions joined; a row with none is the whole selection. */
export const rowLabel = (r: AnalyticsResult, row: AnalyticsRow): string =>
	row.dims.length ? fmtDims(r, row.dims) : t('analytics.viz.all');

/** A bucket value ("2026-10-05 00:00:00+00") as a Date: buckets are whole
 *  days, so its date part is all of it. */
export const bucketDate = (raw: string): Date => new Date(`${raw.slice(0, 10)}T00:00:00Z`);
