<script lang="ts">
	// The user card: the person and the theme, one inset. The person is the
	// user menu's trigger: avatar, name and handle, or just the avatar on the
	// rail, where the theme comes first.
	import { session } from '$lib/app/session.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { initialsOf } from '$lib/core/urls';
	import UserAvatar from '$lib/ui/UserAvatar.svelte';
	import { I } from '$lib/design/icons';
	import ThemePicker from '../ThemePicker.svelte';

	let {
		rail = false,
		btn = $bindable(null),
		onmenu
	}: {
		rail?: boolean;
		/** The trigger, for the menu to open beside. */
		btn: HTMLElement | null;
		onmenu: () => void;
	} = $props();

	const user = $derived(session.user);
	const initials = $derived(initialsOf(user?.name ?? user?.handle ?? '?'));
</script>

{#if rail}
	<div class="ucard rcard inset">
		<ThemePicker rail />
		<span class="udiv"></span>
		<button
			bind:this={btn}
			class="ruser"
			title={user?.name}
			aria-label={t('shell.sidebar.userMenu')}
			onclick={() => onmenu()}
		>
			<UserAvatar name={user?.name ?? ''} {initials} presence />
		</button>
	</div>
{:else}
	<div class="ucard inset">
		<button
			bind:this={btn}
			class="who"
			aria-label={t('shell.sidebar.userMenu')}
			onclick={() => onmenu()}
		>
			<UserAvatar name={user?.name ?? ''} {initials} presence />
			<span class="ut">
				<span class="un truncate">{user?.name}</span>
				<span class="ur truncate"
					>@{user?.handle}{#if user?.admin}
						· {t('shell.sidebar.admin')}{/if}</span
				>
			</span>
			<span class="sw">{I.switcher}</span>
		</button>
		<span class="udiv"></span>
		<ThemePicker />
	</div>
{/if}

<style>
	.ucard {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		padding: 3px;
		border-radius: var(--r-btn);
	}
	.rcard {
		flex: none;
		align-items: center;
		gap: 2px;
	}
	.udiv {
		flex: none;
		align-self: stretch;
		height: 1px;
		margin: 2px 6px;
		background: var(--line);
	}
	.ruser {
		width: 40px;
		height: 38px;
		padding: 0;
		background: none;
		border: 0;
		box-shadow: none;
		border-radius: var(--r-tab);
	}
	.ruser:hover {
		background: var(--fill-3);
	}
	.who {
		min-width: 0;
		display: flex;
		justify-content: flex-start;
		gap: 9px;
		padding: 5px 7px;
		background: none;
		border: 0;
		box-shadow: none;
		text-align: left;
		font-weight: 400;
		border-radius: var(--r-tab);
	}
	.who:hover {
		background: var(--fill-3);
	}
	.ut {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		line-height: 1.2;
	}
	.un {
		font-size: var(--fs-ui);
		font-weight: 600;
	}
	.ur {
		font-size: var(--fs-meta);
		color: var(--text-muted);
	}
	.sw {
		flex: none;
		color: var(--text-muted);
		font-size: var(--fs-meta);
	}
</style>
