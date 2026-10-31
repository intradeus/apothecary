<script lang="ts">
	// 5 · Services: the workshop's services (its app, a database), in the
	// order the run page lists them.
	import ServicesEditor, { type ServiceRow } from '$lib/workshop/ServicesEditor.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import FormPane from './FormPane.svelte';
	import FormSub from './FormSub.svelte';

	let {
		rows = $bindable(),
		issues,
		strict
	}: { rows: ServiceRow[]; issues: Map<number, string[]>; strict: boolean } = $props();
</script>

<FormPane id="services" step={5} title={t('form.services.head')} lead={t('form.services.sub')}>
	<FormSub>
		<div class="row">
			<h3>{t('form.services.listHead')}</h3>
			<span class="count">{rows.length}</span>
		</div>
		<div class="help">{@html th('form.services.help')}</div>
		<div class="help">{@html th('form.services.flowHelp')}</div>
		<ServicesEditor bind:rows {issues} {strict} />
	</FormSub>
</FormPane>
