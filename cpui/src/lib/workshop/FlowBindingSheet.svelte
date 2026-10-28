<script lang="ts">
	// Configure a flow for this workshop: bind values to the parameters the
	// document declares. The document stays as it is and its defaults stay
	// the fallback; a run may still override any value. A template is
	// configurable without being copied — copying is only for changing its
	// stages.
	import { api } from '$lib/api';
	import type { Flow, FlowBinding, Integration, Model, ParamDecl, Project } from '$lib/domain';
	import { KNOWN_PARAMS } from '$lib/flowdoc';
	import ParamFields from '$lib/flow/ParamFields.svelte';
	import { modelHarnessKinds } from '$lib/flow/paramValues';
	import Modal from '$lib/ui/Modal.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import { onMount } from 'svelte';

	let {
		project,
		flow,
		onclose,
		onsaved
	}: {
		project: Project;
		flow: Flow;
		onclose: () => void;
		/** The binding as saved, or null once the workshop is back to the
		 *  flow's defaults. */
		onsaved: (binding: FlowBinding | null) => void;
	} = $props();

	let open = $state(true);
	let values = $state<Record<string, string>>({});
	let stale = $state<string[]>([]);
	let integrations = $state<Integration[]>([]);
	let modelsByKind = $state<Record<string, Model[]>>({});
	let loading = $state(true);
	let busy = $state(false);
	let error = $state('');

	// The engine-known names first, in their fixed order; then the flow's own.
	const known = $derived(
		KNOWN_PARAMS.map((n) => flow.parameters.find((p) => p.name === n)).filter(
			(p): p is ParamDecl => !!p
		)
	);
	const own = $derived(flow.parameters.filter((p) => !p.known));
	const harnesses = $derived(
		integrations
			.filter((i) => i.port === 'harness' && project.harnessIds.includes(i.id))
			.map((i) => ({ kind: i.kind, label: `${i.kind} · ${i.name}`, id: i.id }))
	);
	// The harness kinds the flow's model parameters run on, as the sheet
	// stands: each model field suggests its own harness's models.
	const neededKinds = $derived(modelHarnessKinds(flow.parameters, [values]));

	onMount(async () => {
		const declared = new Set(flow.parameters.map((p) => p.name));
		try {
			const [b, ints] = await Promise.all([
				api.flows.binding(project.id, flow.name),
				api.integrations.list().catch(() => [] as Integration[])
			]);
			integrations = ints;
			const next: Record<string, string> = {};
			const gone: string[] = [];
			for (const [k, v] of Object.entries(b?.params ?? {})) {
				if (declared.has(k)) next[k] = v;
				else gone.push(k);
			}
			values = next;
			stale = gone.sort();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	});

	// Each model field suggests what its harness lists — best effort: a
	// harness that cannot list leaves the field free text. Fetched once per
	// kind.
	const asked = new Set<string>();
	$effect(() => {
		for (const kind of neededKinds) {
			const h = harnesses.find((x) => x.kind === kind);
			if (!h || asked.has(kind)) continue;
			asked.add(kind);
			api.integrations.models(h.id).then(
				(m) => (modelsByKind = { ...modelsByKind, [kind]: m }),
				() => (modelsByKind = { ...modelsByKind, [kind]: [] })
			);
		}
	});

	async function save(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			const b = await api.flows.bind(project.id, flow.name, values);
			onsaved(Object.keys(b.params).length ? b : null);
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}

	async function reset() {
		busy = true;
		error = '';
		try {
			// Nothing bound is already the goal: a missing binding is no error.
			await api.flows.unbind(project.id, flow.name).catch(() => undefined);
			onsaved(null);
		} finally {
			busy = false;
		}
	}
</script>

<Modal
	bind:open
	title={t('workshop.flows.binding.title', { name: flow.name })}
	width={640}
	{onclose}
>
	<form id="flow-binding" class="fb" onsubmit={save}>
		<p>{@html th('workshop.flows.binding.intro')}</p>
		{#if loading}
			<Skeleton lines={5} />
		{:else}
			{#if stale.length}
				<div class="help hue-amb">
					{t('workshop.flows.binding.stale', { names: stale.join(', ') })}
				</div>
			{/if}
			<h3>{t('workshop.flows.binding.known')}</h3>
			<ParamFields
				params={known}
				all={flow.parameters}
				{values}
				{harnesses}
				{modelsByKind}
				idPrefix="fb"
				onchange={(n, v) => (values[n] = v)}
			/>
			<h3>{t('workshop.flows.binding.own')}</h3>
			{#if own.length}
				<ParamFields
					params={own}
					all={flow.parameters}
					{values}
					{harnesses}
					{modelsByKind}
					idPrefix="fb"
					onchange={(n, v) => (values[n] = v)}
				/>
			{:else}
				<p class="fnt">{t('workshop.flows.binding.none')}</p>
			{/if}
		{/if}
		{#if error}<div class="err">{error}</div>{/if}
	</form>
	{#snippet footer()}
		<button type="button" class="btn-ghost" onclick={reset} disabled={busy || loading}
			>{t('workshop.flows.binding.reset')}</button
		>
		<span class="grow"></span>
		<button type="button" class="btn-ghost" onclick={onclose}>{t('common.cancel')}</button>
		<button
			class="btn-primary"
			type="submit"
			form="flow-binding"
			disabled={busy || loading}
			aria-busy={busy}
			>{busy ? t('workshop.flows.binding.saving') : t('workshop.flows.binding.save')}</button
		>
	{/snippet}
</Modal>

<style>
	.fb {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.fb p {
		margin: 0;
	}
	.fb h3 {
		margin: 6px 0 0;
		font-size: var(--fs-ui);
	}
</style>
