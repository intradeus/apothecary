<script lang="ts">
	import type { Guild } from '$lib/guild';

	let {
		guild,
		size = 26
	}: { guild: Pick<Guild, 'c1' | 'c2' | 'initials' | 'name'>; size?: number } = $props();
</script>

<span
	class="ga"
	title={guild.name}
	style:width="{size}px"
	style:height="{size}px"
	style:border-radius="{Math.round(size * 0.35)}px"
	style:font-size="{Math.round(size * 0.4)}px"
	style:background="linear-gradient(160deg, {guild.c1}, {guild.c2})">{guild.initials}</span
>

<style>
	.ga {
		flex: none;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		font-weight: 700;
		color: var(--deepInk);
		letter-spacing: -0.02em;
		box-shadow: var(--guildSh);
	}
</style>
