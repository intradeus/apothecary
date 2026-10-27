// One's policy settings: what happens to finished workspaces and stored
// bytes. Per user. The capacity caps are the deployment's (see Limits).

export interface Settings {
	destroyCompletedAfter: string;
	destroyAbandoned: string;
	/** How long never-claimed uploads survive. */
	orphanUploadsAfter: string;
	/** Whether/when snapshot CONTENT expires ('never' or a duration); the
	 *  record keeps name/sha/size either way. */
	snapshotsAfter: string;
}
