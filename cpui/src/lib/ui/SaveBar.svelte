<script lang="ts">
	// The sticky save bar at the foot of an editable page (settings, setup):
	// a note on the left, the unsaved indicator, Discard and Save. "Saved"
	// confirms for a few seconds after a save lands (completion feedback).
	import type { Snippet } from 'svelte';

	let {
		dirty,
		busy = false,
		savedAt = null,
		note,
		saveLabel = 'Save',
		/** Save is the surrounding form's submit button (native validation
		 *  runs first); otherwise it calls onsave. */
		submit = false,
		onsave,
		ondiscard,
		extra
	}: {
		dirty: boolean;
		busy?: boolean;
		/** ms epoch of the last successful save. */
		savedAt?: number | null;
		note?: string;
		saveLabel?: string;
		submit?: boolean;
		onsave?: () => void;
		ondiscard?: () => void;
		/** Extra controls before the buttons (a "run envcheck after" toggle). */
		extra?: Snippet;
	} = $props();

	let now = $state(Date.now());
	$effect(() => {
		if (!savedAt) return;
		now = Date.now();
		const t = setTimeout(() => (now = Date.now()), 4000);
		return () => clearTimeout(t);
	});
	const justSaved = $derived(!!savedAt && !dirty && now - savedAt < 4000);
</script>

<div class="sb lg">
	{#if note}<span class="note-t">{note}</span>{/if}
	<span class="grow"></span>
	{#if dirty}
		<span class="unsaved">Unsaved changes</span>
	{:else if justSaved}
		<span class="saved materialize">Saved</span>
	{/if}
	{#if extra}{@render extra()}{/if}
	{#if ondiscard}<button
			type="button"
			class="btn-ghost"
			disabled={!dirty || busy}
			onclick={ondiscard}>Discard</button
		>{/if}
	<button
		type={submit ? 'submit' : 'button'}
		class="btn-primary btn-md"
		disabled={!dirty || busy}
		aria-busy={busy}
		onclick={submit ? undefined : onsave}>{saveLabel}</button
	>
</div>

<style>
	.sb {
		position: sticky;
		bottom: 12px;
		z-index: var(--z-float);
		display: flex;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
		padding: 8px 10px 8px 14px;
		border-radius: var(--r-group);
	}
	.note-t {
		font-size: var(--fs-help);
		color: var(--mut);
	}
	.saved {
		font-size: var(--fs-meta);
		font-weight: 600;
		color: var(--grn);
	}
	.saved::before {
		content: '✓ ';
	}
</style>
