// The assistant, as the shell holds it: one corner of the screen, two modes
// that never mix (docs/notes, the unified assistant). It floats over the
// page and never takes its room: closed it is the button (a pill while it
// has something to say), open a card as tall as what it shows, expanded a
// drawer with the whole conversation.
//
//   - page: about the page on screen. It fills the page (unsaved, with
//     Undo) and never acts. Only a page that offers itself has one.
//   - general: about everything. It reads all and acts once the person
//     confirms; it never touches the page.
//
// A page offers itself with offerPage (its kind, subject, guard and state);
// the panel's page mode is about the last page offered, and goes when the
// page does.

import { untrack, type Snippet } from 'svelte';
import type { PageGuard } from './fillGuard.svelte';

export type AssistantMode = 'page' | 'general';
export type PageKind = 'flow' | 'ticket' | 'skill' | 'dashboard' | 'run';

/** What a page gives the assistant to answer about it and fill it. */
export interface PageAssist {
	kind: PageKind;
	/** The flow's name, the ticket's key, the skill's id, or a draft id. */
	subject: string;
	/** The workshop it reads ('' for none). */
	workshop?: string;
	/** What the page shows, for the mode's tab: a ticket's key, a flow's name. */
	name?: string;
	/** The page's guard: what a fill goes through. */
	guard: PageGuard;
	/** The page as it stands, unsaved edits included, for each question. */
	pageState: () => unknown;
	/** Above the conversation: the skill page's workshop choice. */
	workshopPicker?: Snippet;
	/** What to do with a fill that belongs elsewhere (a run's flow). */
	handoff?: Snippet;
}

class AssistantState {
	open = $state(false);
	/** Open as the drawer with the whole conversation, not the card. */
	expanded = $state(false);
	/** The mode the person is in: page mode only shows while a page offers
	 *  itself (mode). */
	chosen = $state<AssistantMode>('general');
	/** A question carried from page mode to general: its composer takes it. */
	carried = $state('');
	#offers = $state<{ id: number; target: PageAssist }[]>([]);
	#next = 0;

	/** The page on screen that offers itself, if any. */
	get page(): PageAssist | null {
		return this.#offers.at(-1)?.target ?? null;
	}

	/** The mode shown: page mode needs a page. */
	get mode(): AssistantMode {
		return this.chosen === 'page' && this.page ? 'page' : 'general';
	}

	/** Open the card: in mode, or about the page when there is one. */
	show(mode?: AssistantMode) {
		this.chosen = mode ?? (this.page ? 'page' : 'general');
		this.expanded = false;
		this.open = true;
	}

	/** Back to the button: what was written and asked stays. */
	close() {
		this.open = false;
		this.expanded = false;
	}

	/** The keyboard's ⌘J: open, then switch modes; with one mode, close. */
	toggle() {
		if (!this.open) return this.show();
		if (!this.page) return this.close();
		this.chosen = this.mode === 'page' ? 'general' : 'page';
	}

	/** Ask in general mode what was asked about the page: the person sends it. */
	carry(question: string) {
		this.carried = question;
		this.chosen = 'general';
		this.open = true;
	}

	/** A page offers itself; the returned function withdraws it. */
	offer(target: PageAssist): () => void {
		const id = ++this.#next;
		this.#offers = [...this.#offers, { id, target }];
		return () => {
			this.#offers = this.#offers.filter((o) => o.id !== id);
		};
	}
}

export const assistant = new AssistantState();

/** Offer the page while the component lives (call it in a component's
 *  script): null offers nothing. Offered again whenever what get reads
 *  changes. */
export function offerPage(get: () => PageAssist | null): void {
	$effect(() => {
		const target = get();
		if (!target) return;
		// The offers it reads to add to are not what it follows.
		return untrack(() => assistant.offer(target));
	});
}

/** The conversation a page's mode holds: one per page, workshop and subject. */
export function pageKey(p: PageAssist): string {
	return [p.kind, p.subject, p.workshop ?? ''].join('\u0000');
}
