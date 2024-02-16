package guardian

import rego.v1

import input.sender as sender

deny contains msg if {
	allowed_teams := ["breakglass", "platform-admins", "organization-admins"]
	found := [team |
		some team in allowed_teams
		team in input.teams
	]
	count(found) == 0
	msg := sprintf("permission denied: user is not in one of the allowed teams %s", [allowed_teams])
}
