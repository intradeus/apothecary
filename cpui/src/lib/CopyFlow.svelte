<script lang="ts">
	// Copy a flow into another project.
	//
	// This is how a good flow spreads now that flows are owned rather than
	// shared: the copy is a NEW document in the target project, with its own
	// history from this moment. Editing it never reaches back — which is the
	// point of ownership, and the thing the dialog should say out loud.
	import { api } from '$lib/api';
	import type { Flow, Project } from '$lib/domain';
	import { untrack } from 'svelte';

	let {
		flow,
		projects,
		sourceLabel,
		onclose,
		oncopied
	}: {
		flow: Flow;
		projects: Project[];
		/** Where it is being copied FROM, for the sentence at the top. */
		sourceLabel: string;
		onclose: () => void;
		oncopied: (projectId: string, name: string) => void;
	} = $props();

	// Defaults, read once: the dialog mounts fresh per copy, so the props it
	// opens with are the props it works with. untrack says that on purpose
	// rather than leaving a reactivity warning for someone to explain later.
	//
	// The target defaults to a project that is NOT the source — copying inside
	// one project is legitimate (a variant) but needs a different name, so it is
	// not the obvious first choice.
	let target = $state(
		untrack(() => projects.find((p) => p.id !== flow.projectId)?.id ?? projects[0]?.id ?? '')
	);
	let name = $state(untrack(() => flow.name));
	let busy = $state(false);
	let error = $state('');
	let el = $state<HTMLDialogElement | null>(null);

	// showModal (not open) is what makes it modal: focus trap, inert background.
	$effect(() => el?.showModal());

	const sameProject = $derived(target === flow.projectId);
	const clash = $derived(sameProject && name.trim() === flow.name);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			const copied = await api.flows.copy(flow.projectId, flow.name, target, name.trim());
			oncopied(copied.projectId, copied.name);
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}
</script>

<!-- The native dialog: it brings Escape, focus trapping and the backdrop with
     it, which is three hand-rolled behaviours (and three a11y warnings) fewer. -->
<dialog
	bind:this={el}
	class="card"
	{onclose}
	onclick={(e) => {
		// Clicking the backdrop lands on the dialog element itself.
		if (e.target === el) onclose();
	}}
>
	<form onsubmit={submit}>
		<h2 style="margin-top:0">Copy <span class="mono">{flow.name}</span></h2>
		<p class="small muted" style="margin-top:-6px">
			From {sourceLabel}. The copy is a new document with its own history — editing it never changes
			the original.
		</p>

		<label for="cf-project">Into project</label>
		<select id="cf-project" bind:value={target}>
			{#each projects as p (p.id)}
				<option value={p.id}>{p.name}{p.id === flow.projectId ? ' (same project)' : ''}</option>
			{/each}
		</select>

		<label for="cf-name" style="margin-top:10px">
			Name <span class="muted">— the document renames itself to match; the name is its id</span>
		</label>
		<input id="cf-name" bind:value={name} required pattern="[a-z][a-z0-9_-]*" class="mono" />

		{#if clash}
			<div class="error">Copying into the same project needs a different name.</div>
		{:else if error}
			<div class="error">{error}</div>
		{/if}

		<div class="row" style="margin-top:14px; gap:8px">
			<button class="primary" type="submit" disabled={busy || !target || !name.trim() || clash}>
				{busy ? 'Copying…' : 'Copy'}
			</button>
			<button type="button" onclick={onclose}>Cancel</button>
		</div>
	</form>
</dialog>

<style>
	dialog {
		width: min(460px, calc(100vw - 32px));
		border: 1px solid var(--border, #333);
		padding: 16px;
	}
	dialog::backdrop {
		background: rgba(0, 0, 0, 0.45);
	}
</style>
