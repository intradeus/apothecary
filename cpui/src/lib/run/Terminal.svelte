<script lang="ts">
	// A shell in the run's workspace, in the repo, as the workspace user —
	// over a WebSocket to the workspace sidecar's PTY. The session lives as
	// long as this view: leaving the tab ends the shell.
	import { api } from '$lib/api';
	import { t } from '$lib/i18n/index.svelte';
	import '@xterm/xterm/css/xterm.css';
	import { token } from '$lib/design/tokens';
	import { I } from '$lib/design/icons';

	let { runId, repo = '', dir = '' }: { runId: string; repo?: string; dir?: string } = $props();

	let host = $state<HTMLDivElement | null>(null);
	let status = $state<'connecting' | 'open' | 'closed'>('connecting');
	let generation = $state(0); // bump to reconnect

	// One terminal + socket per (host, generation): reconnect bumps generation,
	// the effect's cleanup disposes the old pair and this builds a new one.
	$effect(() => {
		void generation;
		const el = host;
		if (!el) return;
		let disposed = false;
		let cleanup = () => {};
		const start = async () => {
			const [{ Terminal }, { FitAddon }] = await Promise.all([
				import('@xterm/xterm'),
				import('@xterm/addon-fit')
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
			el.replaceChildren();
			term.open(el);
			fit.fit();

			let ws: WebSocket | null = null;
			const enc = new TextEncoder();
			const connect = () => {
				status = 'connecting';
				ws = new WebSocket(api.workspace.terminalUrl(runId, term.cols, term.rows, repo));
				ws.binaryType = 'arraybuffer';
				ws.onopen = () => {
					status = 'open';
					term.focus();
				};
				ws.onmessage = (m) => {
					if (disposed) return;
					if (m.data instanceof ArrayBuffer) term.write(new Uint8Array(m.data));
					else term.write(String(m.data));
				};
				ws.onclose = () => {
					if (disposed) return; // this pair is gone, and maybe replaced
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
				ws?.close();
				term.dispose();
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
					: t('run.terminal.disconnected')}{#if dir}&nbsp;· <span class="mono">~/{dir}</span
				>{/if}</span
		>
		{#if status === 'closed'}
			<button class="btn-term btn-sm" onclick={() => generation++}
				>{I.retry} {t('run.terminal.reconnect')}</button
			>
		{/if}
		<span class="hint">{t('run.terminal.hint')}</span>
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
	.term {
		flex: 1;
		min-height: 0;
		padding: 2px 6px 8px 12px;
	}
</style>
