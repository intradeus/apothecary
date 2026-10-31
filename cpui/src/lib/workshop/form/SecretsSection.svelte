<script lang="ts">
	// 7 · Secrets, saved as they are set (only a workshop that exists has any).
	import SecretsEditor from '$lib/workshop/SecretsEditor.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import FormPane from './FormPane.svelte';
	import FormSub from './FormSub.svelte';

	let { projectId }: { projectId: string } = $props();
</script>

<FormPane id="secrets" step={7} title={t('form.secrets.head')} lead={t('form.secrets.sub')}>
	<FormSub>
		<div class="help">{@html th('form.secrets.help')}</div>
		<SecretsEditor {projectId} />
	</FormSub>
</FormPane>
