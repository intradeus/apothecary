// Completion and status feedback (DESIGN 4.17; Apple: "confirm meaningful
// actions"). A toast confirms something happened away from where the user
// is looking — an answer sent, a rule saved, a link copied — and offers the
// natural next step or an undo. Not for errors that need a fix: those stay
// inline next to their cause.
//
//   toast('Answer sent', { action: { label: 'Open run', run: () => goto(…) } });

export type ToastTone = 'ok' | 'info' | 'warn' | 'bad';

export interface Toast {
	id: number;
	text: string;
	tone: ToastTone;
	action?: { label: string; run: () => void };
	/** ms on screen; paused while hovered. */
	ms: number;
}

class Toasts {
	list = $state<Toast[]>([]);
	#id = 0;

	push(text: string, o: Partial<Omit<Toast, 'id' | 'text'>> = {}): number {
		const t: Toast = {
			id: ++this.#id,
			text,
			tone: o.tone ?? 'ok',
			action: o.action,
			ms: o.ms ?? 4000
		};
		// Keep it to a handful: the oldest makes room.
		this.list = [...this.list.slice(-3), t];
		return t.id;
	}

	dismiss(id: number) {
		this.list = this.list.filter((t) => t.id !== id);
	}
}

export const toasts = new Toasts();
export const toast = (text: string, o?: Partial<Omit<Toast, 'id' | 'text'>>) =>
	toasts.push(text, o);
