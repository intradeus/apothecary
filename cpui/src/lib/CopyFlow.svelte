<script lang="ts">
	// Copy a flow into another project.
	//
	// This is how a good flow spreads now that flows are owned rather than
	// shared: the copy is a NEW document in the target project, with its own
	// history from this moment. Editing it never reaches back — which is the
	// point of ownership, and the thing the dialog should say out loud.
	import { api } from '$lib/api';
	import type { Flow, Project } from '$lib/domain';
	import { lore } from '$lib/lore';
	import Modal from '$lib/ui/Modal.svelte';
	import { untrack } from 'svelte';

	let {
		flow,
		projects,
		sourceLabel,
		target: preset,
		onclose,
		oncopied
	}: {
		flow: Flow;
		projects: Project[];
		/** Where it is being copied FROM, for the sentence at the top. */
		sourceLabel: string;
		/** Preselect the target project (e.g. adopting a template into the
		 *  workshop you are looking at). */
		target?: string;
		onclose: () => void;
		oncopied: (projectId: string, name: string) => void;
	} = $props();

	// Defaults, read once: the dialog mounts fresh per copy, so the props it
	// opens with are the props it works with. untrack says that on purpose.
	//
	// Without a preset the target defaults to a project that is NOT the
	// source — copying inside one project is legitimate (a variant) but needs a
	// different name, so it is not the obvious first choice.
	let target = $state(
		untrack(
			() => preset ?? projects.find((p) => p.id !== flow.projectId)?.id ?? projects[0]?.id ?? ''
		)
	);
	// Into its own workshop (the only one, say) a copy needs a new name.
	let name = $state(untrack(() => (target === flow.projectId ? `${flow.name}-copy` : flow.name)));
	let busy = $state(false);
	let error = $state('');
	let open = $state(true);

	const sameProject = $derived(target === flow.projectId);
	const clash = $derived(sameProject && name.trim() === flow.name);
	const badName = $derived(!!name.trim() && !/^[a-z][a-z0-9_-]*$/.test(name.trim()));

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			// A template belongs to no project, so it copies from its own route
			// rather than from under a project id it does not have.
			const copied = flow.builtin
				? await api.flows.copyTemplate(flow.name, target, name.trim())
				: await api.flows.copy(flow.projectId, flow.name, target, name.trim());
			oncopied(copied.projectId, copied.name);
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}
</script>

<Modal bind:open title={flow.builtin ? `Use ${flow.name}` : `Copy ${flow.name}`} icon="⧉" {onclose}>
	<form id="copy-flow" class="cf" onsubmit={submit}>
		<p>
			From <b>{sourceLabel}</b>. The copy is a new document with its own history — editing it never
			changes the original.
		</p>
		<div class="field">
			<label for="cf-project">Into {lore('workshop').toLowerCase()}</label>
			<select id="cf-project" bind:value={target}>
				{#each projects as p (p.id)}
					<option value={p.id}>{p.name}{p.id === flow.projectId ? ' (same)' : ''}</option>
				{/each}
			</select>
		</div>
		<div class="field">
			<label for="cf-name">Name</label>
			<input
				id="cf-name"
				bind:value={name}
				required
				pattern="[a-z][a-z0-9_-]*"
				class="mono"
				class:invalid={clash || badName}
			/>
			{#if clash}
				<div class="err">
					Copying into the same {lore('workshop').toLowerCase()} needs a different name.
				</div>
			{:else if badName}
				<div class="err">A lowercase slug: a letter, then letters, digits, - or _.</div>
			{:else}
				<div class="help">The document renames itself to match; the name is its id.</div>
			{/if}
		</div>
		{#if error}<div class="err">{error}</div>{/if}
	</form>
	{#snippet footer()}
		<button type="button" class="btn-ghost" onclick={onclose}>Cancel</button>
		<button
			class="btn-primary"
			type="submit"
			form="copy-flow"
			disabled={busy || !target || !name.trim() || clash || badName}
		>
			{busy ? 'Copying…' : flow.builtin ? `Copy into ${lore('workshop').toLowerCase()}` : 'Copy'}
		</button>
	{/snippet}
</Modal>

<style>
	.cf {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.cf p {
		margin: 0;
	}
	.cf label {
		color: var(--tx);
	}
	/* app.css's input reset outranks its own input.mono; restate it. */
	input.mono {
		font-family: var(--mono) !important;
		font-size: 12px !important;
	}
</style>
