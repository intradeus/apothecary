<script lang="ts">
	// 4 · Workspace: what a run's sandbox may reach, how big it is, and the
	// editor it opens.
	import TagInput from '$lib/ui/TagInput.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import FormPane from './FormPane.svelte';
	import FormSub from './FormSub.svelte';
	import EgressPolicy from './EgressPolicy.svelte';
	import EditorDefaults, { type EditorFields } from './EditorDefaults.svelte';
	import SandboxSizeField from './SandboxSizeField.svelte';
	import SandboxRuntimeField from './SandboxRuntimeField.svelte';
	import SandboxIdleField from './SandboxIdleField.svelte';
	import type { SandboxSize } from '$lib/domain';

	let {
		blockedDomains,
		egressMode = $bindable(),
		allowDomains = $bindable(),
		denyDomains = $bindable(),
		docker = $bindable(),
		size = $bindable(),
		runtime = $bindable(),
		idleMinutes = $bindable(),
		projectId,
		codeServerExtensions = $bindable(),
		editor = $bindable(),
		onerror
	}: {
		blockedDomains: string[];
		egressMode: 'allow' | 'deny';
		allowDomains: string[];
		denyDomains: string[];
		docker: boolean;
		size: SandboxSize;
		/** The sandbox runtime, '' the provisioner's default. */
		runtime: string;
		/** Stop-on-idle: 0 the instance's default, -1 never, else minutes. */
		idleMinutes: number;
		/** The workshop, once it exists. */
		projectId?: string;
		codeServerExtensions: string[];
		editor: EditorFields;
		onerror: (message: string) => void;
	} = $props();
</script>

<FormPane id="workspace" step={4} title={t('form.project.workspaceHead')}>
	{#snippet lead()}{@html th('form.project.workspaceSub')}{/snippet}

	<EgressPolicy {blockedDomains} bind:egressMode bind:allowDomains bind:denyDomains {onerror} />

	<FormSub>
		<Toggle bind:checked={docker} label={t('form.project.docker')} />
		<div class="help">{t('form.project.dockerHelp')}</div>
	</FormSub>

	<FormSub>
		<SandboxSizeField bind:size {projectId} />
	</FormSub>

	<SandboxRuntimeField bind:runtime {docker} />

	<FormSub>
		<SandboxIdleField bind:idleMinutes />
	</FormSub>

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
