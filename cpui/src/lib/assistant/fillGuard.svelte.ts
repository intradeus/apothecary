// What a page keeps around an assistant's fill (docs/assistant-plan.md,
// decision 10): the values it replaced (Undo), the fields it changed (the
// changed marker), and the click it waits for when the page changed after
// the question was asked — never overwriting newer edits on its own.

import type { Filled } from '$lib/domain';

/** What the panel uses of a page's guard, whatever the page holds. */
export interface PageGuard {
	readonly changed: string[];
	readonly pending: { fill: unknown; changed: string[] } | null;
	/** A question of this page's is being answered: a fill is its. */
	readonly asking: boolean;
	ask(): void;
	/** The answer ended: a fill now is not this page's. */
	answered(): void;
	offer(fill: unknown): boolean;
	/** The person asked for this fill (an earlier answer's): applied to the
	 *  page as it stands, its own Undo. false: not this page's fill. */
	applyNow(fill: unknown): boolean;
	applyPending(): void;
	discardPending(): void;
	revert(): void;
}

export class FillGuard<T> implements PageGuard {
	/** The values the last fill replaced: Undo puts them back. */
	undo = $state<T | null>(null);
	/** The fields the last fill changed. */
	changed = $state<string[]>([]);
	/** A fill waiting for a click: the page changed since the question.
	 *  changed is what it would change now. */
	pending = $state<{ fill: unknown; changed: string[] } | null>(null);
	/** A question of this page's is being answered. */
	asking = $state(false);
	/** The page as the last question or fill left it: newer edits differ. */
	#asked: string | null = null;
	/** The page before this question's first fill: Undo returns to it. */
	#before: T | null = null;

	constructor(
		private read: () => T,
		private write: (v: T) => void,
		private convert: (cur: T, fill: unknown) => Filled<T> | null
	) {}

	/** A question is on its way: remember the page as it stands. */
	ask() {
		this.#asked = JSON.stringify(this.read());
		this.#before = null;
		this.asking = true;
	}

	/** The answer ended. */
	answered() {
		this.asking = false;
	}

	/** A fill arrived: applied, or held for a click when the page changed
	 *  since the question. false: not this page's fill. */
	offer(fill: unknown): boolean {
		const filled = this.convert(this.read(), fill);
		if (!filled) return false;
		if (this.#asked !== null && JSON.stringify(this.read()) !== this.#asked) {
			this.pending = { fill, changed: filled.changed };
			return true;
		}
		this.#apply(filled);
		return true;
	}

	applyNow(fill: unknown): boolean {
		const filled = this.convert(this.read(), fill);
		if (!filled) return false;
		this.#before = null;
		this.#apply(filled);
		return true;
	}

	// A second fill in one answer (the agent corrected the first) builds on
	// it: Undo goes back to before the first, the marker names both, and the
	// page as filled is what newer edits are measured against.
	#apply(filled: Filled<T>) {
		if (this.#before === null) {
			this.#before = this.read();
			this.changed = [];
		}
		this.undo = this.#before;
		this.write(filled.next);
		this.changed = [...new Set([...this.changed, ...filled.changed])];
		this.pending = null;
		this.#asked = JSON.stringify(this.read());
	}

	/** Apply the fill held for a click, to the page as it stands now: the
	 *  fields it does not set keep the newer edits. */
	applyPending() {
		if (!this.pending) return;
		const filled = this.convert(this.read(), this.pending.fill);
		if (filled) this.#apply(filled);
		this.pending = null;
	}

	/** Keep the page as it is: the held fill goes. */
	discardPending() {
		this.pending = null;
	}

	/** Put back what the last fill replaced. */
	revert() {
		if (this.undo === null) return;
		this.write(this.undo);
		this.undo = null;
		this.#before = null;
		this.changed = [];
	}

	/** The page was saved or reloaded: there is nothing to undo, and a fill
	 *  still on its way is not for what the page holds now. */
	settle() {
		this.undo = null;
		this.#before = null;
		this.changed = [];
		this.pending = null;
		this.asking = false;
	}
}
