<script lang="ts">
	// One terminal session in the run's workspace, as the workspace user —
	// over a WebSocket to the workspace sidecar's PTY. The shell lives in the
	// sidecar, not in this view: with no `session` the workspace opens a new
	// one in the repo and the first frame names it (onsession); with one, this
	// view attaches to it and is sent its recent output again. Leaving only
	// detaches; the shell ends when it exits, is closed, or the workspace
	// stops (onended). A sidecar that predates sessions names none: the shell
	// then ends with this view, and the bar says so. Its URLs open on a
	// click; copy and paste follow terminalKeys, and its bar copies, pastes
	// and jumps to either end.
	import { api } from '$lib/api';
	import { t } from '$lib/i18n/index.svelte';
	import '@xterm/xterm/css/xterm.css';
	import type { Terminal as XTerm } from '@xterm/xterm';
	import { token } from '$lib/design/tokens';
	import { I } from '$lib/design/icons';
	import { toast } from '$lib/ui/toast.svelte';
	import { terminalKey, terminalShortcuts } from './terminalKeys';

	let {
		runId,
		repo = '',
		dir = '',
		session = '',
		legacy = false,
		onsession,
		onended
	}: {
		runId: string;
		repo?: string;
		dir?: string;
		/** The session to attach to; '' opens a new one. */
		session?: string;
		/** Known in advance to predate sessions (the list answered 501). */
		legacy?: boolean;
		/** The workspace named the session this view shows. */
		onsession?: (id: string) => void;
		/** The session's shell ended, or the session is gone. */
		onended?: () => void;
	} = $props();

	let host = $state<HTMLDivElement | null>(null);
	let status = $state<'connecting' | 'open' | 'closed' | 'ended'>('connecting');
	/** null until the workspace says: a session frame (true) or plain output (false). */
	let persistent = $state<boolean | null>(null);
	/** The session a create learned, so Reconnect attaches to it, not a new one. */
	let learned = '';
	let generation = $state(0); // bump to reconnect
	/** The terminal on screen, for the bar's buttons. */
	let current: XTerm | null = null;
	let selected = $state(false);

	const mac = /Mac|iPhone|iPad|iPod/.test(navigator.platform);
	const keys = terminalShortcuts(mac);

	/** The selection to the clipboard; it stays selected if the browser refuses. */
	async function copySelection(term: XTerm) {
		const text = term.getSelection();
		if (!text) return;
		try {
			await navigator.clipboard.writeText(text);
			term.clearSelection();
		} catch {
			toast(t('ui.copy.failed'), { tone: 'bad' });
		}
	}

	/** The bar's buttons act on the terminal, then hand the keyboard back to it. */
	async function fromBar(act: (term: XTerm) => unknown) {
		const term = current;
		if (!term) return;
		await act(term);
		term.focus();
	}

	// Through xterm's paste, which brackets it when the shell asked for that.
	async function paste(term: XTerm) {
		try {
			const text = await navigator.clipboard.readText();
			if (text) term.paste(text);
		} catch {
			toast(t('run.terminal.pasteRefused', { keys: keys.paste }), { tone: 'bad' });
		}
	}

	// One terminal + socket per (host, generation): reconnect bumps generation,
	// the effect's cleanup disposes the old pair and this builds a new one.
	$effect(() => {
		void generation;
		const el = host;
		if (!el) return;
		let disposed = false;
		let cleanup = () => {};
		const start = async () => {
			const [{ Terminal }, { FitAddon }, { WebLinksAddon }] = await Promise.all([
				import('@xterm/xterm'),
				import('@xterm/addon-fit'),
				import('@xterm/addon-web-links')
			]);
			if (disposed) return;
			const term = new Terminal({
				cursorBlink: true,
				fontFamily: token('--font-code'),
				fontSize: parseFloat(token('--fs-label')) || 12,
				lineHeight: 1.25,
				allowTransparency: true,
				// Palette from app.css (--term*): the terminal stays dark
				// in both themes, so it is read once per terminal.
				theme: {
					background: 'transparent',
					foreground: token('--term-text'),
					cursor: token('--term-cursor'),
					cursorAccent: token('--term-cursor-accent'),
					selectionBackground: token('--term-selection'),
					black: token('--term-black'),
					red: token('--term-red'),
					green: token('--term-green'),
					yellow: token('--term-yellow'),
					blue: token('--term-blue'),
					magenta: token('--term-magenta'),
					cyan: token('--term-cyan'),
					white: token('--term-text'),
					brightBlack: token('--term-faint')
				}
			});
			const fit = new FitAddon();
			term.loadAddon(fit);
			term.loadAddon(
				new WebLinksAddon((_e, uri) => window.open(uri, '_blank', 'noopener,noreferrer'))
			);
			// Copy writes the selection itself and swallows the key (Ctrl+Shift+C
			// is Chromium's inspector otherwise). Paste is left to the browser:
			// its paste event reaches xterm's textarea with no clipboard prompt.
			term.attachCustomKeyEventHandler((e) => {
				const act = terminalKey(e, term.hasSelection(), mac);
				if (act === 'pass') return true;
				if (act === 'copy' && e.type === 'keydown') {
					e.preventDefault();
					void copySelection(term);
				}
				return false;
			});
			const selection = term.onSelectionChange(() => (selected = term.hasSelection()));
			el.replaceChildren();
			term.open(el);
			fit.fit();
			current = term;

			let ws: WebSocket | null = null;
			const enc = new TextEncoder();
			const end = () => {
				status = 'ended';
				onended?.();
			};
			const connect = () => {
				status = 'connecting';
				const asked = learned || session;
				let opened = false;
				ws = new WebSocket(api.workspace.terminalUrl(runId, term.cols, term.rows, repo, asked));
				ws.binaryType = 'arraybuffer';
				ws.onopen = () => {
					opened = true;
					status = 'open';
					term.focus();
				};
				ws.onmessage = (m) => {
					if (disposed) return;
					if (typeof m.data === 'string' && m.data.startsWith('{"type":')) {
						const frame = JSON.parse(m.data) as { type: string; id?: string };
						if (frame.type === 'session' && frame.id) {
							// The replay follows: start it on a clean screen.
							term.reset();
							persistent = true;
							learned = frame.id;
							onsession?.(frame.id);
							return;
						}
						if (frame.type === 'exit') {
							term.write(`\r\n\x1b[90m[${t('run.terminal.sessionEnded')}]\x1b[0m\r\n`);
							end();
							return;
						}
					}
					persistent ??= false;
					if (m.data instanceof ArrayBuffer) term.write(new Uint8Array(m.data));
					else term.write(String(m.data));
				};
				ws.onclose = () => {
					if (disposed || status === 'ended') return; // this pair is gone, or its shell
					// Refused before the upgrade: the session asked for is gone.
					if (!opened && asked) {
						end();
						return;
					}
					status = 'closed';
					term.write(`\r\n\x1b[90m[${t('run.terminal.sessionClosed')}]\x1b[0m\r\n`);
				};
			};
			const input = term.onData((d) => {
				if (ws?.readyState === WebSocket.OPEN) ws.send(enc.encode(d));
			});
			const resize = term.onResize(({ cols, rows }) => {
				if (ws?.readyState === WebSocket.OPEN)
					ws.send(JSON.stringify({ type: 'resize', cols, rows }));
			});
			const ro = new ResizeObserver(() => fit.fit());
			ro.observe(el);
			connect();
			cleanup = () => {
				ro.disconnect();
				input.dispose();
				resize.dispose();
				selection.dispose();
				ws?.close();
				term.dispose();
				current = null;
				selected = false;
			};
		};
		start();
		return () => {
			disposed = true;
			cleanup();
		};
	});
</script>

<div class="panel">
	<div class="vbar bar">
		<span class="live-dot sm {status}" class:pulse={status === 'connecting'}></span>
		<span
			>{status === 'open'
				? t('run.terminal.connected')
				: status === 'connecting'
					? t('run.terminal.connecting')
					: status === 'ended'
						? t('run.terminal.sessionEnded')
						: t('run.terminal.disconnected')}{#if dir}&nbsp;· <span class="mono">~/{dir}</span
				>{/if}</span
		>
		{#if status === 'closed'}
			<button class="btn-term btn-sm" onclick={() => generation++}
				>{I.retry} {t('run.terminal.reconnect')}</button
			>
		{/if}
		{#if legacy || persistent === false}
			<span class="hint">{t('run.terminal.notPersistent')}</span>
		{:else if persistent}
			<span class="hint">{t('run.terminal.hint')}</span>
		{/if}
		<button
			class="btn-term btn-sm"
			disabled={!selected}
			title={t('run.terminal.copy', { keys: keys.copy })}
			aria-label={t('run.terminal.copy', { keys: keys.copy })}
			onclick={() => fromBar(copySelection)}>{I.copy}</button
		>
		<button
			class="btn-term btn-sm"
			title={t('run.terminal.paste', { keys: keys.paste })}
			aria-label={t('run.terminal.paste', { keys: keys.paste })}
			onclick={() => fromBar(paste)}>{I.paste}</button
		>
		<button
			class="btn-term btn-sm"
			title={t('run.terminal.top')}
			aria-label={t('run.terminal.top')}
			onclick={() => fromBar((term) => term.scrollToTop())}>{I.top}</button
		>
		<button
			class="btn-term btn-sm"
			title={t('run.terminal.bottom')}
			aria-label={t('run.terminal.bottom')}
			onclick={() => fromBar((term) => term.scrollToBottom())}>{I.bottom}</button
		>
	</div>
	<div class="term" bind:this={host}></div>
</div>

<style>
	.panel {
		flex: 1;
		margin: 8px;
		border-radius: var(--r-card);
		background: var(--term-bg);
		color: var(--term-text);
		display: flex;
		flex-direction: column;
		min-height: 0;
		border: 1px solid var(--term-line);
		overflow: hidden;
	}
	.bar {
		font-size: var(--fs-help);
		color: var(--term-muted);
		flex-wrap: nowrap;
	}
	.bar > .hint {
		color: var(--term-faint);
	}
	.mono {
		font-size: var(--fs-meta);
	}
	/* The terminal stays dark in every theme, so its dot uses the fixed
	 * dark palette rather than the themed hues. */
	.live-dot.open {
		color: var(--term-green);
	}
	.live-dot.connecting {
		color: var(--term-orange);
	}
	.live-dot.closed {
		color: var(--term-red);
		box-shadow: none;
	}
	.live-dot.ended {
		color: var(--term-faint);
		box-shadow: none;
	}
	.term {
		flex: 1;
		min-height: 0;
		padding: 2px 6px 8px 12px;
	}
</style>
