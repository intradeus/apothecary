<script lang="ts">
	// The workshop's sandbox size: its own CPUs and memory, what an unset
	// field falls back to (the instance's default), the instance's ceiling,
	// and — once the workshop exists — what its recent runs reached, the
	// evidence for choosing a size.
	import { api } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import { fmtBytes } from '$lib/core/format';
	import {
		aboveCeiling,
		unsetSize,
		usageSummary,
		type SandboxRunUsage,
		type SandboxSize
	} from '$lib/domain';
	import SizeFields from '$lib/ui/SizeFields.svelte';
	import { t } from '$lib/i18n/index.svelte';

	let {
		size = $bindable(),
		projectId
	}: {
		size: SandboxSize;
		/** The workshop, once it exists. */
		projectId?: string;
	} = $props();

	const policy = $derived(
		fleet.limits?.sandbox ?? { default: unsetSize(), ceiling: unsetSize(), idleMinutes: 0 }
	);
	const over = $derived(aboveCeiling(size, policy.ceiling));
	const fallback = (v: number | null, unit: string) =>
		v === null ? t('form.project.sizeNoLimit') : t('form.project.sizeDefault', { v, unit });

	let usage = $state<SandboxRunUsage[] | null>(null);
	$effect(() => {
		const id = projectId;
		usage = null;
		if (!id) return;
		api.projects
			.sandboxUsage(id)
			.then((u) => {
				if (projectId === id) usage = u;
			})
			// Usage is a hint beside the setting: none is no line, never an error.
			.catch(() => (usage = []));
	});
	const summary = $derived(usage ? usageSummary(usage) : null);
</script>

<h3>{t('form.project.size')}</h3>
<SizeFields
	idPrefix="ws-size"
	bind:size
	placeholder={{
		cpus: fallback(policy.default.cpus, t('ui.size.cpusUnit')),
		memoryGiB: fallback(policy.default.memoryGiB, t('ui.size.memoryUnit'))
	}}
	invalid={over}
/>
{#if over.length > 0}
	<div class="error small">
		{t('form.project.sizeOverCeiling', {
			cpus: policy.ceiling.cpus ?? t('page.limits.sizeAny'),
			memory: policy.ceiling.memoryGiB ?? t('page.limits.sizeAny')
		})}
	</div>
{/if}
<div class="help">{t('form.project.sizeHelp')}</div>
{#if summary}
	<div class="help" class:hue-danger={summary.oomKills > 0}>
		{t('form.project.sizeUsage', {
			count: summary.runs,
			peak: fmtBytes(summary.peakMem)
		})}{#if summary.oomKills > 0}
			· {t('form.project.sizeOOM', { count: summary.oomKills })}{/if}
	</div>
{:else if usage}
	<div class="help faint">{t('form.project.sizeNoUsage')}</div>
{/if}
