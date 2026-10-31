<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// The mark (DESIGN 2.6): the only ember fill in the UI.
	import tile from '../../assets/mark/svg/apothecary-tile-primary.svg?url';

	let { size = 36 }: { size?: number } = $props();
</script>

<img
	class="logo"
	src={tile}
	alt={t('ui.logo.title')}
	title={t('ui.logo.title')}
	width={size}
	height={size}
	style:border-radius="{Math.round(size * 0.24)}px"
/>

<style>
	.logo {
		flex: none;
		display: block;
		box-shadow: 0 0 22px var(--running-glow);
	}
</style>
