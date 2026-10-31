<script lang="ts">
	// 4 · Workspace: what a run's sandbox may reach, and the editor it opens.
	import TagInput from '$lib/ui/TagInput.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import FormPane from './FormPane.svelte';
	import FormSub from './FormSub.svelte';
	import EgressPolicy from './EgressPolicy.svelte';
	import EditorDefaults, { type EditorFields } from './EditorDefaults.svelte';

	let {
		blockedDomains,
		egressMode = $bindable(),
		allowDomains = $bindable(),
		denyDomains = $bindable(),
		codeServerExtensions = $bindable(),
		editor = $bindable(),
		onerror
	}: {
		blockedDomains: string[];
		egressMode: 'allow' | 'deny';
		allowDomains: string[];
		denyDomains: string[];
		codeServerExtensions: string[];
		editor: EditorFields;
		onerror: (message: string) => void;
	} = $props();
</script>

<FormPane id="workspace" step={4} title={t('form.project.workspaceHead')}>
	{#snippet lead()}{@html th('form.project.workspaceSub')}{/snippet}

	<EgressPolicy {blockedDomains} bind:egressMode bind:allowDomains bind:denyDomains {onerror} />

	<FormSub>
		<div class="row">
			<h3>{t('form.project.extensions')}</h3>
			<span class="count">{codeServerExtensions.length}</span>
		</div>
		<TagInput bind:values={codeServerExtensions} placeholder="golang.go, ms-python.python" />
		<div class="help">{t('form.project.extensionsHelp')}</div>
	</FormSub>

	<EditorDefaults bind:editor />
</FormPane>
