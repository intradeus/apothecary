# Workshop Reviews tab

2026-10-24, asked for by the user: a page listing the repository's merge
requests with a button to start a review.

- **API.** `GET /api/projects/{id}/reviews?repo=` — each open merge request
  (`ports.MRInfo`), `mine` when the repository host integration's own account opened it
  (the factory's runs), the merge request's ticket (looked up by its URL in
  one ticket scan) and that ticket's newest run with the run count. `POST
  /api/projects/{id}/mrs/{iid}/review {flow?, phase?, params?, repo?}` — the
  merge request must be open; its ticket is found or made
  (`tickets.Factory.ForMR`, shared with the merge_request automation through
  `FindByKey`/`CreateForMR`, so a person and the automation file reviews
  under the same ticket); 409 while one of the ticket's runs is not
  terminal; the run is created on the merge request at the flow's
  `mr_opened` entrypoint (`reviewer` by default). A ticket made by a start
  that is then refused is deleted with the refusal.
- **UI.** A Reviews tab between Tickets and Flows: others' merge requests
  first, the factory's own after them — flagged "opened by the factory", but
  reviewable like any other (the user wanted the option; only the merge_request
  automation never picks them); a row shows the newest run's state (linked)
  and "Review" / "Open the review" / "Review again". The flow is picked among those with an `mr_opened` entrypoint,
  remembered per workshop in localStorage (`reviewer` when present).
  Refreshes every 30s while visible.
- **Fake repository host per locator.** The dev `fake` repository host kind used to hand out a
  new, empty repository host on every resolution; it now keeps one per repository
  locator for the process (`kinds.FakeRepoHostOf`), which is what made the API
  test possible — and the dev world now remembers its merge requests.
  Tests sharing it reset it (`fake.RepoHost.Reset`).
- Not done: a repository picker for extra repositories, CI state per row
  (one more repository host read per merge request per refresh).
