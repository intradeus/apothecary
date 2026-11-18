// Where an annotation from the Files viewer goes: the chat that can take it
// now. The held session's composer registers while the run is in your hands,
// Ask's while its pane is on; the held session wins, being the one the agent
// is waiting on. Nothing registered: the viewer's box says why it is off.
import { untrack } from 'svelte';
import { t } from '$lib/i18n/index.svelte';
import type { Annotation } from './annotation';

export type ComposerKind = 'hold' | 'ask';

export interface ComposerTarget {
	kind: ComposerKind;
	add(a: Annotation): void;
}

export class ComposerRegistry {
	/** In registration order; one entry per registration, so the same target
	 *  registered twice is two entries, each with its own unregister. */
	#entries = $state.raw<{ target: ComposerTarget }[]>([]);

	/** Adds a target; the latest of a kind wins. The returned function drops
	 *  this registration only, and is safe to call more than once. Neither
	 *  reads the registry for the caller, so an effect may register (and
	 *  unregister on cleanup) without depending on what is registered. */
	register(target: ComposerTarget): () => void {
		const entry = { target };
		untrack(() => (this.#entries = [...this.#entries, entry]));
		return () => untrack(() => (this.#entries = this.#entries.filter((e) => e !== entry)));
	}

	readonly active = $derived.by((): ComposerTarget | null => {
		const latest = (kind: ComposerKind) => this.#entries.findLast((e) => e.target.kind === kind);
		return (latest('hold') ?? latest('ask'))?.target ?? null;
	});

	/** Why there is nowhere to send to; '' when there is. */
	readonly reason = $derived(this.active ? '' : t('run.annotate.noTarget'));
}
