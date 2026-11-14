// Rebuilding a run's workspace, from the header's menu or the broken-workspace
// banner: one request under the header's 'rebuild' action, so both show it
// busy, and the same toasts either way.
import { api } from '$lib/api';
import { t } from '$lib/i18n/index.svelte';
import { toast } from '$lib/ui/toast.svelte';
import type { DeskState } from '$lib/desk/deskState.svelte';
import type { HeaderAct } from './actions.svelte';

/** Rebuilds `runId`'s workspace, from its image cache or (`noCache`) from
 *  scratch: false when it failed, the failure left to the store. */
export async function rebuild(
	runId: string,
	noCache: boolean,
	{ act, desk }: { act: HeaderAct; desk: Pick<DeskState, 'reveal'> }
): Promise<boolean> {
	// The request blocks for the whole build; the badge and the Logs tail
	// show progress meanwhile.
	toast(noCache ? t('run.rebuild.startedNoCache') : t('run.rebuild.started'), {
		tone: 'info',
		action: { label: t('run.rebuild.watchLogs'), run: () => desk.reveal('logs') }
	});
	const ok = await act('rebuild', () => api.runs.rebuild(runId, noCache));
	if (ok) toast(t('run.rebuild.done'));
	return ok;
}
