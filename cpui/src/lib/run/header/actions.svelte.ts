// What the run header has in flight. Actions can overlap: a rebuild blocks for
// the whole build, and the editor stays reachable meanwhile. So this is a set,
// not one value: an action finishing only ever clears itself, never the lock
// another one holds.
import { SvelteSet } from 'svelte/reactivity';

export type HeaderAction = 'pause' | 'play' | 'stop' | 'retry' | 'archive' | 'editor' | 'rebuild';

export class HeaderActions {
	#running = new SvelteSet<HeaderAction>();

	/** Anything in flight: holds every button that changes the run. */
	get any(): boolean {
		return this.#running.size > 0;
	}

	has(which: HeaderAction): boolean {
		return this.#running.has(which);
	}

	/** Runs `fn` under `which`, once: false when it failed, or was already
	 *  running (a double click, or the same action from a second control). */
	async run(which: HeaderAction, fn: () => Promise<boolean>): Promise<boolean> {
		if (this.#running.has(which)) return false;
		this.#running.add(which);
		try {
			return await fn();
		} finally {
			this.#running.delete(which);
		}
	}
}

/** Runs a store action under `which`: false when it failed. */
export type HeaderAct = (which: HeaderAction, fn: () => Promise<unknown>) => Promise<boolean>;
