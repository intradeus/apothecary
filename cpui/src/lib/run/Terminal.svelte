<script lang="ts">
	// A shell in the run's workspace, in the repo, as the workspace user —
	// over a WebSocket to the workspace sidecar's PTY. The session lives as
	// long as this view: leaving the tab ends the shell.
	import { api } from '$lib/api';
	import '@xterm/xterm/css/xterm.css';

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
				fontFamily: "'Geist Mono', ui-monospace, 'JetBrains Mono', monospace",
				fontSize: 12,
				lineHeight: 1.25,
				allowTransparency: true,
				theme: {
					background: '#00000000',
					foreground: '#e6efe8',
					cursor: '#efe6cf',
					cursorAccent: '#020a06',
					selectionBackground: 'rgba(239,230,207,.3)',
					black: '#0b1a12',
					red: '#ff7a70',
					green: '#7ee2a8',
					yellow: '#f5b54a',
					blue: '#7fb2ff',
					magenta: '#c3a2ff',
					cyan: '#5fd6cc',
					white: '#e6efe8',
					brightBlack: '#5f7c69'
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
					if (m.data instanceof ArrayBuffer) term.write(new Uint8Array(m.data));
					else term.write(String(m.data));
				};
				ws.onclose = () => {
					status = 'closed';
					term.write('\r\n\x1b[90m[session closed]\x1b[0m\r\n');
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
	<div class="bar">
		<span class="dot {status}"></span>
		<span
			>{status === 'open'
				? 'connected'
				: status === 'connecting'
					? 'connecting…'
					: 'disconnected'}{#if dir}&nbsp;· <span class="mono">~/{dir}</span>{/if}</span
		>
		{#if status === 'closed'}
			<button class="btn-sm" onclick={() => generation++}>Reconnect</button>
		{/if}
		<span class="end">ends when you leave this tab</span>
	</div>
	<div class="term" bind:this={host}></div>
</div>

<style>
	.panel {
		flex: 1;
		margin: 8px;
		border-radius: 14px;
		background: rgba(2, 10, 6, 0.78);
		color: #e6efe8;
		display: flex;
		flex-direction: column;
		min-height: 0;
		border: 1px solid rgba(255, 255, 255, 0.06);
		overflow: hidden;
	}
	.bar {
		flex: none;
		padding: 8px 12px;
		font-size: 11.5px;
		display: flex;
		gap: 8px;
		align-items: center;
		color: #93ad9c;
	}
	.mono {
		font-size: 11px;
	}
	.dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: #ff7a70;
		flex: none;
	}
	.dot.open {
		background: #7ee2a8;
		box-shadow: 0 0 8px #7ee2a8;
	}
	.dot.connecting {
		background: #7fb2ff;
		animation: ap-pulse 1.4s ease-in-out infinite;
	}
	.btn-sm {
		color: #e6efe8;
		background: rgba(170, 255, 205, 0.08);
		border-color: rgba(210, 255, 225, 0.18);
	}
	.end {
		margin-left: auto;
		color: #5f7c69;
		white-space: nowrap;
	}
	.term {
		flex: 1;
		min-height: 0;
		padding: 2px 6px 8px 12px;
	}
	.term :global(.xterm-viewport) {
		background: transparent !important;
	}
</style>
