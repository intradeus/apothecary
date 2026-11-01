// The editing rules of a tracker's status and type lists, kept out of the
// component so they can be tested on their own.

import { FLOW_STEPS, duplicateSteps, type FlowStep, type TrackerStatus } from '$lib/domain';
import type { SelectOption } from '$lib/ui/Select.svelte';
import { t } from '$lib/i18n/index.svelte';

/** The "no step" choice of the flow-step select. */
export const NO_STEP = 'none';
export type StepChoice = FlowStep | typeof NO_STEP;

/** The flow-step choices for one row: a step another row already holds is
 *  offered disabled, since a tracker gives each step to one status. */
export function stepOptions(rows: TrackerStatus[], index: number): SelectOption<StepChoice>[] {
	const taken = new Set(rows.flatMap((r, i) => (i !== index && r.flowStep ? [r.flowStep] : [])));
	return [
		...FLOW_STEPS.map((s) => ({
			value: s,
			label: t(`tracker.step.${s}`),
			disabled: taken.has(s)
		})),
		{ value: NO_STEP, label: t('tracker.step.none') }
	];
}

/** Why a status list can't be saved as it stands, or '' when it can. */
export function statusListProblem(rows: TrackerStatus[]): string {
	const dup = duplicateSteps(rows);
	if (dup.length) return t('tracker.problem.stepTwice', { step: t(`tracker.step.${dup[0]}`) });
	return namesProblem(rows);
}

/** Why a list's names can't be saved: an empty one or two the same (case
 *  aside, as the engine compares them). */
export function namesProblem(rows: { name: string }[]): string {
	const seen = new Set<string>();
	for (const r of rows) {
		const n = r.name.trim().toLowerCase();
		if (!n) return t('tracker.problem.emptyName');
		if (seen.has(n)) return t('tracker.problem.nameTwice', { name: r.name.trim() });
		seen.add(n);
	}
	return '';
}

/** The row moved by delta places (clamped), positions renumbered. */
export function moveRow<T extends { position: number }>(
	rows: T[],
	index: number,
	delta: number
): T[] {
	const to = Math.max(0, Math.min(rows.length - 1, index + delta));
	const next = [...rows];
	const [row] = next.splice(index, 1);
	next.splice(to, 0, row);
	return next.map((r, i) => ({ ...r, position: i }));
}

/** The saved rows a list no longer has: deleting them needs a replacement
 *  when tickets use them. */
export function removedRows<T extends { id: string }>(saved: T[], rows: T[]): T[] {
	const kept = new Set(rows.map((r) => r.id));
	return saved.filter((s) => s.id && !kept.has(s.id));
}
