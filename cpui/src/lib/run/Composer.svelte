<script lang="ts">
	// The composer (DESIGN 4.12): ↵ queues a message for the end of the
	// agent's turn, ⌘↵ sends it now (interrupting the turn), ⇧↵ is a newline.
	import { api } from '$lib/api';
	import { lore } from '$lib/lore';

	let { runId, onsent }: { runId: string; onsent: () => void } = $props();

	let draft = $state('');
	let sending = $state(false);
	let error = $state('');
	let ta = $state<HTMLTextAreaElement | null>(null);

	async function send(interrupt: boolean) {
		const text = draft.trim();
		if (!text || sending) return;
		sending = true;
		error = '';
		try {
			await api.runs.sendMessage(runId, text, interrupt);
			draft = '';
			onsent();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			sending = false;
		}
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			void send(e.metaKey || e.ctrlKey);
		}
	}

	// Grow with the text, up to a third of the window.
	$effect(() => {
		void draft;
		if (!ta) return;
		ta.style.height = 'auto';
		ta.style.height = `${Math.min(ta.scrollHeight, 220)}px`;
	});
</script>

<div class="comp lg">
	<textarea
		bind:this={ta}
		bind:value={draft}
		rows="1"
		placeholder={lore('composer')}
		onkeydown={onKey}
		disabled={sending}
		aria-label="Message the agent"></textarea>
	<div class="foot">
		<span class="keys">
			<span><span class="k">↵</span> send, queued</span>
			<span><span class="k">⌘↵</span> send now, interrupts</span>
			<span class="nl"><span class="k">⇧↵</span> newline</span>
		</span>
		{#if error}<span class="err" title={error}>{error}</span>{/if}
		<span class="sp"></span>
		<button
			class="now"
			onclick={() => send(true)}
			disabled={sending || !draft.trim()}
			title="Cancel the agent's turn now and have it read this">Send now</button
		>
		<button
			class="btn-primary"
			onclick={() => send(false)}
			disabled={sending || !draft.trim()}
			title="Delivered when the agent finishes its current turn">Send</button
		>
	</div>
</div>

<style>
	.comp {
		flex: none;
		margin: 0 12px 12px;
		border-radius: 20px;
		display: flex;
		flex-direction: column;
	}
	textarea {
		all: unset;
		box-sizing: border-box;
		width: 100%;
		min-height: 44px;
		padding: 11px 14px;
		font-size: 13px;
		line-height: 1.5;
		color: var(--tx);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		resize: none;
	}
	textarea:focus {
		border: 0;
		box-shadow: none;
		outline: none;
	}
	.comp:focus-within {
		border-color: var(--brandLn);
	}
	textarea::placeholder {
		color: var(--fnt);
	}
	.foot {
		display: flex;
		align-items: center;
		gap: 14px;
		padding: 6px 6px 6px 14px;
		font-size: 11px;
		color: var(--fnt);
		min-width: 0;
	}
	.keys {
		display: flex;
		gap: 14px;
		white-space: nowrap;
		overflow: hidden;
		min-width: 0;
	}
	.k {
		font-family: var(--mono);
		color: var(--mut);
	}
	.err {
		color: var(--ros);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
	.sp {
		flex: 1;
	}
	.now {
		padding: 5px 12px;
		background: var(--p3);
		border-color: transparent;
		box-shadow: none;
	}
	.btn-primary {
		padding: 5px 14px;
	}
	@container (max-width: 520px) {
		.keys .nl,
		.keys > span:nth-child(2) {
			display: none;
		}
	}
</style>
