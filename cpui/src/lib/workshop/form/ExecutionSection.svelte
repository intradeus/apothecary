<script lang="ts">
	// 3 · Execution: the provisioner and the workshop's SET of available
	// harness integrations, no default. The FLOW names the harness kind
	// (defaults.harness / a stage override); the workshop just needs an
	// integration of that kind (for auth + image layers). At most one per
	// kind so kind→integration is unambiguous.
	import type { Integration } from '$lib/domain';
	import { to } from '$lib/core/urls';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import StatusMark from '$lib/ui/StatusMark.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import FormPane from './FormPane.svelte';
	import FormSub from './FormSub.svelte';

	let {
		provisioners,
		harnesses,
		provisionerId = $bindable(),
		harnessIds = $bindable()
	}: {
		provisioners: Integration[];
		harnesses: Integration[];
		provisionerId: string;
		harnessIds: string[];
	} = $props();

	// A kind is taken if a DIFFERENT checked harness already covers it.
	const kindTaken = (h: Integration) =>
		harnessIds.some((id) => id !== h.id && harnesses.find((x) => x.id === id)?.kind === h.kind);
	function toggleHarness(h: Integration) {
		harnessIds = harnessIds.includes(h.id)
			? harnessIds.filter((id) => id !== h.id)
			: [...harnessIds, h.id];
	}

	const provisionerOptions = $derived<SelectOption[]>(
		provisioners.map((p) => ({ value: p.id, label: `${p.name} (${p.kind})` }))
	);
</script>

<FormPane
	id="execution"
	step={3}
	title={t('form.project.executionHead')}
	lead={t('form.project.executionSub')}
>
	<div class="grid2">
		<div class="field">
			<label for="p-prov">{t('form.project.provisioner')}</label>
			<Select id="p-prov" options={provisionerOptions} bind:value={provisionerId} required />
			{#if provisioners.length === 0}
				<div class="error">
					{@html th('form.project.noProvisioner', { href: to.system('integrations') })}
				</div>
			{/if}
		</div>
	</div>
	<FormSub>
		<div class="row">
			<h3>{t('form.project.harnesses')}</h3>
			<span class="count">{harnessIds.length}</span>
		</div>
		<div class="help">{t('form.project.harnessesHelp')}</div>
		<div class="hs">
			{#each harnesses as h (h.id)}
				{@const on = harnessIds.includes(h.id)}
				{@const blocked = kindTaken(h) && !on}
				<button
					type="button"
					class="rowbtn hopt"
					class:on
					disabled={blocked}
					role="checkbox"
					aria-checked={on}
					title={blocked ? t('form.project.kindTaken', { kind: h.kind }) : ''}
					onclick={() => toggleHarness(h)}
				>
					<StatusMark
						tone={on ? 'ok' : 'pending'}
						size="sm"
						title={on ? t('form.project.picked') : t('form.project.notPicked')}
					/>
					<span class="hn truncate">{h.name}</span>
					<span class="pill sm tint-muted">{h.kind}</span>
					{#if blocked}<span class="faint tiny">{t('form.project.kindTakenTag')}</span>{/if}
				</button>
			{:else}
				<div class="error">
					{@html th('form.project.noHarness', { href: to.system('integrations') })}
				</div>
			{/each}
		</div>
	</FormSub>
</FormPane>

<style>
	.hs {
		display: flex;
		flex-direction: column;
		gap: 5px;
		margin-top: 4px;
		max-width: 520px;
	}
	.hopt {
		gap: 10px;
		border: 1px solid var(--line);
		background: var(--fill-1);
		transition:
			background var(--dur-fade) var(--ease-out),
			border-color var(--dur-fade) var(--ease-out);
	}
	.hopt.on {
		background: var(--success-bg);
		border-color: var(--success-line);
		box-shadow: none;
	}
	.hopt:disabled {
		opacity: var(--o-dim);
		cursor: not-allowed;
	}
	.hn {
		flex: 1;
		font-weight: 500;
		font-size: var(--fs-ui);
	}
</style>
