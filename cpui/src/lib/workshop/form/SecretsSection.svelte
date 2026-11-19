<script lang="ts">
	// 7 · Secrets, saved as they are set (only a workshop that exists has any).
	// Even listing them needs project:secrets: without it, a notice instead.
	import SecretsEditor from '$lib/workshop/SecretsEditor.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import FormPane from './FormPane.svelte';
	import FormSub from './FormSub.svelte';

	let { projectId, canEdit = true }: { projectId: string; canEdit?: boolean } = $props();
</script>

<FormPane id="secrets" step={7} title={t('form.secrets.head')} lead={t('form.secrets.sub')}>
	<FormSub>
		<div class="help">{@html th('form.secrets.help')}</div>
		{#if canEdit}
			<SecretsEditor {projectId} />
		{:else}
			<p class="help">{t('common.needsPermission')}</p>
		{/if}
	</FormSub>
</FormPane>
