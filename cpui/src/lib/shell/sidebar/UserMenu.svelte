<script lang="ts">
	// The user card's menu: the account page, the admin's users page, sign-out.
	import { goto } from '$app/navigation';
	import { session } from '$lib/app/session.svelte';
	import { errorMessage } from '$lib/core/errors';
	import { t } from '$lib/i18n/index.svelte';
	import { to } from '$lib/core/urls';
	import Popover from '$lib/ui/Popover.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/design/icons';

	let {
		anchor,
		open = $bindable(false),
		onnavigate
	}: { anchor: HTMLElement | null; open: boolean; onnavigate?: () => void } = $props();

	const user = $derived(session.user);

	function go(href: string) {
		open = false;
		onnavigate?.();
		void goto(href);
	}
	async function signOut() {
		open = false;
		try {
			await session.logout();
		} catch (e) {
			toast(errorMessage(e));
		}
	}
</script>

<Popover
	{anchor}
	bind:open
	placement="above-start"
	flip
	width={240}
	title={user?.name}
	legend="@{user?.handle}"
>
	<button class="menu-item" onclick={() => go(to.account())}>
		<span class="ic">{I.members}</span><span>{t('shell.sidebar.account')}</span>
	</button>
	{#if session.admin}
		<button class="menu-item" onclick={() => go(to.users())}>
			<span class="ic">{I.members}</span><span>{t('shell.sidebar.users')}</span>
		</button>
	{/if}
	<div class="menu-div"></div>
	<button class="menu-item" onclick={signOut}>
		<span class="ic">{I.back}</span><span>{t('shell.sidebar.signOut')}</span>
	</button>
</Popover>

<style>
	.ic {
		width: 16px;
		text-align: center;
		font-size: var(--fs-body);
		color: var(--text-muted);
	}
	/* Menu rows: base .menu-item is a link row; a button needs the same reset. */
	button.menu-item {
		width: 100%;
		display: flex;
		align-items: center;
		gap: 10px;
		text-align: left;
		font-weight: 400;
	}
</style>
