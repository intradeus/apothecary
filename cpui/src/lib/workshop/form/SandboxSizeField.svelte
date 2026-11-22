<script lang="ts">
	// The workshop's sandbox size: its own CPUs and memory, what an unset
	// field falls back to (the instance's default, else the ceiling as a
	// limit), the instance's ceiling, and — once the workshop exists — what
	// its recent runs reached, the evidence for choosing a size. As the
	// engine does, a save checks only the fields it changes: one kept above
	// a ceiling lowered since is capped, not refused.
	import { api } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import { fmtBytes } from '$lib/core/format';
	import { untrack } from 'svelte';
	import {
		aboveCeiling,
		changedFields,
		effectiveSize,
		outOfBounds,
		sizeCap,
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
	// What the form opened with: the stored size a save is checked against.
	const saved = untrack(() => ({ ...size }));
	const changed = $derived(changedFields(saved, size));
	const over = $derived(aboveCeiling(changed, policy.ceiling));
	const capped = $derived(aboveCeiling(size, policy.ceiling).filter((k) => !over.includes(k)));
	const bad = $derived(outOfBounds(changed));
	// An unset field: the default, else the ceiling as a limit, else none.
	const unsetGets = $derived(effectiveSize(unsetSize(), policy));
	const unsetCap = $derived(sizeCap(unsetGets, policy));
	const fallback = (k: 'cpus' | 'memoryGiB', unit: string) => {
		const d = unsetGets[k];
		if (d !== null) return t('form.project.sizeDefault', { v: d, unit });
		const c = unsetCap[k];
		if (c !== null) return t('form.project.sizeUpTo', { v: c, unit });
		return t('form.project.sizeNoLimit');
	};

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
		cpus: fallback('cpus', t('ui.size.cpusUnit')),
		memoryGiB: fallback('memoryGiB', t('ui.size.memoryUnit'))
	}}
	invalid={[...over, ...bad]}
/>
{#if over.length > 0}
	<div class="error small">
		{t('form.project.sizeOverCeiling', {
			cpus: policy.ceiling.cpus ?? t('page.limits.sizeAny'),
			memory: policy.ceiling.memoryGiB ?? t('page.limits.sizeAny')
		})}
	</div>
{/if}
{#if capped.length > 0}
	<div class="help hue-warning">
		{t('form.project.sizeCapped', {
			cpus: policy.ceiling.cpus ?? t('page.limits.sizeAny'),
			memory: policy.ceiling.memoryGiB ?? t('page.limits.sizeAny')
		})}
	</div>
{/if}
{#if bad.length > 0}
	<div class="error small">{t('ui.size.bounds')}</div>
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
