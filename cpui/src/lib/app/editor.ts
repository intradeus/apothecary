// Opening a run's workspace in code-server, from any page that offers it.
import { api } from '$lib/api';

/** Opens the run's editor in a new tab. The tab opens synchronously, inside
 *  the click, so it is not blocked; code-server boots for a few seconds on
 *  first open. A failure closes the tab and rethrows. */
export async function openRunEditor(runId: string): Promise<void> {
	const w = window.open('', '_blank');
	try {
		const url = await api.runs.openEditor(runId);
		if (w) w.location.href = url;
		else window.open(url, '_blank');
	} catch (e) {
		w?.close();
		throw e;
	}
}
