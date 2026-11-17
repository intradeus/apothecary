// Narrowing the finished runs: by how they ended and by whether they are
// archived. Archiving is a flag beside the outcome, so the two filter
// independently. Pure, so both runs lists (the page and a workshop's tab)
// share it and the URL round-trip.

import type { Run } from '$lib/domain';

export type OutcomeFilter = 'any' | 'completed' | 'abandoned' | 'failed';
export type ArchivedFilter = 'any' | 'no' | 'yes';

export interface FinishedFilter {
	outcome: OutcomeFilter;
	archived: ArchivedFilter;
}

export const OUTCOME_FILTERS: OutcomeFilter[] = ['any', 'completed', 'abandoned', 'failed'];
export const ARCHIVED_FILTERS: ArchivedFilter[] = ['any', 'no', 'yes'];

/** Every finished run: what the lists start from. */
export const ANY_FINISHED: FinishedFilter = { outcome: 'any', archived: 'any' };

/** Whether a finished run passes the filter. */
export const matchesFinished = (run: Pick<Run, 'state' | 'archivedAt'>, f: FinishedFilter) =>
	(f.outcome === 'any' || run.state === f.outcome) &&
	(f.archived === 'any' || (f.archived === 'yes') === !!run.archivedAt);

const oneOf = <T extends string>(allowed: readonly T[], v: string | null): T =>
	allowed.includes(v as T) ? (v as T) : allowed[0];

/** The filter a URL's ?outcome= and ?archived= say; anything else is "any". */
export const readFinishedFilter = (params: URLSearchParams): FinishedFilter => ({
	outcome: oneOf(OUTCOME_FILTERS, params.get('outcome')),
	archived: oneOf(ARCHIVED_FILTERS, params.get('archived'))
});

/** The URL params of a filter (`to.runs`): "any" is left out. */
export const finishedParams = (f: FinishedFilter) => ({
	outcome: f.outcome === 'any' ? undefined : f.outcome,
	archived: f.archived === 'any' ? undefined : f.archived
});
