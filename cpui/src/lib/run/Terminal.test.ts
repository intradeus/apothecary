import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { toasts } from '$lib/ui/toast.svelte';
import Terminal from './Terminal.svelte';

vi.mock('$lib/api', () => ({ api: { workspace: { terminalUrl: vi.fn() } } }));

// xterm draws on a canvas jsdom has not got; the socket is the boundary.
const xterm = vi.hoisted(() => {
	class FakeTerminal {
		static made: FakeTerminal[] = [];
		cols = 80;
		rows = 24;
		options: unknown;
		written: (string | Uint8Array)[] = [];
		mountedIn: HTMLElement | null = null;
		focused = 0;
		disposed = false;
		dataListener: ((d: string) => void) | null = null;
		resizeListener: ((r: { cols: number; rows: number }) => void) | null = null;
		selectionListener: (() => void) | null = null;
		keyHandler: ((e: KeyboardEvent) => boolean) | null = null;
		inputDisposed = false;
		resizeDisposed = false;
		selectionDisposed = false;
		addons: object[] = [];
		selection = '';
		pasted: string[] = [];
		scrolled: string[] = [];
		constructor(options: unknown) {
			this.options = options;
			FakeTerminal.made.push(this);
		}
		get addon() {
			return this.addons.find((a): a is FakeFit => a instanceof FakeFit) ?? null;
		}
		get links() {
			return this.addons.find((a): a is FakeLinks => a instanceof FakeLinks) ?? null;
		}
		loadAddon(addon: object) {
			this.addons.push(addon);
		}
		open(el: HTMLElement) {
			this.mountedIn = el;
		}
		focus() {
			this.focused++;
		}
		write(data: string | Uint8Array) {
			this.written.push(data);
		}
		onData(fn: (d: string) => void) {
			this.dataListener = fn;
			return { dispose: () => (this.inputDisposed = true) };
		}
		onResize(fn: (r: { cols: number; rows: number }) => void) {
			this.resizeListener = fn;
			return { dispose: () => (this.resizeDisposed = true) };
		}
		onSelectionChange(fn: () => void) {
			this.selectionListener = fn;
			return { dispose: () => (this.selectionDisposed = true) };
		}
		attachCustomKeyEventHandler(fn: (e: KeyboardEvent) => boolean) {
			this.keyHandler = fn;
		}
		/** The reader selects (or clears) text in the terminal. */
		select(text: string) {
			this.selection = text;
			this.selectionListener?.();
		}
		hasSelection() {
			return this.selection !== '';
		}
		getSelection() {
			return this.selection;
		}
		clearSelection() {
			this.select('');
		}
		paste(data: string) {
			this.pasted.push(data);
		}
		scrollToTop() {
			this.scrolled.push('top');
		}
		scrollToBottom() {
			this.scrolled.push('bottom');
		}
		dispose() {
			this.disposed = true;
		}
	}
	class FakeFit {
		fits = 0;
		fit() {
			this.fits++;
		}
	}
	class FakeLinks {
		constructor(public open: (e: MouseEvent, uri: string) => void) {}
	}
	return { FakeTerminal, FakeFit, FakeLinks };
});
vi.mock('@xterm/xterm', () => ({ Terminal: xterm.FakeTerminal }));
vi.mock('@xterm/addon-fit', () => ({ FitAddon: xterm.FakeFit }));
vi.mock('@xterm/addon-web-links', () => ({ WebLinksAddon: xterm.FakeLinks }));

class FakeSocket {
	static CONNECTING = 0;
	static OPEN = 1;
	static CLOSED = 3;
	static made: FakeSocket[] = [];
	readyState = FakeSocket.CONNECTING;
	binaryType = '';
	sent: unknown[] = [];
	onopen: (() => void) | null = null;
	onclose: (() => void) | null = null;
	onmessage: ((m: { data: unknown }) => void) | null = null;
	constructor(public url: string) {
		FakeSocket.made.push(this);
	}
	send(data: unknown) {
		this.sent.push(data);
	}
	close() {
		this.readyState = FakeSocket.CLOSED;
	}
	/** The server's side. */
	opened() {
		this.readyState = FakeSocket.OPEN;
		this.onopen?.();
	}
	closed() {
		this.readyState = FakeSocket.CLOSED;
		this.onclose?.();
	}
}
vi.stubGlobal('WebSocket', FakeSocket);

class FakeObserver {
	static made: FakeObserver[] = [];
	disconnected = false;
	observed: Element[] = [];
	constructor(public fire: () => void) {
		FakeObserver.made.push(this);
	}
	observe(el: Element) {
		this.observed.push(el);
	}
	unobserve() {}
	disconnect() {
		this.disconnected = true;
	}
}
vi.stubGlobal('ResizeObserver', FakeObserver);

const lastTerm = () => xterm.FakeTerminal.made.at(-1)!;
const lastSocket = () => FakeSocket.made.at(-1)!;

/** Rendered, with the terminal built and its socket dialling. */
async function connect(props: { runId?: string; repo?: string; dir?: string } = {}) {
	const view = render(Terminal, { runId: 'r1', ...props });
	await vi.waitFor(() => expect(FakeSocket.made).not.toHaveLength(0));
	return view;
}
/** Socket events set state outside a user event: let the view catch up. */
const reads = (word: string) =>
	vi.waitFor(() => expect(screen.getByText(word)).toBeInTheDocument());
/** A key as xterm hands it to the terminal's own key handler. */
const key = (type: 'keydown' | 'keyup', k: string, mods: KeyboardEventInit = {}) =>
	new KeyboardEvent(type, {
		key: k,
		code: `Key${k.toUpperCase()}`,
		cancelable: true,
		...mods
	});
const btn = (name: RegExp) => screen.getByRole('button', { name });

describe('Terminal', () => {
	beforeEach(() => {
		xterm.FakeTerminal.made = [];
		FakeSocket.made = [];
		FakeObserver.made = [];
		toasts.list = [];
		vi.resetAllMocks();
		vi.restoreAllMocks();
		vi.mocked(api.workspace.terminalUrl).mockReturnValue('ws://engine/term');
	});

	it('says it is connecting until the socket opens', async () => {
		await connect();
		expect(screen.getByText('connecting…')).toBeInTheDocument();
	});

	it('tells you it ends with the tab', () => {
		render(Terminal, { runId: 'r1' });
		expect(screen.getByText('ends when you leave this tab')).toBeInTheDocument();
	});

	it('shows where it opens, when told', async () => {
		await connect({ dir: 'repo/api' });
		expect(screen.getByText('~/repo/api')).toBeInTheDocument();
	});

	it('leaves the place out otherwise', async () => {
		await connect();
		expect(screen.queryByText(/^~\//)).not.toBeInTheDocument();
	});

	it('opens a terminal in its pane, fitted, and dials at its size', async () => {
		const { container } = await connect({ runId: 'r1', repo: 'docs' });

		expect(lastTerm().mountedIn).toBe(container.querySelector('.term'));
		expect(lastTerm().addon?.fits).toBe(1);
		expect(api.workspace.terminalUrl).toHaveBeenCalledExactlyOnceWith('r1', 80, 24, 'docs');
		expect(lastSocket().url).toBe('ws://engine/term');
		expect(lastSocket().binaryType).toBe('arraybuffer');
	});

	it('goes straight to the shell once connected', async () => {
		await connect();

		lastSocket().opened();

		await reads('connected');
		expect(lastTerm().focused).toBe(1);
	});

	describe('output', () => {
		it('writes binary frames as bytes', async () => {
			await connect();

			lastSocket().onmessage?.({ data: new Uint8Array([104, 105]).buffer });

			expect(lastTerm().written).toEqual([new Uint8Array([104, 105])]);
		});

		it('writes text frames as text', async () => {
			await connect();

			lastSocket().onmessage?.({ data: 'hello' });

			expect(lastTerm().written).toEqual(['hello']);
		});
	});

	describe('input', () => {
		it('sends keystrokes as bytes', async () => {
			await connect();
			lastSocket().opened();

			lastTerm().dataListener?.('ls\r');

			expect(lastSocket().sent).toEqual([new TextEncoder().encode('ls\r')]);
		});

		it('sends nothing before the socket is open', async () => {
			await connect();

			lastTerm().dataListener?.('ls\r');
			lastTerm().resizeListener?.({ cols: 100, rows: 30 });

			expect(lastSocket().sent).toEqual([]);
		});

		it('sends nothing after it closed', async () => {
			await connect();
			lastSocket().opened();
			lastSocket().closed();

			lastTerm().dataListener?.('ls\r');

			expect(lastSocket().sent).toEqual([]);
		});
	});

	describe('resizing', () => {
		it('tells the shell its new size', async () => {
			await connect();
			lastSocket().opened();

			lastTerm().resizeListener?.({ cols: 100, rows: 30 });

			expect(lastSocket().sent).toEqual([JSON.stringify({ type: 'resize', cols: 100, rows: 30 })]);
		});

		it('refits when the pane changes size', async () => {
			const { container } = await connect();
			const observer = FakeObserver.made.find((o) =>
				o.observed.includes(container.querySelector('.term')!)
			)!;

			observer.fire();

			expect(lastTerm().addon?.fits).toBe(2);
		});
	});

	describe('when the session closes', () => {
		it('says so in the bar and in the terminal', async () => {
			await connect();
			lastSocket().opened();

			lastSocket().closed();

			await reads('disconnected');
			expect(lastTerm().written.at(-1)).toContain('session closed');
		});

		it('offers Reconnect, and only then', async () => {
			await connect();
			lastSocket().opened();
			await reads('connected');
			expect(screen.queryByRole('button', { name: /Reconnect/ })).not.toBeInTheDocument();

			lastSocket().closed();

			expect(await screen.findByRole('button', { name: /Reconnect/ })).toBeInTheDocument();
		});

		it('builds a new terminal and socket on Reconnect', async () => {
			await connect();
			const first = lastTerm();
			lastSocket().closed();

			await userEvent.click(await screen.findByRole('button', { name: /Reconnect/ }));

			await vi.waitFor(() => expect(FakeSocket.made).toHaveLength(2));
			expect(first.disposed).toBe(true);
			expect(xterm.FakeTerminal.made).toHaveLength(2);
			await reads('connecting…');
			expect(screen.queryByRole('button', { name: /Reconnect/ })).not.toBeInTheDocument();

			lastSocket().opened();
			await reads('connected');
		});
	});

	describe('links', () => {
		it('opens a link in a new tab, with no way back to this one', async () => {
			await connect();
			const open = vi.spyOn(window, 'open').mockReturnValue(null);

			lastTerm().links!.open(new MouseEvent('click'), 'https://a.test/x');

			expect(open).toHaveBeenCalledExactlyOnceWith(
				'https://a.test/x',
				'_blank',
				'noopener,noreferrer'
			);
		});
	});

	describe('copy and paste', () => {
		it('copies the selection on Ctrl+Shift+C, and sends the shell nothing', async () => {
			userEvent.setup();
			const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
			await connect();
			lastSocket().opened();
			lastTerm().select('ls -la');

			const e = key('keydown', 'C', { ctrlKey: true, shiftKey: true });
			expect(lastTerm().keyHandler!(e)).toBe(false);

			// Chromium would open its inspector on Ctrl+Shift+C otherwise.
			expect(e.defaultPrevented).toBe(true);
			expect(write).toHaveBeenCalledExactlyOnceWith('ls -la');
			await vi.waitFor(() => expect(lastTerm().selection).toBe(''));
			expect(lastSocket().sent).toEqual([]);
		});

		it('copies on Ctrl+C when text is selected', async () => {
			userEvent.setup();
			const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
			await connect();
			lastTerm().select('pwd');

			expect(lastTerm().keyHandler!(key('keydown', 'c', { ctrlKey: true }))).toBe(false);
			expect(write).toHaveBeenCalledExactlyOnceWith('pwd');
		});

		it('hands Ctrl+C to the shell when nothing is selected', async () => {
			userEvent.setup();
			const write = vi.spyOn(navigator.clipboard, 'writeText');
			await connect();

			const e = key('keydown', 'c', { ctrlKey: true });
			expect(lastTerm().keyHandler!(e)).toBe(true);
			expect(e.defaultPrevented).toBe(false);
			expect(write).not.toHaveBeenCalled();
		});

		it('copies nothing on Ctrl+Shift+C with nothing selected', async () => {
			userEvent.setup();
			const write = vi.spyOn(navigator.clipboard, 'writeText');
			await connect();

			expect(lastTerm().keyHandler!(key('keydown', 'C', { ctrlKey: true, shiftKey: true }))).toBe(
				false
			);
			expect(write).not.toHaveBeenCalled();
		});

		it('acts on the keydown only', async () => {
			userEvent.setup();
			const write = vi.spyOn(navigator.clipboard, 'writeText');
			await connect();
			lastTerm().select('pwd');

			const e = key('keyup', 'C', { ctrlKey: true, shiftKey: true });
			expect(lastTerm().keyHandler!(e)).toBe(false);
			expect(e.defaultPrevented).toBe(false);
			expect(write).not.toHaveBeenCalled();
		});

		it.each([
			['Ctrl+V', { ctrlKey: true }],
			['Ctrl+Shift+V', { ctrlKey: true, shiftKey: true }]
		])('leaves %s to the browser’s own paste, which xterm takes', async (_, mods) => {
			await connect();

			const e = key('keydown', 'v', mods);
			expect(lastTerm().keyHandler!(e)).toBe(false);
			expect(e.defaultPrevented).toBe(false);
		});

		it('hands every other key to xterm', async () => {
			await connect();
			expect(lastTerm().keyHandler!(key('keydown', 'd', { ctrlKey: true }))).toBe(true);
		});

		it('offers Copy only while text is selected', async () => {
			await connect();
			expect(btn(/^Copy the selection/)).toBeDisabled();

			lastTerm().select('pwd');

			await vi.waitFor(() => expect(btn(/^Copy the selection/)).toBeEnabled());
			expect(btn(/^Copy the selection/)).toHaveAccessibleName('Copy the selection (Ctrl+Shift+C)');
		});

		it('copies from its button, and goes back to the shell', async () => {
			const user = userEvent.setup();
			const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
			await connect();
			lastTerm().select('pwd');
			await vi.waitFor(() => expect(btn(/^Copy the selection/)).toBeEnabled());

			await user.click(btn(/^Copy the selection/));

			expect(write).toHaveBeenCalledExactlyOnceWith('pwd');
			await vi.waitFor(() => expect(lastTerm().focused).toBe(1));
		});

		it('says in a toast when the browser refuses the clipboard, and keeps the selection', async () => {
			userEvent.setup();
			vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'));
			await connect();
			lastTerm().select('pwd');

			lastTerm().keyHandler!(key('keydown', 'C', { ctrlKey: true, shiftKey: true }));

			await vi.waitFor(() =>
				expect(toasts.list.map((x) => [x.text, x.tone])).toEqual([
					['Could not copy — the browser refused clipboard access.', 'bad']
				])
			);
			expect(lastTerm().selection).toBe('pwd');
		});

		it('pastes from its button through xterm, and goes back to the shell', async () => {
			const user = userEvent.setup();
			vi.spyOn(navigator.clipboard, 'readText').mockResolvedValue('echo hi');
			await connect();

			await user.click(btn(/^Paste/));

			expect(lastTerm().pasted).toEqual(['echo hi']);
			expect(lastTerm().focused).toBe(1);
			expect(btn(/^Paste/)).toHaveAccessibleName('Paste (Ctrl+Shift+V)');
		});

		it('pastes nothing from an empty clipboard', async () => {
			const user = userEvent.setup();
			vi.spyOn(navigator.clipboard, 'readText').mockResolvedValue('');
			await connect();

			await user.click(btn(/^Paste/));

			expect(lastTerm().pasted).toEqual([]);
		});

		it('points at the shortcut when the browser will not hand the clipboard over', async () => {
			const user = userEvent.setup();
			vi.spyOn(navigator.clipboard, 'readText').mockRejectedValue(new Error('denied'));
			await connect();

			await user.click(btn(/^Paste/));

			expect(toasts.list.map((x) => [x.text, x.tone])).toEqual([
				['The browser would not hand over the clipboard — paste with Ctrl+Shift+V.', 'bad']
			]);
			expect(lastTerm().pasted).toEqual([]);
		});
	});

	describe('scrolling', () => {
		it.each([
			['Scroll to the top', 'top'],
			['Scroll to the bottom', 'bottom']
		])('%s, and goes back to the shell', async (name, where) => {
			const user = userEvent.setup();
			await connect();

			await user.click(screen.getByRole('button', { name }));

			expect(lastTerm().scrolled).toEqual([where]);
			expect(lastTerm().focused).toBe(1);
		});
	});

	describe('leaving', () => {
		it('ends the session and cleans up after itself', async () => {
			const { unmount, container } = await connect();
			const term = lastTerm();
			const socket = lastSocket();
			const pane = container.querySelector('.term')!;
			const observer = FakeObserver.made.find((o) => o.observed.includes(pane))!;
			socket.opened();

			unmount();

			expect(socket.readyState).toBe(FakeSocket.CLOSED);
			expect(term.disposed).toBe(true);
			expect(term.inputDisposed).toBe(true);
			expect(term.resizeDisposed).toBe(true);
			expect(term.selectionDisposed).toBe(true);
			expect(observer.disconnected).toBe(true);
		});

		it('builds nothing if it left before xterm loaded', async () => {
			const { unmount } = render(Terminal, { runId: 'r1' });
			unmount();

			await vi.dynamicImportSettled();

			expect(xterm.FakeTerminal.made).toEqual([]);
			expect(FakeSocket.made).toEqual([]);
		});

		// The socket's close event lands after term.dispose().
		it('writes nothing to the terminal it has disposed', async () => {
			const { unmount } = await connect();
			const term = lastTerm();
			const socket = lastSocket();
			socket.opened();

			unmount();
			const before = term.written.length;
			socket.closed();

			expect(term.written).toHaveLength(before);
		});

		it('writes no late output to it either', async () => {
			const { unmount } = await connect();
			const term = lastTerm();
			const socket = lastSocket();
			socket.opened();

			unmount();
			const before = term.written.length;
			socket.onmessage?.({ data: 'late' });

			expect(term.written).toHaveLength(before);
		});
	});
});
