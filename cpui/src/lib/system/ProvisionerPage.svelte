<script lang="ts">
	// System → Provisioner (DESIGN 7.10): where workspaces are built and how
	// many, then when they and stored content are given back. Three blocks,
	// each saved on its own: the cluster (the instance's provisioner), the
	// limits (instance caps, an admin's), and the reaper and storage (one's
	// own settings).
	import ProvisionerCluster from './ProvisionerCluster.svelte';
	import LimitsSection from './LimitsSection.svelte';
	import RetentionSettings from './RetentionSettings.svelte';
</script>

<div class="prov">
	<ProvisionerCluster />
	<LimitsSection />
	<RetentionSettings />
</div>

<style>
	.prov {
		display: flex;
		flex-direction: column;
		gap: 14px;
		max-width: var(--settings-max);
	}
</style>
