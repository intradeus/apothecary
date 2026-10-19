<script lang="ts">
	// Ask for a new flow's name (New flow, Clone). The name is the flow's
	// immutable id: a lowercase slug.
	import { untrack, type Snippet } from 'svelte';
	import Modal from '$lib/ui/Modal.svelte';
	import { I } from '$lib/ui/icons';
	import SlugField, { slugOk } from './SlugField.svelte';

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
	const bad = $derived(!!trimmed && !slugOk(trimmed));
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

<Modal bind:open {title} icon={I.rx} {onclose}>
	<form id="name-flow" class="nf" onsubmit={submit}>
		{#if children}<div class="intro">{@render children()}</div>{/if}
		<SlugField
			id="nf-name"
			bind:value={name}
			placeholder="v2-fast"
			conflict={dup ? `This workshop already has a flow called ${trimmed}.` : ''}
			help="The flow’s id — it can’t be renamed later."
		/>
		{#if error}<div class="err">{error}</div>{/if}
	</form>
	{#snippet footer()}
		<button type="button" class="btn-ghost" onclick={onclose}>Cancel</button>
		<button
			class="btn-primary"
			type="submit"
			form="name-flow"
			disabled={busy || !trimmed || bad || dup}
			aria-busy={busy}>{busy ? 'Working…' : action}</button
		>
	{/snippet}
</Modal>

<style>
	.nf {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.intro :global(p) {
		margin: 0;
	}
</style>
