<script lang="ts">
	// The primary nav of the full sidebar, the bead sliding between its items.
	import { t } from '$lib/i18n/index.svelte';
	import { NAV, type NavItem } from '$lib/app/nav';
	import { bump, indicator } from '$lib/ui/motion';

	let {
		path,
		count,
		onnavigate
	}: { path: string; count: (n: NavItem) => number; onnavigate?: () => void } = $props();
</script>

<nav class="nav on-rail" aria-label={t('shell.sidebar.primary')} use:indicator>
	{#each NAV as n (n.id)}
		{@const c = count(n)}
		<a
			class="ni"
			class:on={n.match(path)}
			href={n.href()}
			aria-current={n.match(path) ? 'page' : undefined}
			onclick={onnavigate}
		>
			<span class="ic">{n.icon}</span>
			<span class="grow">{n.label}</span>
			<span class="count" class:hot={n.id === 'inbox' && c > 0} use:bump={n.id === 'inbox' ? c : 0}
				>{c || ''}</span
			>
		</a>
	{/each}
</nav>

<style>
	.nav {
		flex: none;
		display: flex;
		flex-direction: column;
		gap: 1px;
		padding: 2px 8px;
	}
	.ni {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px 10px;
		border-radius: var(--r-row);
		color: var(--text);
	}
	.ni:hover {
		background: var(--fill-1);
		text-decoration: none;
	}
	.ni:active {
		opacity: 0.75;
	}
	.ni.on {
		font-weight: 600;
	}
	.ic {
		width: 16px;
		text-align: center;
		font-size: var(--fs-body);
		color: var(--text-muted);
	}
	.count {
		color: var(--text-muted);
	}
	.count.hot {
		color: var(--on-brand);
	}
</style>
