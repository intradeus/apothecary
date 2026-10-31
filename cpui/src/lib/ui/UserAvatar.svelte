<script lang="ts">
	// A person: a sky circle, initials, an optional presence dot.
	let {
		name,
		initials,
		size = 30,
		presence = false
	}: { name: string; initials: string; size?: number; presence?: boolean } = $props();
</script>

<span
	class="ua"
	title={name}
	style:width="{size}px"
	style:height="{size}px"
	style:font-size="{Math.round(size * 0.38)}px"
	>{initials}{#if presence}<span class="pr"></span>{/if}</span
>

<style>
	.ua {
		flex: none;
		position: relative;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
		background: var(--avatarBg);
		color: var(--onBrand);
		font-weight: 700;
		box-shadow: var(--avatarSh);
	}
	.pr {
		position: absolute;
		right: -1px;
		bottom: -1px;
		width: 9px;
		height: 9px;
		border-radius: 50%;
		background: var(--presence);
		border: 2px solid var(--solid);
	}
</style>
