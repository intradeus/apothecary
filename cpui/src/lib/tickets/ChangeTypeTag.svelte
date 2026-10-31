<script lang="ts">
	// A ticket's change type as a small pill: a fix stands out, a hotfix
	// more, a chore recedes. The tracker's own word for an imported issue's type rides in
	// the tooltip ("Bug → fix").
	import type { ChangeType } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';

	let { type, sourceType }: { type: ChangeType; sourceType?: string } = $props();
	const HUE: Record<ChangeType, string> = {
		feat: 'tint-attention',
		fix: 'tint-danger',
		hotfix: 'tint-hot',
		perf: 'tint-warning',
		docs: 'tint-accent-2',
		chore: 'tint-muted'
	};
</script>

<span
	class="pill sm mono {HUE[type]}"
	title={sourceType
		? t('tickets.changeTypeFrom', { type, source: sourceType })
		: t('tickets.changeType', { type })}>{type}</span
>
