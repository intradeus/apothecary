<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// The workshop's vial at header size, on a control-glass tile — the
	// workshop page's counterpart of the run badge.
	import { workshopVial } from '$lib/urls';
	import Vial from '$lib/ui/Vial.svelte';

	let { projectId, live = 0 }: { projectId: string; live?: number } = $props();
	const hue = $derived(workshopVial(projectId).hue);
</script>

<div class="wb c" style:--h={hue}>
	<span class="cap">{t('workshop.badge.caption')}</span>
	<span class="v"><Vial {projectId} idle={!live} /></span>
</div>

<style>
	.wb {
		position: relative;
		width: 64px;
		height: 70px;
		border-radius: var(--r-group);
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: space-between;
		padding: 7px 0 15px;
		overflow: hidden;
	}
	.wb::after {
		content: '';
		position: absolute;
		right: -18px;
		bottom: -18px;
		width: 56px;
		height: 56px;
		border-radius: 50%;
		background: radial-gradient(
			circle,
			color-mix(in oklch, var(--h) 40%, transparent),
			transparent 70%
		);
		pointer-events: none;
	}
	.cap {
		font-size: var(--fs-badge);
		font-weight: 500;
		color: var(--mut);
		letter-spacing: var(--tr-small);
	}
	.v {
		display: inline-flex;
		transform: scale(2.1);
		transform-origin: center bottom;
	}
</style>
