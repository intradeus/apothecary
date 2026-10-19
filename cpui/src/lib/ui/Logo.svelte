<script lang="ts">
	// The mark: a glass stopper holding a parchment ℞ (DESIGN 2.6).
	import { I } from './icons';

	let { size = 30 }: { size?: number } = $props();
</script>

<span
	class="logo"
	title="Apothecary"
	style:width="{size}px"
	style:height="{size}px"
	style:border-radius="{Math.round(size * 0.38)}px"
	style:font-size="{Math.round(size * 0.65)}px">{I.rx}</span
>

<style>
	.logo {
		flex: none;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		background: var(--logoBg);
		border: 1px solid var(--logoLn);
		box-shadow: var(--logoSh);
		font-family: var(--serif);
		color: var(--parch);
		text-shadow: var(--logoTxSh);
	}
</style>
