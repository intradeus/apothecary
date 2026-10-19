<script lang="ts">
	import { loreLc } from '$lib/lore';
	// Ask for a new flow's name (New flow, Clone). The name is the flow's
	// immutable id: a lowercase slug.
	import { untrack, type Snippet } from 'svelte';
	import Modal from '$lib/ui/Modal.svelte';

	let {
		title,
		initial = '',
		action,
		taken = [],
		children,
		onsubmit,
		onclose
	}: {
		title: string;
		initial?: string;
		/** The submit button's label. */
		action: string;
		/** Names already used in this workshop. */
		taken?: string[];
		/** Copy above the field. */
		children?: Snippet;
		/** Throw to show the error in the dialog. */
		onsubmit: (name: string) => Promise<void>;
		onclose: () => void;
	} = $props();

	let open = $state(true);
	let name = $state(untrack(() => initial));
	let busy = $state(false);
	let error = $state('');

	const trimmed = $derived(name.trim());
	const bad = $derived(!!trimmed && !/^[a-z][a-z0-9_-]*$/.test(trimmed));
	const dup = $derived(taken.includes(trimmed));

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (!trimmed || bad || dup) return;
		busy = true;
		error = '';
		try {
			await onsubmit(trimmed);
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
			busy = false;
		}
	}
</script>

<Modal bind:open {title} icon="℞" {onclose}>
	<form id="name-flow" class="nf" onsubmit={submit}>
		{#if children}<div class="intro">{@render children()}</div>{/if}
		<div class="field">
			<label for="nf-name">Name</label>
			<input
				id="nf-name"
				class="mono"
				bind:value={name}
				placeholder="v2-fast"
				autocomplete="off"
				spellcheck="false"
				class:invalid={bad || dup}
			/>
			{#if bad}
				<div class="err">A lowercase slug: a letter, then letters, digits, - or _.</div>
			{:else if dup}
				<div class="err">This {loreLc('workshop')} already has a flow called {trimmed}.</div>
			{:else}
				<div class="help">The flow’s id — it can’t be renamed later.</div>
			{/if}
		</div>
		{#if error}<div class="err">{error}</div>{/if}
	</form>
	{#snippet footer()}
		<button type="button" class="btn-ghost" onclick={onclose}>Cancel</button>
		<button
			class="btn-primary"
			type="submit"
			form="name-flow"
			disabled={busy || !trimmed || bad || dup}>{busy ? 'Working…' : action}</button
		>
	{/snippet}
</Modal>

<style>
	.nf {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.nf label {
		color: var(--tx);
	}
	.intro :global(p) {
		margin: 0;
	}
	/* app.css's input reset outranks its own input.mono; restate it. */
	input.mono {
		font-family: var(--mono) !important;
		font-size: 12px !important;
	}
</style>
