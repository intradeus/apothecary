<script lang="ts">
	// 6 · Browser: the workshop's browser container, and how it is doing once
	// the workshop exists.
	import BrowserStatusCard from '$lib/workshop/BrowserStatusCard.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import FormPane from './FormPane.svelte';
	import FormSub from './FormSub.svelte';

	let {
		enabled = $bindable(),
		projectId,
		savedAt
	}: {
		enabled: boolean;
		/** The workshop, once it exists. */
		projectId?: string;
		savedAt: number | null;
	} = $props();
</script>

<FormPane id="browser" step={6} title={t('form.browser.head')} lead={t('form.browser.sub')}>
	<FormSub>
		<Toggle bind:checked={enabled} label={t('form.browser.toggle')} />
		<div class="help">{@html th('form.browser.help')}</div>
		<div class="help">{t('form.browser.preflightHelp')}</div>
		{#if projectId}
			<BrowserStatusCard {projectId} {enabled} {savedAt} />
		{/if}
	</FormSub>
</FormPane>
