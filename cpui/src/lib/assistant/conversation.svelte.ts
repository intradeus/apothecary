// One conversation with the person's assistant (docs/assistant-plan.md):
// its status, its session, its messages, the answer as it is written, the
// requests it asks the person to confirm, and the person's side — ask,
// stop, confirm or decline, pick a model, refetch the repositories. The
// global drawer and a page's fill modal each hold one; it lives outside
// what shows it, so closing the drawer loses neither the stream nor the
// session.

import { api } from '$lib/api';
import { errorMessage } from '$lib/core/errors';
import type {
	AssistantFrame,
	AssistantKind,
	AssistantMessage,
	AssistantRequest,
	AssistantSession,
	AssistantStatus
} from '$lib/domain';

/** What a conversation needs of a page to fill it: a page's guard is one. */
export interface FillHook {
	/** A question of this page's is being answered: a fill is its. */
	readonly asking: boolean;
	ask(): void;
	/** The answer ended: a fill now is not this page's. */
	answered(): void;
	/** A fill arrived for the page. false: not this page's fill. */
	offer(fill: unknown): boolean;
}

/** How often typing may start the pod again: once a minute is plenty, the
 *  pod stays up for ten idle ones. */
export const WARM_EVERY_MS = 60_000;

/** When typing last asked for the pod: one clock for every conversation,
 *  the drawer's and the modal's. */
let warmedAt = -Infinity;

/** The person is typing a question: start their pod ahead of it, at most
 *  once a minute, never while the assistant is off. */
export function typing(text: string, status: AssistantStatus | null): void {
	if (!text.trim() || !status?.enabled) return;
	const now = Date.now();
	if (now - warmedAt < WARM_EVERY_MS) return;
	warmedAt = now;
	api.assistant.warm().catch(() => {
		// A warm is a head start, never needed: the question starts the pod.
	});
}

export class Conversation {
	readonly kind: AssistantKind;
	readonly subject: string;
	/** The workshop it reads ('' for none). */
	workshop = $state('');

	status = $state<AssistantStatus | null>(null);
	session = $state<AssistantSession | null>(null);
	messages = $state<AssistantMessage[]>([]);
	busy = $state(false);
	/** The answer as it is written, and the tool it is using. */
	live = $state('');
	tool = $state('');
	/** The engine's line on what it is doing (starting the pod…). */
	note = $state('');
	error = $state('');
	loadError = $state('');
	fetching = $state(false);
	/** A request proposed while the answer is written: shown, decided once
	 *  the answer is in. */
	liveRequest = $state<AssistantRequest | null>(null);
	/** The message whose request is being decided. */
	deciding = $state<number | null>(null);
	/** A session is being opened (the first, a fresh one, another
	 *  workshop's). */
	#opening = $state(false);
	/** The session held is being read again after a gap. */
	#rereading = $state(false);
	/** A session is being opened or read again: nothing is asked or
	 *  decided until the one shown is what the record says. */
	get switching(): boolean {
		return this.#opening || this.#rereading;
	}

	#pageState: () => unknown;
	#fill: FillHook | undefined;
	#stop: (() => void) | null = null;
	/** Bumped by every load: an older one's answer is dropped. */
	#loads = 0;
	/** Bumped by every reread and load: an older reread's answer is dropped. */
	#rereads = 0;
	/** The session's frames that came while it was read again: played over
	 *  the record once it is in, so an older record never undoes them. */
	#held: AssistantFrame[] = [];
	/** The session's calls on their way (a question, a decision, a model):
	 *  a reread waits them out, so the record it reads already holds them. */
	#inFlight = new Set<Promise<unknown>>();
	/** The last message held when the page's question was asked: an answer
	 *  to it comes after. Moved past a fill recovered from the record. */
	#after = 0;
	/** The last fill offered for the page's question, as JSON. */
	#offered: string | null = null;

	constructor(opts: {
		kind: AssistantKind;
		subject: string;
		workshop?: string;
		/** The page as it stands, sent with each question. */
		pageState?: () => unknown;
		/** The page a fill goes to; none for a conversation that fills nothing. */
		fill?: FillHook;
	}) {
		this.kind = opts.kind;
		this.subject = opts.subject;
		this.workshop = opts.workshop ?? '';
		this.#pageState = opts.pageState ?? (() => undefined);
		this.#fill = opts.fill;
	}

	/** The page's presets and whether it reads repositories. */
	get page() {
		return this.status?.pages[this.kind];
	}

	/** The status, then the session (a new one when fresh). Until it is in,
	 *  the conversation is switching: the session shown so far takes no
	 *  question, so none goes to a session about to be replaced. */
	async load(opts: { fresh?: boolean } = {}): Promise<void> {
		const n = ++this.#loads;
		// A reread of the session shown so far is not this load's.
		this.#rereads++;
		this.#release();
		this.loadError = '';
		this.error = '';
		this.#opening = true;
		try {
			const status = await api.assistant.status(this.workshop || undefined);
			if (n !== this.#loads) return;
			this.status = status;
			if (!status.enabled) return;
			const open = await api.assistant.open(this.kind, this.subject, this.workshop || undefined, {
				fresh: opts.fresh
			});
			if (n !== this.#loads) return;
			this.session = open.session;
			this.messages = open.messages;
			this.busy = open.busy;
			this.liveRequest = null;
			this.#idle();
		} catch (e) {
			if (n === this.#loads) this.loadError = errorMessage(e);
		} finally {
			if (n === this.#loads) this.#opening = false;
		}
	}

	/** Another workshop (the person moved on): its own conversation. */
	async retarget(workshop: string): Promise<void> {
		if (workshop === this.workshop) return;
		this.workshop = workshop;
		this.#reset();
		await this.load();
	}

	/** Follow the person's stream; stop with disconnect. */
	connect(): void {
		this.#stop ??= api.assistant.stream((f) => this.onFrame(f));
	}

	disconnect(): void {
		this.#stop?.();
		this.#stop = null;
	}

	onFrame(f: AssistantFrame): void {
		if (f.kind === 'gap') {
			// Frames were lost (or the engine restarted): the record says
			// where the session is.
			if (this.session) void this.reopen();
			return;
		}
		if (f.kind === 'repos') {
			if (f.workshop !== this.workshop || !this.status) return;
			this.fetching = !f.repos;
			if (f.repos) this.status = { ...this.status, repos: f.repos };
			return;
		}
		if (!this.session || f.sessionId !== this.session.id) return;
		if (this.#rereading) {
			this.#held.push(f);
			return;
		}
		switch (f.kind) {
			case 'message':
				if (!this.messages.some((m) => m.seq === f.message.seq))
					this.messages = [...this.messages, f.message];
				if (f.message.role === 'assistant') {
					this.live = '';
					this.tool = '';
					this.liveRequest = null;
				}
				break;
			case 'request':
				if (f.message.seq > 0) this.#replace(f.message);
				else this.liveRequest = f.message.request ?? null;
				break;
			case 'delta':
				this.live = f.text;
				this.tool = f.tool ?? '';
				break;
			case 'state':
				this.busy = f.busy;
				if (!f.busy) {
					this.note = '';
					this.#fill?.answered();
				}
				break;
			case 'status':
				this.note = f.text;
				break;
			case 'fill':
				// Only an answer to this page's question fills it: not one
				// replayed, nor one asked from another tab.
				if (this.#fill?.asking) {
					this.#offered = JSON.stringify(f.fill);
					this.#fill.offer(f.fill);
				}
				break;
		}
	}

	/** The session held, read again: never another about its subject (a
	 *  fresh one, or one another tab opened since). Until it is in, nothing
	 *  is asked or decided and the session's frames are held, then played
	 *  over it: a record read before them never replaces what they said. */
	async reopen(): Promise<void> {
		const s = this.session;
		if (!s) return;
		const n = ++this.#rereads;
		// What came before this gap is in the record read after it; what an
		// earlier gap's reread held may have lost its sequel.
		this.#held = [];
		this.#rereading = true;
		try {
			while (this.#inFlight.size) await Promise.allSettled(this.#inFlight);
			if (n !== this.#rereads) return;
			const open = await api.assistant.session(s.id);
			// A later gap's reread, or another session: this answer is not its.
			if (n !== this.#rereads) return;
			this.session = open.session;
			this.messages = open.messages;
			this.busy = open.busy;
			this.liveRequest = null;
			this.#recover();
			this.#idle();
		} catch (e) {
			if (n !== this.#rereads) return;
			this.error = errorMessage(e);
		}
		this.#release();
	}

	/** Ask, with the page as it stands. false: nothing was asked. */
	async ask(text: string): Promise<boolean> {
		const q = text.trim();
		if (!q || !this.session || this.busy || this.switching) return false;
		const id = this.session.id;
		this.error = '';
		this.#after = this.messages.reduce((n, m) => Math.max(n, m.seq), 0);
		this.#offered = null;
		this.#fill?.ask();
		this.busy = true;
		try {
			const m = await this.#track(api.assistant.send(id, q, this.#pageState()));
			if (this.session?.id === id && !this.messages.some((x) => x.seq === m.seq))
				this.messages = [...this.messages, m];
			return true;
		} catch (e) {
			if (this.session?.id !== id) return false;
			this.busy = false;
			this.#fill?.answered();
			this.error = errorMessage(e);
			return false;
		}
	}

	/** The person is typing: see typing. */
	typing(text: string): void {
		typing(text, this.status);
	}

	async cancel(): Promise<void> {
		if (!this.session) return;
		try {
			await api.assistant.cancel(this.session.id);
		} catch (e) {
			this.error = errorMessage(e);
		}
	}

	/** The person's answer to a request: confirmed, its acts run as them. */
	async decide(m: AssistantMessage, confirm: boolean): Promise<void> {
		if (!this.session || this.deciding !== null || this.switching) return;
		const id = this.session.id;
		this.deciding = m.seq;
		this.error = '';
		try {
			const out = await this.#track(
				confirm ? api.assistant.confirm(id, m.seq) : api.assistant.decline(id, m.seq)
			);
			if (this.session?.id === id) this.#replace(out);
		} catch (e) {
			if (this.session?.id === id) this.error = errorMessage(e);
		} finally {
			this.deciding = null;
		}
	}

	async pickModel(model: string): Promise<void> {
		if (!this.session || this.switching) return;
		const id = this.session.id;
		try {
			const s = await this.#track(api.assistant.setModel(id, model));
			if (this.session?.id === id) this.session = s;
		} catch (e) {
			if (this.session?.id === id) this.error = errorMessage(e);
		}
	}

	/** Fetch the workshop's repositories into the pod now. */
	async refresh(): Promise<void> {
		if (!this.workshop || !this.status) return;
		this.fetching = true;
		this.error = '';
		try {
			this.status = { ...this.status, repos: await api.assistant.refresh(this.workshop) };
		} catch (e) {
			this.error = errorMessage(e);
		} finally {
			this.fetching = false;
		}
	}

	/** The engine sends a fill's frame before it records the answer holding
	 *  it, so a gap can lose the frame of an answer the record already has:
	 *  the page's question still waiting takes the answer's fill from the
	 *  record, once — not when a frame already brought it, nor again. */
	#recover() {
		if (!this.#fill?.asking) return;
		const m = this.messages.findLast(
			(x) => x.role === 'assistant' && x.seq > this.#after && x.fill !== undefined
		);
		if (!m) return;
		this.#after = m.seq;
		const fill = JSON.stringify(m.fill);
		if (fill === this.#offered) return;
		this.#offered = fill;
		this.#fill.offer(m.fill);
	}

	/** Not answering: nothing is being written, no fill is this page's. */
	#idle() {
		if (this.busy) return;
		this.live = '';
		this.tool = '';
		this.note = '';
		this.#fill?.answered();
	}

	/** No longer reading the session again: what it held is played over
	 *  what is shown. */
	#release() {
		const held = this.#held;
		this.#held = [];
		this.#rereading = false;
		for (const f of held) this.onFrame(f);
	}

	/** p, waited out by a reread while it is on its way. */
	#track<T>(p: Promise<T>): Promise<T> {
		this.#inFlight.add(p);
		const done = () => void this.#inFlight.delete(p);
		p.then(done, done);
		return p;
	}

	#reset() {
		this.session = null;
		this.messages = [];
		this.busy = false;
		this.liveRequest = null;
		this.#idle();
	}

	#replace(m: AssistantMessage) {
		this.messages = this.messages.map((x) => (x.seq === m.seq ? m : x));
	}
}
