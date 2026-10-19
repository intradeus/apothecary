// Promise-based confirm dialog (DESIGN 4.9). Never use native confirm().
//
//   if (!(await confirmDialog({
//     title: 'Stop this run?',
//     body: 'The current turn is cancelled and the run becomes <b>abandoned</b>. …',
//     action: 'Stop run'
//   }))) return;
//
// The body says exactly what happens to the RUN vs the WORKSPACE. It is
// trusted markup written in our own components (bold the object names) —
// never pass user or engine text through it unescaped.

export interface ConfirmOptions {
	title: string;
	/** Trusted HTML (our own copy only). */
	body: string;
	/** The destructive button's label. */
	action: string;
	tone?: 'danger' | 'warn';
	cancel?: string;
}

interface Pending extends ConfirmOptions {
	resolve: (ok: boolean) => void;
}

class ConfirmState {
	current = $state<Pending | null>(null);

	ask(o: ConfirmOptions): Promise<boolean> {
		this.current?.resolve(false);
		return new Promise((resolve) => (this.current = { ...o, resolve }));
	}

	settle(ok: boolean) {
		this.current?.resolve(ok);
		this.current = null;
	}
}

export const confirmState = new ConfirmState();
export const confirmDialog = (o: ConfirmOptions) => confirmState.ask(o);
