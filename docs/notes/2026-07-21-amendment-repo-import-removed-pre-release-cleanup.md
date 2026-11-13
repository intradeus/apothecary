# Amendment: repo import removed (pre-release cleanup)

The M12 one-time `.apothecary/agents/` repo import is deleted end to end
(user decision: the project is unreleased, no legacy to migrate): the
Agents-panel button, `POST /api/projects/{id}/agents/import-repo`,
`IntegrationOps.RepoAgentFiles`, the resolver implementation, and the
`RepoHost.ListDir`/`ReadFile` port methods added solely for it (gitlab,
github, fake adapters). `agentspec.Parse` stays (name-rule + spec
parsing). Reference agent prompts now live in `artifacts/agents/*.md`
(+ `artifacts/flows/v2.yaml`, the expanded-form reference flow), with the
neutral YAML specs in `examples/agents/`.
