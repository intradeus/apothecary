<script lang="ts">
	// Members & roles (DESIGN 7.10). The engine has no user model yet: the one
	// local operator is the whole guild, and the table says so honestly.
	import { currentGuild, ME } from '$lib/guild';
	import EmptyState from '$lib/ui/EmptyState.svelte';

	const guild = $derived(currentGuild());
	const ROLES = ['Owner', 'Maintainer', 'Engineer', 'Viewer'] as const;
</script>

<section class="c pane">
	<div class="pane-head">
		<h2>Members</h2>
		<span class="mut small">1 member</span>
	</div>
	<div class="rows">
		<div class="r head mrow">
			<span></span><span>Name</span><span>Email</span><span>Role</span><span class="num"
				>Last active</span
			>
		</div>
		<div class="r mrow">
			<span class="av" aria-hidden="true">{ME.initials}<span class="pres"></span></span>
			<span class="nm ell">{ME.name} <span class="you">you</span></span>
			<span class="mut ell">local operator · no sign-in</span>
			<span>
				<select disabled aria-label="Role" value={guild.role}>
					{#each ROLES as r (r)}<option value={r}>{r}</option>{/each}
				</select>
			</span>
			<span class="num mut">now</span>
		</div>
	</div>
</section>

<EmptyState
	title="Members arrive with multi-user support"
	text="This engine runs for one operator. Invites, roles and per-guild access come with accounts."
>
	<button class="btn-notyet" disabled>+ Invite</button>
</EmptyState>

<style>
	.mrow {
		grid-template-columns: 30px minmax(0, 1.2fr) minmax(0, 1.4fr) 150px 90px;
	}
	.av {
		position: relative;
		width: 30px;
		height: 30px;
		border-radius: 50%;
		background: linear-gradient(160deg, #f6efdc, #c9b98e);
		color: #2a2210;
		font-size: 10.5px;
		font-weight: 700;
		display: flex;
		align-items: center;
		justify-content: center;
	}
	.pres {
		position: absolute;
		right: -1px;
		bottom: -1px;
		width: 9px;
		height: 9px;
		border-radius: 50%;
		background: var(--grn);
		box-shadow: 0 0 0 2px var(--body);
	}
	.nm {
		font-weight: 600;
	}
	.you {
		font-weight: 500;
		font-size: 11px;
		color: var(--mut);
		margin-left: 4px;
	}
	select {
		padding-top: 4px;
		padding-bottom: 4px;
		font-size: 12px;
	}
</style>
