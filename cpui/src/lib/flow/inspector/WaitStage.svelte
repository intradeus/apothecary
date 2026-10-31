<script lang="ts">
	// A wait stage: the signal it stands still for, and how long.
	import type { SignalSpec } from '$lib/domain';
	import Select from '$lib/ui/Select.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import { portLabel } from '$lib/core/format';
	import OptionalDuration from '$lib/ui/OptionalDuration.svelte';
	import { observedSignals } from '$lib/flow/inspector/inspector';
	import type { DocStage } from '$lib/domain/flowdoc';

	let {
		stage: s,
		signals,
		onchange
	}: {
		stage: DocStage;
		signals: SignalSpec[];
		onchange: () => void;
	} = $props();

	const waitSig = $derived(signals.find((g) => g.slug === s.for));
	const observed = $derived(observedSignals(signals));
</script>

<div class="field">
	<label for="st-for">{t('flow.insp.waitFor')}</label>
	<Select
		id="st-for"
		value={s.for ?? ''}
		options={[
			{ value: '', label: '—' },
			...observed.map((sig) => ({ value: sig.slug, label: sig.title }))
		]}
		onchange={(v) => {
			s.for = v || undefined;
			onchange();
		}}
	/>
	<div class="help">{t('flow.insp.waitHelp')}</div>
</div>
{#if waitSig}
	{#if waitSig.description}<div class="help">{waitSig.description}</div>{/if}
	{#if waitSig.requires}
		<div class="help">
			{@html th('flow.insp.needsIntegration', { kind: portLabel(waitSig.requires) })}
		</div>
	{/if}
{:else if s.for}
	<div class="help hue-warning">{t('flow.insp.unknownSignal', { name: s.for })}</div>
{/if}
<div class="field">
	<label for="st-wait-timeout">{t('flow.insp.timeout')}</label>
	<OptionalDuration
		id="st-wait-timeout"
		label={t('flow.insp.timeout')}
		bind:value={s.timeout}
		empty={t('flow.insp.waitsForever')}
		fallback="24h"
		{onchange}
	/>
</div>
