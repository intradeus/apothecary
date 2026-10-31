<script lang="ts">
	// A service stage: a service of the project, started or stopped, and the
	// stages reading its address.
	import type { Selection } from '$lib/flow/FlowGraph.svelte';
	import Select from '$lib/ui/Select.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import { stringParams } from '$lib/flow/params';
	import Group from '$lib/flow/inspector/Group.svelte';
	import { runners } from '$lib/flow/inspector/inspector';
	import {
		SERVICE_DOS,
		flowServices,
		serviceSource,
		type DocStage,
		type FlowDoc,
		type ServiceDo
	} from '$lib/domain/flowdoc';

	let {
		doc,
		stage: s,
		onchange,
		onopen
	}: {
		doc: FlowDoc;
		stage: DocStage;
		onchange: () => void;
		onopen: (sel: Selection) => void;
	} = $props();

	// ---- services: the project's, named by a stage or read as a source -----
	// What a service stage may name: a string parameter (usually $app), or a
	// service the flow names already.
	const services = $derived(flowServices(doc));
	const serviceChoices = $derived([
		...new Set([...stringParams(doc).map((p) => '$' + p), ...services])
	]);
	const readers = $derived(
		runners(doc, (x) =>
			Object.values(x.input ?? {}).some((p) => !!s.service && p.from === serviceSource(s.service))
		)
	);
	function setServiceDo(v: ServiceDo) {
		s.do = v === 'start' ? undefined : v;
		// A stop takes no time to be healthy.
		if (v === 'stop') s.timeout = undefined;
		onchange();
	}
</script>

<div class="field">
	<label for="st-service">{t('flow.insp.service')}</label>
	<input
		id="st-service"
		class="mono"
		list="flow-services"
		bind:value={s.service}
		oninput={onchange}
		placeholder="$app"
	/>
	<datalist id="flow-services">
		{#each serviceChoices as v (v)}<option value={v}></option>{/each}
	</datalist>
	<div class="help">{t('flow.insp.serviceHelp')}</div>
</div>
<div class="field">
	<label for="st-do">{t('flow.insp.serviceDo')}</label>
	<Select
		id="st-do"
		value={s.do ?? 'start'}
		options={SERVICE_DOS.map((d) => ({ value: d, label: t(`flow.insp.serviceDo.${d}`) }))}
		onchange={(v) => setServiceDo(v as ServiceDo)}
	/>
</div>
{#if s.do !== 'stop'}
	<div class="field">
		<label for="st-svc-timeout">{t('flow.insp.timeout')}</label>
		<input
			id="st-svc-timeout"
			class="mono"
			list="flow-duration-params"
			bind:value={s.timeout}
			oninput={onchange}
			placeholder={t('flow.insp.serviceOwnTimeout')}
		/>
		<div class="help">{t('flow.insp.serviceTimeoutHelp')}</div>
	</div>
{/if}
<Group caption={t('flow.insp.serviceAddress')}>
	<div class="help">
		{@html th('flow.insp.serviceAddressHelp', {
			source: serviceSource(s.service || '$app')
		})}
	</div>
	{#if readers.length}
		<div class="chips">
			{#each readers as u (u.label)}<button class="btn-sm mono" onclick={() => onopen(u.sel)}
					>{u.label}</button
				>{/each}
		</div>
	{/if}
</Group>
