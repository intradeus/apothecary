// Who may do what (schema.cedarschema names the entities). Deny is the
// default: an action no permit below reaches is refused, and a forbid wins
// over any permit.

// --- The instance -----------------------------------------------------------

// Administering the instance: accounts, limits, the provisioner. Nothing
// else names the admin role: it grants no view into anyone's work.
@id("instance-admin")
permit (principal in Role::"admin", action in Action::"admin", resource is Instance);

// --- Routes with no resource in their path -----------------------------------

// One's own account, the lists (each filters itself to what its caller may
// see) and the actions a request body aims at a workshop, which the handler
// checks again on that workshop.
@id("browse")
permit (principal, action in Action::"browse", resource is Instance);

// Making configuration of one's own (a workshop, an integration, a
// credential, a skill, a channel) is a member's: a guest has only what is
// shared with it.
@id("personal")
permit (principal in Role::"member", action in Action::"personal", resource is Instance);

// --- What one owns ------------------------------------------------------------

@id("owner")
permit (principal, action in Action::"owned", resource is Resource)
when { resource.owner == principal };

// A workshop's owner holds everything in it, whoever made it.
@id("workshop-owner")
permit (principal, action in Action::"owned", resource is Resource)
when { resource has project && resource.project.owner == principal };

// --- A shared workshop: viewer < operator < maintainer ------------------------

@id("viewer")
permit (principal, action in Action::"viewer", resource is Resource)
when { resource has project && principal in resource.project.viewers };

@id("operator")
permit (principal, action in Action::"operator", resource is Resource)
when { resource has project && principal in resource.project.operators };

@id("maintainer")
permit (principal, action in Action::"maintainer", resource is Resource)
when { resource has project && principal in resource.project.maintainers };

// --- Tokens: never more than the account, only what they were given ----------

@id("token-actions")
forbid (principal, action, resource)
when { context.token.scoped && !context.token.actions.contains(action) };

// A scoped token cannot change the account: no password, no wider token,
// no leaving a workshop.
@id("token-self")
forbid (principal, action in Action::"manageself", resource)
when { context.token.scoped };

// A token limited to some workshops reaches a workshop's kind of resource
// only inside them; personal configuration is in none, and stays its.
@id("token-projects")
forbid (principal, action, resource is Resource)
when {
  context.token.limited &&
  resource.workshopKind &&
  !(resource has project && context.token.projects.contains(resource.project))
};
